// Package subagent implements delegated asynchronous agent sessions.
package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/dump"
	q15media "github.com/q15co/q15/systems/agent/internal/media"
	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
	"github.com/q15co/q15/systems/agent/internal/turnctx"
)

const (
	defaultWaitSeconds = 30
	maxWaitSeconds     = 300
	defaultMaxOutput   = 20000
	maxOutput          = 200000
	defaultListLimit   = 100
	maxListLimit       = 500
)

// Finished-session retention bounds. Retention is count-based rather than
// age-based: counts are what bound memory under bursty fan-outs, where wall
// age would not. Running sessions are never evicted; see
// evictFinishedSessions for the eviction rule.
const (
	// maxFinishedSessionLogs is how many finished sessions keep their full
	// event log and transcript. Older finished sessions drop only those
	// unbounded parts and keep their record (id, model, task, status, error,
	// final answer), so subagent_list and subagent_read still show that the
	// session existed and how it ended.
	maxFinishedSessionLogs = 16
	// maxFinishedSessionRecords is how many finished session records are kept
	// at all; older records are deleted from the session map entirely, which
	// is what bounds the map itself.
	maxFinishedSessionRecords = 128
)

// ModelFactory constructs model clients for delegated agents.
type ModelFactory func(modelcatalog.Model, q15media.Store) (agent.ModelClient, error)

// Skill is a delegated skill whose body and declared tools are injected into a
// sub-agent at spawn time. The subagent package owns this neutral value type
// to avoid importing internal/skills; the app layer maps from
// skills.ResolvedSkill into this type.
type Skill struct {
	Name          string
	Description   string
	Source        string
	SkillPath     string
	SkillFilePath string
	Body          string
	Tools         []string
}

// SkillResolver resolves a skill name or path into a delegated Skill. It is
// implemented by the app layer and passed into the subagent Manager to avoid
// an import cycle between internal/skills and internal/tools/subagent.
type SkillResolver interface {
	ResolveSkill(ref string) (Skill, error)
}

// StartRequest holds the parameters for starting a delegated sub-agent
// session. Using a struct avoids a long, fragile positional parameter list as
// skill support grows.
type StartRequest struct {
	ModelRef     string
	Task         string
	ExtraContext string
	AllowedTools []string
	Skills       []Skill
	MaxTurns     int
	ParentTurn   int64
	// Origin is the chat that dispatched the spawn. It is captured here
	// because the session deliberately outlives the turn that carries it, so
	// a completion has no dispatch context to read a target from. A zero
	// Origin means the session was not spawned from a chat, and no terminal
	// completion is queued.
	Origin        turnctx.Origin
	spawnReturned <-chan struct{}
}

// mediaAdaptiveClient wraps a ModelClient so that media parts are adapted to
// the delegated model's capabilities before each completion call. Subagents
// build a fresh client per Start (unlike the parent's provider-cached client),
// so the capability is bound here at construction time.
//
// providerModel is the provider-facing model identifier (e.g. "gemma3:4b")
// bound at Start from modelcatalog.Model.ProviderModel. It is passed to the
// inner provider client on every Complete call instead of the engine's
// agent-facing ref, so tagged Ollama variants resolve correctly at the API.
type mediaAdaptiveClient struct {
	registry      *modelcatalog.Registry
	boundModel    modelcatalog.Model
	inner         agent.ModelClient
	support       q15media.Support
	store         q15media.Store
	providerModel string
}

func (c *mediaAdaptiveClient) ContextForModel(ref string) agent.ModelContext {
	model := c.boundModel
	if c.registry != nil {
		if current, ok := c.registry.Lookup(model.ProviderName, model.Ref); ok {
			model = current
		}
	}
	if model.ProviderName != "" && model.Ref != "" {
		ref = model.ProviderName + "/" + model.Ref
	}
	return agent.ModelContext{
		Ref: ref, MaxContextTokens: model.MaxContextTokens, MaxOutputTokens: model.MaxOutputTokens,
	}
}

func (c *mediaAdaptiveClient) Complete(
	ctx context.Context,
	model string,
	messages []conversation.Message,
	tools []agent.ToolDefinition,
) (agent.ModelClientResult, error) {
	if c.registry != nil {
		current, ok := c.registry.Lookup(c.boundModel.ProviderName, c.boundModel.Ref)
		if !ok || current.ProviderModel != c.boundModel.ProviderModel {
			return agent.ModelClientResult{}, fmt.Errorf(
				"role primary model %q: missing capability chat in the live catalog",
				c.boundModel.ProviderModel,
			)
		}
		if err := c.registry.RequireChat("primary", current); err != nil {
			return agent.ModelClientResult{}, err
		}
	}
	adapted := q15media.AdaptMediaToCapabilities(messages, c.support, c.store)
	// Send the bound provider-facing model id (e.g. "gemma3:4b") to the inner
	// provider client rather than the engine's agent-facing ref (e.g.
	// "gemma3"). Falling back to the incoming model only when no provider model
	// was bound keeps direct wrapper construction in tests working.
	providerModel := c.providerModel
	if strings.TrimSpace(providerModel) == "" {
		providerModel = model
	}
	return c.inner.Complete(ctx, providerModel, adapted, tools)
}

// Manager tracks delegated sub-agent sessions.
type Manager struct {
	mu       sync.Mutex
	sessions map[string]*Session
	// baseCtx is the application root context: every session derives its
	// lifetime from it (see Start), so a session outlives the chat turn that
	// spawned it but is still canceled when the process shuts down.
	baseCtx       context.Context
	registry      *modelcatalog.Registry
	factory       ModelFactory
	tools         agent.ToolRegistry
	media         q15media.Store
	skillResolver SkillResolver
	dumpWriter    io.Writer
	contextFitter agent.RequestContextFitter
	publisher     CompletionPublisher
	next          uint64
	runID         string
	records       *recordStore
	closed        bool
	workers       sync.WaitGroup
}

// Session stores one delegated sub-agent lifecycle.
type Session struct {
	ID        string
	Model     string
	Task      string
	Status    string
	CreatedAt time.Time

	mu       sync.Mutex
	messages []conversation.Message
	pending  []conversation.Message
	events   []Event
	cancel   context.CancelFunc
	result   *agent.EngineResult
	err      error
	done     chan struct{}
	// The result releases synchronous spawn waits before completion delivery.
	// Waiting for spawnReturned lets that caller consume an inline result first.
	resultReady        chan struct{}
	spawnReturned      <-chan struct{}
	completionConsumed bool
	// finishedAt is recorded during run's terminal bookkeeping and orders
	// finished sessions for retention eviction. It stays zero while run is
	// active, including after Kill marks the session but before the engine
	// returns, so retention cannot drop a log that is still being written.
	finishedAt time.Time
	// finalAnswer, logEvicted and droppedEvents keep an evicted session
	// readable after retention drops its event log: the terminal state, the
	// failure reason (err) and the final answer survive the log.
	finalAnswer   string
	logEvicted    bool
	droppedEvents int
	parentTurn    int64
	// origin is the chat that spawned the session. It is captured at Start
	// because the session outlives the dispatch context that carries it.
	origin         turnctx.Origin
	runID          string
	recordPath     string
	restored       bool
	persistenceErr error
}

// Event is a buffered sub-agent progress event.
type Event struct {
	Index int    `json:"index"`
	Type  string `json:"type"`
	Text  string `json:"text,omitempty"`
	Error string `json:"error,omitempty"`
}

// NewManager constructs a sub-agent manager backed by the live model registry.
// ctx must be the application root context (process lifetime): every session
// derives its lifetime from it, so sessions outlive the chat turn that spawned
// them but are still canceled when the process shuts down.
// A nil skillResolver is valid and preserves the pre-skill behavior (no skill
// injection possible, but existing spawns work unchanged).
func NewManager(
	ctx context.Context,
	registry *modelcatalog.Registry,
	factory ModelFactory,
	tools agent.ToolRegistry,
	media q15media.Store,
	skillResolver SkillResolver,
	dumpWriter io.Writer,
	contextFitter agent.RequestContextFitter,
	publisher CompletionPublisher,
) *Manager {
	return &Manager{
		baseCtx:       ctx,
		sessions:      map[string]*Session{},
		registry:      registry,
		factory:       factory,
		tools:         tools,
		media:         media,
		skillResolver: skillResolver,
		dumpWriter:    dumpWriter,
		contextFitter: contextFitter,
		publisher:     publisher,
	}
}

// Start creates and runs a delegated sub-agent session. The session context
// derives from the manager's base context (the application root), not from
// the chat turn that dispatched the spawn: the session survives its parent
// turn ending and is canceled only by subagent_kill or process shutdown.
func (m *Manager) Start(req StartRequest) (*Session, error) {
	if m == nil {
		return nil, fmt.Errorf("subagent manager is not configured")
	}
	if m.contextFitter == nil {
		return nil, fmt.Errorf("subagent context budget is not configured")
	}
	modelRef := strings.TrimSpace(req.ModelRef)
	modelCfg, ok := m.registry.LookupByRef(modelRef)
	if !ok {
		return nil, fmt.Errorf("unknown subagent model %q", modelRef)
	}
	if strings.TrimSpace(req.Task) == "" {
		return nil, fmt.Errorf("task is required")
	}
	skills := req.Skills
	if err := m.validateSkillToolsAvailable(skills); err != nil {
		return nil, err
	}
	if err := m.registry.RequireChat("primary", modelCfg); err != nil {
		return nil, err
	}
	client, err := m.factory(modelCfg, m.media)
	if err != nil {
		return nil, fmt.Errorf("configure subagent model %q: %w", modelRef, err)
	}
	client = &mediaAdaptiveClient{
		registry:   m.registry,
		boundModel: modelCfg,
		inner:      client,
		support:    q15media.SupportFromCapabilities(modelCfg.Capabilities),
		store:      m.media,
		// Provider-facing model id (full tag) so tagged Ollama variants such
		// as "gemma3:4b" reach the provider API intact. The engine and
		// session continue to use the agent-facing ref (modelCfg.Ref).
		providerModel: modelCfg.ProviderModel,
	}
	if m.dumpWriter != nil {
		client = dump.NewModelClientDump(client, m.dumpWriter)
	}
	// allowedTools governs WHICH tools the sub-agent may use (membership and
	// intended precedence: explicit first, then skill-declared). The emitted
	// definition ORDER sent to the model follows base-registry order, since
	// FilterToolRegistry iterates the base registry.
	allowedTools := unionAllowedTools(req.AllowedTools, skills)
	registry := agent.FilterToolRegistry(m.tools, allowedTools)
	engine := agent.NewEngine(client, registry, []string{modelCfg.Ref})
	engine.SetMaxTurns(req.MaxTurns)
	msg := strings.TrimSpace(req.Task)
	if strings.TrimSpace(req.ExtraContext) != "" {
		msg += "\n\nContext:\n" + strings.TrimSpace(req.ExtraContext)
	}
	messages := []conversation.Message{
		conversation.SystemMessage(
			"You are a delegated sub-agent. Work only on the provided task. Do not assume access to the parent conversation or private memory.",
		),
	}
	if injected, ok := renderDelegatedSkills(skills); ok {
		messages = append(messages, conversation.SystemMessage(injected))
	}
	messages = append(messages, conversation.UserMessage(msg))
	// The session context derives from the manager base context, not from the
	// tool-dispatch context: the parent chat turn's context ends with the
	// turn, and deriving from it killed every live session mid-work (#393).
	// context.WithoutCancel on the dispatch context is deliberately not used
	// either: sessions must still be canceled by process shutdown, so the
	// cancel chain stays rooted at the application context.
	childCtx, cancel := context.WithCancel(m.baseCtx)
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		cancel()
		return nil, errors.New("subagent manager is closed")
	}
	id, err := m.allocateIDLocked()
	if err != nil {
		m.mu.Unlock()
		cancel()
		return nil, err
	}
	s := &Session{
		ID:            id,
		Model:         modelCfg.Ref,
		Task:          req.Task,
		Status:        "running",
		CreatedAt:     time.Now(),
		messages:      messages,
		cancel:        cancel,
		done:          make(chan struct{}),
		resultReady:   make(chan struct{}),
		spawnReturned: req.spawnReturned,
		parentTurn:    req.ParentTurn,
		origin:        req.Origin,
		runID:         m.runID,
	}
	if m.records != nil {
		s.recordPath = m.records.path(id)
		if err := m.records.save(s.recordLocked()); err != nil {
			m.mu.Unlock()
			cancel()
			return nil, fmt.Errorf("record subagent before starting: %w", err)
		}
	}
	m.sessions[id] = s
	m.workers.Add(1)
	m.mu.Unlock()
	go func() {
		defer m.workers.Done()
		m.run(childCtx, s, engine, agent.NewTurnBudget(req.MaxTurns))
	}()
	return s, nil
}

func (m *Manager) run(
	ctx context.Context,
	s *Session,
	engine *agent.Engine,
	turnBudget *agent.TurnBudget,
) {
	// Every terminal transition funnels through this defer, which makes it the
	// single point where finished-session retention is bounded. Eviction runs
	// before done is closed so a waiter unblocked by done observes the
	// post-eviction state.
	defer func() {
		if s.cancel != nil {
			s.cancel()
		}
		s.mu.Lock()
		if s.Status != "running" {
			s.finishedAt = time.Now().UTC()
		}
		m.saveSessionLocked(s)
		if s.persistenceErr != nil {
			s.err = errors.Join(s.err, s.persistenceErr)
			if s.Status == "completed" {
				s.Status = "failed"
			}
			s.addEventLocked("persistence_failed", "", s.persistenceErr)
			// A failed directory sync may still have installed the first
			// snapshot. Try to replace it with the visible failure state.
			m.saveSessionLocked(s)
		}
		s.mu.Unlock()
		if err := m.evictFinishedSessions(); err != nil {
			log.Printf("q15: subagent event=retention_failed error=%q", err)
		}
		close(s.resultReady)
		if s.spawnReturned != nil {
			<-s.spawnReturned
		}
		m.publishCompletion(s)
		close(s.done)
	}()
	observer := agent.RunObserverFunc(func(_ context.Context, ev agent.RunEvent) {
		text := ev.FinalText
		if text == "" {
			text = ev.ToolOutput
		}
		if text == "" && ev.ToolCall.Name != "" {
			text = ev.ToolCall.Name
		}
		s.mu.Lock()
		s.addEventLocked(string(ev.Type), text, ev.Err)
		if s.Status == "running" && (ev.Type == agent.RunEventToolFinished ||
			ev.Type == agent.RunEventModelTurnStarted) {
			m.saveSessionLocked(s)
		}
		s.mu.Unlock()
	})

	for {
		result, err := engine.Run(
			ctx,
			agent.EngineRequest{
				Messages:       s.snapshotMessages(),
				UseTools:       true,
				ToolCallPolicy: workspaceOnlyPolicy{},
				Observer:       observer,
				ContextFitter:  m.contextFitter,
				TurnBudget:     turnBudget,
			},
		)
		s.mu.Lock()
		if s.Status == "killed" || s.Status == "interrupted" {
			// subagent_kill marks the session before canceling it, so a killed
			// session never reaches the cancellation check below: an
			// intentional kill is terminal state killed, not a failure. The
			// distinction is session state, not error text.
			s.mu.Unlock()
			return
		}
		s.result = &result
		s.messages = append(s.messages, result.Messages...)
		s.finalAnswer = result.FinalText
		err = errors.Join(err, s.persistenceErr)
		if ctx.Err() != nil {
			// Check the context before a generic run error: cancellation may
			// cause that error, and the terminal record must retain the
			// cancellation reason rather than only a secondary failure.
			s.Status = "failed"
			s.err = fmt.Errorf("session context ended before the run finished: %w", ctx.Err())
			if err != nil {
				s.err = fmt.Errorf("%w; run error: %v", s.err, err)
			}
			s.addEventLocked("failed", result.FinalText, s.err)
			s.mu.Unlock()
			return
		}
		if err != nil {
			s.Status = "failed"
			s.err = err
			s.addEventLocked("failed", result.FinalText, err)
			s.mu.Unlock()
			return
		}
		if len(s.pending) == 0 || turnBudget.Remaining() == 0 {
			if len(s.pending) > 0 {
				s.addEventLocked("turn_limit", fmt.Sprintf(
					"Turn budget exhausted; %d queued parent messages were not processed.",
					len(s.pending),
				), nil)
			}
			s.Status = "completed"
			s.addEventLocked("completed", result.FinalText, nil)
			s.mu.Unlock()
			return
		}
		s.addEventLocked("continued", result.FinalText, nil)
		s.messages = append(s.messages, s.pending...)
		s.pending = nil
		s.mu.Unlock()
	}
}

// dropLog releases the session's event log and message transcript — the
// unbounded per-session memory — while keeping the record itself: id, model,
// task, terminal status, failure reason and final answer. Called by
// evictFinishedSessions for finished sessions only; a running session's log
// is never dropped while the engine still appends to it.
func (s *Session) dropLog() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.logEvicted {
		return
	}
	s.droppedEvents += len(s.events)
	s.events = nil
	s.messages = nil
	s.pending = nil
	s.result = nil
	s.logEvicted = true
}

// evictFinishedSessions bounds retention of finished sessions. The newest
// maxFinishedSessionLogs finished sessions keep their event log; older
// finished sessions lose the log but keep their record; beyond
// maxFinishedSessionRecords the oldest records are deleted from the map
// entirely. Sessions of any age are candidates only once finished: running
// sessions are never evicted.
func (m *Manager) evictFinishedSessions() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.evictFinishedSessionsLocked()
}

func (m *Manager) evictFinishedSessionsLocked() error {
	// finished pairs a session with its terminal timestamp, snapshotted under
	// the session lock. Lock order is m.mu then s.mu; nothing acquires them
	// in the opposite order.
	type finished struct {
		session    *Session
		finishedAt time.Time
	}
	var done []finished
	for _, s := range m.sessions {
		s.mu.Lock()
		if s.Status != "running" && !s.finishedAt.IsZero() {
			done = append(done, finished{session: s, finishedAt: s.finishedAt})
		}
		s.mu.Unlock()
	}
	if len(done) <= maxFinishedSessionLogs && len(done) <= maxFinishedSessionRecords {
		return nil
	}
	// Oldest terminal transitions are evicted first: ordering by finishedAt
	// (not CreatedAt) protects the log of a long-running session that has
	// just completed over stale logs of sessions finished earlier.
	sort.Slice(done, func(i, j int) bool {
		if done[i].finishedAt.Equal(done[j].finishedAt) {
			return done[i].session.ID < done[j].session.ID
		}
		return done[i].finishedAt.Before(done[j].finishedAt)
	})
	for i, f := range done {
		if i >= len(done)-maxFinishedSessionLogs {
			break
		}
		f.session.dropLog()
		if i < len(done)-maxFinishedSessionRecords {
			if m.records != nil {
				if err := m.records.remove(f.session.ID); err != nil {
					return err
				}
			}
			delete(m.sessions, f.session.ID)
		}
	}
	return nil
}

func (s *Session) addEventLocked(typ, text string, err error) {
	e := Event{
		Index: s.droppedEvents + len(s.events),
		Type:  typ,
		Text:  truncate(text, defaultMaxOutput),
	}
	if err != nil {
		e.Error = err.Error()
	}
	s.events = append(s.events, e)
}
func (s *Session) snapshotMessages() []conversation.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]conversation.Message(nil), s.messages...)
}

// Get returns a session by ID.
func (m *Manager) Get(id string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[strings.TrimSpace(id)]
	return s, ok
}

// List returns sessions matching a state filter.
func (m *Manager) List(state string, limit int) []*Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	if limit <= 0 || limit > maxListLimit {
		limit = defaultListLimit
	}
	out := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		if state == "" || state == "all" || s.status() == state {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (s *Session) Read(after, maxChars int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if maxChars <= 0 || maxChars > maxOutput {
		maxChars = defaultMaxOutput
	}
	footer := fmt.Sprintf("Next-Event-Index: %d", s.droppedEvents+len(s.events))
	var b strings.Builder
	fmt.Fprintf(&b, "Session-ID: %s\nStatus: %s\n", s.ID, s.Status)
	if s.err != nil {
		fmt.Fprintf(&b, "Error: %v\n", s.err)
	}
	if s.parentTurn != 0 {
		fmt.Fprintf(&b, "Parent-Turn: %d\n", s.parentTurn)
	}
	if !s.finishedAt.IsZero() {
		fmt.Fprintf(&b, "Finished-At: %s\n", s.finishedAt.Format(time.RFC3339))
	}
	if s.recordPath != "" {
		fmt.Fprintf(&b, "Record-Path: %s\n", s.recordPath)
	}
	if s.logEvicted {
		fmt.Fprintf(&b, "Event-Log: evicted by retention (%d events dropped)\n", s.droppedEvents)
	} else if s.restored {
		fmt.Fprintf(&b, "Event-Log: restored snapshot (%d of %d events retained)\n",
			len(s.events), s.droppedEvents+len(s.events))
	}
	if s.logEvicted || s.restored {
		if s.finalAnswer != "" {
			line := fmt.Sprintf("Final-Answer: %s\n", truncate(s.finalAnswer, defaultMaxOutput))
			remaining := maxChars - b.Len() - len(footer)
			if len(line) > remaining {
				const marker = "...\nOutput-Truncated: true\n"
				if remaining > len(marker) {
					b.WriteString(line[:remaining-len(marker)])
					b.WriteString(marker)
				} else {
					if len("Output-Truncated: true\n") <= remaining {
						b.WriteString("Output-Truncated: true\n")
					}
				}
			} else {
				b.WriteString(line)
			}
		}
	}
	for _, e := range s.events {
		if e.Index < int(after) {
			continue
		}
		line := fmt.Sprintf("[%d] %s", e.Index, e.Type)
		if e.Text != "" {
			line += ": " + e.Text
		}
		if e.Error != "" {
			line += ": " + e.Error
		}
		line += "\n"
		if b.Len()+len(line)+len(footer) > maxChars {
			if b.Len()+len("Output-Truncated: true\n")+len(footer) <= maxChars {
				b.WriteString("Output-Truncated: true\n")
			}
			break
		}
		b.WriteString(line)
	}
	b.WriteString(footer)
	return b.String()
}

func (s *Session) status() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Status
}

func (s *Session) Write(message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Status != "running" {
		return fmt.Errorf("session %s is %s", s.ID, s.Status)
	}
	if strings.TrimSpace(message) == "" {
		return fmt.Errorf("message is required")
	}
	s.pending = append(s.pending, conversation.UserMessage(message))
	s.addEventLocked("parent_message", message, nil)
	return nil
}

// Kill cancels a running session.
func (s *Session) Kill() {
	s.mu.Lock()
	if s.Status == "running" {
		s.Status = "killed"
		if s.cancel != nil {
			s.cancel()
		}
		s.addEventLocked("killed", "", nil)
	}
	s.mu.Unlock()
}

// Spawn starts delegated sub-agent sessions.
type Spawn struct{ manager *Manager }

// NewSpawn constructs the subagent spawn tool.
func NewSpawn(m *Manager) *Spawn { return &Spawn{m} }

// Definition returns the spawn tool schema.
func (t *Spawn) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name:        "subagent",
		Description: "Start a delegated sub-agent on a configured model",
		PromptGuidance: []string{
			"Use for isolated delegated work; provide only sanitized context.",
			"Use a vision-capable model when delegating analysis of media refs such as media://sha256/... attachments.",
			"Default tools allowlist is empty; explicitly grant only needed tools.",
			"max_turns counts model responses, includes a final answer-only turn without tools, and is shared with queued follow-up messages.",
			"A session keeps running after the chat turn that spawned it ends; it stops by completing or failing, through subagent_kill, or at process shutdown.",
			"Poll a spawned session's progress later with subagent_read or subagent_list.",
			"When an asynchronous session completes, fails, or is killed, its result returns to you in a new interactive turn. Review it and continue the original task; the result is not sent directly to the user. Poll only when you need interim progress.",
			"After a restart, unfinished sessions are listed as interrupted with their persisted diagnostic tail; they are not resumed.",
			"Give the sub-agent full /workspace/... paths for files in packages whose names collide with runtime roots (memory, skills); /memory and /skills are persistent runtime roots, not Go package paths.",
			"When delegating a skill by name or path, pass it via `skills`; its instructions are injected automatically and its declared tools are granted without listing them in `tools`.",
		},
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"model":   map[string]string{"type": "string"},
				"task":    map[string]string{"type": "string"},
				"context": map[string]string{"type": "string"},
				"tools": map[string]any{
					"type":  "array",
					"items": map[string]string{"type": "string"},
				},
				"skills": map[string]any{
					"type":        "array",
					"description": "Skill names or skill paths to inject into the delegated sub-agent",
					"items":       map[string]string{"type": "string"},
				},
				"wait_seconds": map[string]any{
					"type":    "integer",
					"minimum": 0,
					"maximum": maxWaitSeconds,
					"default": defaultWaitSeconds,
				},
				"max_turns": map[string]any{
					"type": "integer", "minimum": 1,
					"description": "Total model turns, including the final answer-only turn. The default is 96; 1 disables tools entirely.",
				},
			},
			"required":             []string{"model", "task"},
			"additionalProperties": false,
		},
	}
}

// Run starts a delegated session and waits within the parent turn's lifetime.
// Canceling the wait leaves the session running under the manager's context.
func (t *Spawn) Run(ctx context.Context, args string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var a struct {
		Model, Task, Context string
		Tools                []string `json:"tools"`
		Skills               []string `json:"skills"`
		Wait                 *int     `json:"wait_seconds"`
		MaxTurns             int      `json:"max_turns"`
	}
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "", fmt.Errorf("invalid arguments JSON: %w", err)
	}
	wait := defaultWaitSeconds
	if a.Wait != nil {
		wait = *a.Wait
	}
	if wait < 0 {
		return "", fmt.Errorf("wait_seconds must be >= 0")
	}
	if wait > maxWaitSeconds {
		wait = maxWaitSeconds
	}
	skills, err := t.manager.resolveSkillRefs(a.Skills)
	if err != nil {
		return "", err
	}
	// The chat is captured here, while the dispatch context is still alive.
	// The session outlives this context, so a completion cannot recover it.
	origin, _ := turnctx.OriginFrom(ctx)
	spawnReturned := make(chan struct{})
	defer close(spawnReturned)
	s, err := t.manager.Start(StartRequest{
		ModelRef:      a.Model,
		Task:          a.Task,
		ExtraContext:  a.Context,
		AllowedTools:  a.Tools,
		Skills:        skills,
		MaxTurns:      a.MaxTurns,
		ParentTurn:    turnctx.TurnSeqFrom(ctx),
		Origin:        origin,
		spawnReturned: spawnReturned,
	})
	if err != nil {
		return "", err
	}
	if wait > 0 {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-s.resultReady:
			s.mu.Lock()
			s.completionConsumed = true
			s.mu.Unlock()
			return s.Read(0, defaultMaxOutput), nil
		case <-time.After(time.Duration(wait) * time.Second):
		}
	}
	return fmt.Sprintf("Session-ID: %s\nStatus: running\nNext-Event-Index: 0", s.ID), nil
}

// Read polls delegated sub-agent events.
type Read struct{ manager *Manager }

// NewRead constructs the subagent_read tool.
func NewRead(m *Manager) *Read { return &Read{m} }

// Definition returns the read tool schema.
func (t *Read) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name:        "subagent_read",
		Description: "Read delegated sub-agent events",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session_id":        map[string]string{"type": "string"},
				"after_event_index": map[string]any{"type": "integer", "minimum": 0, "default": 0},
				"max_output_chars": map[string]any{
					"type":    "integer",
					"minimum": 0,
					"maximum": maxOutput,
					"default": defaultMaxOutput,
				},
			},
			"required":             []string{"session_id"},
			"additionalProperties": false,
		},
	}
}

// Run reads session events.
func (t *Read) Run(_ context.Context, args string) (string, error) {
	var a struct {
		SessionID string `json:"session_id"`
		After     int    `json:"after_event_index"`
		Max       int    `json:"max_output_chars"`
	}
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "", fmt.Errorf("invalid arguments JSON: %w", err)
	}
	s, ok := t.manager.Get(a.SessionID)
	if !ok {
		return "", fmt.Errorf("unknown subagent session %q", a.SessionID)
	}
	return s.Read(a.After, a.Max), nil
}

// Write appends follow-up messages to sessions.
type Write struct{ manager *Manager }

// NewWrite constructs the subagent_write tool.
func NewWrite(m *Manager) *Write { return &Write{m} }

// Definition returns the write tool schema.
func (t *Write) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name:        "subagent_write",
		Description: "Append a follow-up message to a running sub-agent session",
		PromptGuidance: []string{
			"The follow-up text goes in the `message` field; do not use `data` or `input`.",
		},
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session_id": map[string]string{"type": "string"},
				"message":    map[string]string{"type": "string"},
				"close":      map[string]string{"type": "boolean"},
			},
			"required":             []string{"session_id", "message"},
			"additionalProperties": false,
		},
	}
}

// Run appends a follow-up message to a session.
func (t *Write) Run(_ context.Context, args string) (string, error) {
	var a struct {
		SessionID string `json:"session_id"`
		Message   string `json:"message"`
		Close     bool   `json:"close"`
	}
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "", fmt.Errorf("invalid arguments JSON: %w", err)
	}
	s, ok := t.manager.Get(a.SessionID)
	if !ok {
		return "", fmt.Errorf("unknown subagent session %q", a.SessionID)
	}
	if err := s.Write(a.Message); err != nil {
		return "", err
	}
	return fmt.Sprintf("Session-ID: %s\nStatus: %s\nMessage-Appended: true", s.ID, s.status()), nil
}

// List enumerates delegated sub-agent sessions.
type List struct{ manager *Manager }

// NewList constructs the subagent_list tool.
func NewList(m *Manager) *List { return &List{m} }

// Definition returns the list tool schema.
func (t *List) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name:        "subagent_list",
		Description: "List delegated sub-agent sessions",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"state": map[string]any{
					"type": "string",
					"enum": []string{
						"all",
						"running",
						"completed",
						"failed",
						"killed",
						"interrupted",
					},
					"default": "all",
				},
				"limit": map[string]any{
					"type":    "integer",
					"minimum": 0,
					"maximum": maxListLimit,
					"default": defaultListLimit,
				},
			},
			"additionalProperties": false,
		},
	}
}

// Run lists sessions.
func (t *List) Run(_ context.Context, args string) (string, error) {
	if strings.TrimSpace(args) == "" {
		args = "{}"
	}
	var a struct {
		State string
		Limit int
	}
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "", fmt.Errorf("invalid arguments JSON: %w", err)
	}
	if a.State == "" {
		a.State = "all"
	}
	var b strings.Builder
	for _, s := range t.manager.List(a.State, a.Limit) {
		s.mu.Lock()
		fmt.Fprintf(
			&b,
			"Session-ID: %s\nModel: %s\nStatus: %s\nTask: %s\nCreated-At: %s\n",
			s.ID,
			s.Model,
			s.Status,
			truncate(s.Task, 200),
			s.CreatedAt.Format(time.RFC3339),
		)
		if s.parentTurn != 0 {
			fmt.Fprintf(&b, "Parent-Turn: %d\n", s.parentTurn)
		}
		if !s.finishedAt.IsZero() {
			fmt.Fprintf(&b, "Finished-At: %s\n", s.finishedAt.Format(time.RFC3339))
		}
		fmt.Fprintf(&b, "Last-Event-Index: %d\n", s.droppedEvents+len(s.events)-1)
		if s.err != nil {
			fmt.Fprintf(&b, "Error: %v\n", s.err)
		}
		if s.recordPath != "" {
			fmt.Fprintf(&b, "Record-Path: %s\n", s.recordPath)
		}
		b.WriteByte('\n')
		s.mu.Unlock()
	}
	if b.Len() == 0 {
		return "No sub-agent sessions found.", nil
	}
	return strings.TrimSpace(b.String()), nil
}

// Kill cancels delegated sub-agent sessions.
type Kill struct{ manager *Manager }

// NewKill constructs the subagent_kill tool.
func NewKill(m *Manager) *Kill { return &Kill{m} }

// Definition returns the kill tool schema.
func (t *Kill) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name:        "subagent_kill",
		Description: "Cancel a running delegated sub-agent session",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session_id": map[string]string{"type": "string"},
				"force":      map[string]string{"type": "boolean"},
			},
			"required":             []string{"session_id"},
			"additionalProperties": false,
		},
	}
}

// Run cancels a delegated sub-agent session.
func (t *Kill) Run(_ context.Context, args string) (string, error) {
	var a struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "", fmt.Errorf("invalid arguments JSON: %w", err)
	}
	s, ok := t.manager.Get(a.SessionID)
	if !ok {
		return "", fmt.Errorf("unknown subagent session %q", a.SessionID)
	}
	s.Kill()
	return fmt.Sprintf("Session-ID: %s\nStatus: %s", s.ID, s.status()), nil
}

// resolveSkillRefs resolves skill name/path refs from the spawn tool into
// delegated Skill values using the configured SkillResolver.
func (m *Manager) resolveSkillRefs(refs []string) ([]Skill, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	if m.skillResolver == nil {
		return nil, fmt.Errorf(
			"skill delegation is not configured: no skill resolver available",
		)
	}
	resolved := make([]Skill, 0, len(refs))
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			return nil, fmt.Errorf("skill ref is empty")
		}
		skill, err := m.skillResolver.ResolveSkill(ref)
		if err != nil {
			return nil, fmt.Errorf("resolve delegated skill %q: %w", ref, err)
		}
		resolved = append(resolved, skill)
	}
	return resolved, nil
}

// unionAllowedTools merges explicit tools (parent intent, first) with
// skill-declared tools (in skills-list order, then each skill's Tools order).
// The result is deduplicated in first-seen order. If both inputs are empty the
// return value is nil so agent.FilterToolRegistry still exposes zero tools
// (preserving the no-tools default).
func unionAllowedTools(explicit []string, skills []Skill) []string {
	var count int
	for _, name := range explicit {
		if strings.TrimSpace(name) != "" {
			count++
		}
	}
	for _, skill := range skills {
		count += len(skill.Tools)
	}
	if count == 0 {
		return nil
	}
	seen := make(map[string]struct{}, count)
	out := make([]string, 0, count)
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		if _, dup := seen[name]; dup {
			return
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	for _, name := range explicit {
		add(name)
	}
	for _, skill := range skills {
		for _, name := range skill.Tools {
			add(name)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// validateSkillToolsAvailable checks that every tool declared by a delegated
// skill is registered in the live tool registry. This catches declared
// dependencies that cannot be satisfied by the current deployment at spawn
// time, when the registry is known. Explicit tools are not validated here
// (existing behavior allows unknown explicit names to pass through).
func (m *Manager) validateSkillToolsAvailable(skills []Skill) error {
	if len(skills) == 0 {
		return nil
	}
	if m.tools == nil {
		for _, skill := range skills {
			if len(skill.Tools) > 0 {
				return fmt.Errorf(
					"tool registry not configured; cannot validate declared tools for delegated skill %q",
					skill.Name,
				)
			}
		}
		return nil
	}
	available := make(map[string]struct{})
	for _, def := range m.tools.Definitions() {
		name := strings.TrimSpace(def.Name)
		if name != "" {
			available[name] = struct{}{}
		}
	}
	for _, skill := range skills {
		for _, declared := range skill.Tools {
			declared = strings.TrimSpace(declared)
			if declared == "" {
				continue
			}
			if _, ok := available[declared]; !ok {
				return fmt.Errorf(
					"delegated skill %q declares unavailable tool %q",
					skill.Name,
					declared,
				)
			}
		}
	}
	return nil
}

// renderDelegatedSkills renders one or more delegated skill bodies into a
// single <delegated_skills> system prompt block. Returns ok=false when there
// are no skills to inject (preserving the pre-skill message layout).
func renderDelegatedSkills(skills []Skill) (string, bool) {
	if len(skills) == 0 {
		return "", false
	}
	parts := make([]string, 0, len(skills))
	for _, skill := range skills {
		attrs := map[string]string{
			"name": skill.Name,
		}
		if strings.TrimSpace(skill.Source) != "" {
			attrs["source"] = skill.Source
		}
		if path := strings.TrimSpace(skill.SkillFilePath); path != "" {
			attrs["path"] = path
		}
		if len(skill.Tools) > 0 {
			attrs["tools"] = strings.Join(skill.Tools, ",")
		}
		body := strings.TrimSpace(skill.Body)
		if body == "" {
			body = strings.TrimSpace(skill.Description)
		}
		rendered := agent.RenderPromptElement("skill", attrs, body)
		if rendered == "" {
			continue
		}
		parts = append(parts, rendered)
	}
	if len(parts) == 0 {
		return "", false
	}
	return agent.RenderPromptElement(
		"delegated_skills",
		nil,
		strings.Join(parts, "\n\n"),
	), true
}

type workspaceOnlyPolicy struct{}

// memoryRootPattern matches /memory only as a leading path component (the
// persistent memory filesystem root), never as a trailing segment of another
// path. This avoids false positives on legitimate workspace source paths such
// as /workspace/systems/agent/internal/memory/sanitize.go, where "/memory" is
// preceded by "internal".
var memoryRootPattern = regexp.MustCompile(`(?:^|[\s"'])(/memory(?:/[^\s"']*)?)(?:[\s"']|$)`)

// CheckToolCall blocks delegated sub-agents from touching the persistent
// /memory filesystem root. /memory is the runtime memory root, not the Go
// package internal/memory; workspace source lives under /workspace/...
func (workspaceOnlyPolicy) CheckToolCall(call agent.ToolCall) error {
	blocked := memoryRootAccess(call.Arguments)
	if blocked == "" {
		return nil
	}
	return fmt.Errorf(
		"sub-agent filesystem policy denies access to %q: "+
			"/memory is the persistent memory root, not a Go package or workspace source path; "+
			"reference repo/workspace files under /workspace/... "+
			"(e.g. /workspace/systems/agent/internal/memory)",
		blocked,
	)
}

// memoryRootAccess returns the first /memory-rooted path token in arguments,
// or "" when no /memory filesystem-root path is referenced. The pattern anchors
// /memory to path boundaries on both sides, so sibling roots such as /memoryx
// and workspace source like /workspace/systems/agent/internal/memory are not
// mistaken for the persistent memory filesystem root.
func memoryRootAccess(arguments string) string {
	if match := memoryRootPattern.FindStringSubmatch(arguments); len(match) >= 2 {
		return match[1]
	}
	return ""
}
func truncate(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
