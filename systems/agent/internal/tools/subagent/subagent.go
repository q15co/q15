// Package subagent implements delegated asynchronous agent sessions.
package subagent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/config"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	q15media "github.com/q15co/q15/systems/agent/internal/media"
)

const (
	defaultWaitSeconds = 30
	maxWaitSeconds     = 300
	defaultMaxOutput   = 20000
	maxOutput          = 200000
	defaultListLimit   = 100
	maxListLimit       = 500
)

// ModelFactory constructs model clients for delegated agents.
type ModelFactory func(config.AgentModelRuntime, q15media.Store) (agent.ModelClient, error)

// Manager tracks delegated sub-agent sessions.
type Manager struct {
	mu       sync.Mutex
	sessions map[string]*Session
	models   map[string]config.AgentModelRuntime
	factory  ModelFactory
	tools    agent.ToolRegistry
	media    q15media.Store
	next     int64
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
}

// Event is a buffered sub-agent progress event.
type Event struct {
	Index int    `json:"index"`
	Type  string `json:"type"`
	Text  string `json:"text,omitempty"`
	Error string `json:"error,omitempty"`
}

// NewManager constructs a sub-agent manager.
func NewManager(
	models []config.AgentModelRuntime,
	factory ModelFactory,
	tools agent.ToolRegistry,
	media q15media.Store,
) *Manager {
	m := &Manager{
		sessions: map[string]*Session{},
		models:   map[string]config.AgentModelRuntime{},
		factory:  factory,
		tools:    tools,
		media:    media,
	}
	for _, model := range models {
		ref := strings.TrimSpace(model.Ref)
		if ref != "" {
			m.models[ref] = model
		}
	}
	return m
}

// Start creates and runs a delegated sub-agent session.
func (m *Manager) Start(
	ctx context.Context,
	modelRef, task, extraContext string,
	allowedTools []string,
	maxTurns int,
) (*Session, error) {
	if m == nil {
		return nil, fmt.Errorf("subagent manager is not configured")
	}
	modelCfg, ok := m.models[strings.TrimSpace(modelRef)]
	if !ok {
		return nil, fmt.Errorf("unknown subagent model %q", modelRef)
	}
	if strings.TrimSpace(task) == "" {
		return nil, fmt.Errorf("task is required")
	}
	client, err := m.factory(modelCfg, m.media)
	if err != nil {
		return nil, fmt.Errorf("configure subagent model %q: %w", modelRef, err)
	}
	registry := agent.FilterToolRegistry(m.tools, allowedTools)
	engine := agent.NewEngine(client, registry, []string{modelCfg.Ref})
	engine.SetMaxTurns(maxTurns)
	msg := strings.TrimSpace(task)
	if strings.TrimSpace(extraContext) != "" {
		msg += "\n\nContext:\n" + strings.TrimSpace(extraContext)
	}
	messages := []conversation.Message{
		conversation.SystemMessage(
			"You are a delegated sub-agent. Work only on the provided task. Do not assume access to the parent conversation or private memory.",
		),
		conversation.UserMessage(msg),
	}
	childCtx, cancel := context.WithCancel(ctx)
	m.mu.Lock()
	m.next++
	id := fmt.Sprintf("subagent-%d", m.next)
	s := &Session{
		ID:        id,
		Model:     modelCfg.Ref,
		Task:      task,
		Status:    "running",
		CreatedAt: time.Now(),
		messages:  messages,
		cancel:    cancel,
		done:      make(chan struct{}),
	}
	m.sessions[id] = s
	m.mu.Unlock()
	go m.run(childCtx, s, engine)
	return s, nil
}

func (m *Manager) run(
	ctx context.Context,
	s *Session,
	engine *agent.Engine,
) {
	defer close(s.done)
	observer := agent.RunObserverFunc(func(_ context.Context, ev agent.RunEvent) {
		text := ev.FinalText
		if text == "" {
			text = ev.ToolOutput
		}
		if text == "" && ev.ToolCall.Name != "" {
			text = ev.ToolCall.Name
		}
		s.addEvent(string(ev.Type), text, ev.Err)
	})

	for {
		result, err := engine.Run(
			ctx,
			agent.EngineRequest{
				Messages:       s.snapshotMessages(),
				UseTools:       true,
				ToolCallPolicy: workspaceOnlyPolicy{},
				Observer:       observer,
			},
		)
		s.mu.Lock()
		if s.Status == "killed" {
			s.mu.Unlock()
			return
		}
		if err != nil {
			s.Status = "failed"
			s.err = err
			s.addEventLocked("failed", "", err)
			s.mu.Unlock()
			return
		}
		s.result = &result
		s.messages = append(s.messages, result.Messages...)
		if len(s.pending) == 0 {
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

func (s *Session) addEvent(typ, text string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.addEventLocked(typ, text, err)
}
func (s *Session) addEventLocked(typ, text string, err error) {
	e := Event{Index: len(s.events), Type: typ, Text: truncate(text, defaultMaxOutput)}
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
		if state == "" || state == "all" || s.Status == state {
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
	var b strings.Builder
	fmt.Fprintf(&b, "Session-ID: %s\nStatus: %s\n", s.ID, s.Status)
	if s.err != nil {
		fmt.Fprintf(&b, "Error: %v\n", s.err)
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
		if b.Len()+len(line) > maxChars {
			b.WriteString("Output-Truncated: true\n")
			break
		}
		b.WriteString(line)
	}
	fmt.Fprintf(&b, "Next-Event-Index: %d", len(s.events))
	return b.String()
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
				"wait_seconds": map[string]any{
					"type":    "integer",
					"minimum": 0,
					"maximum": maxWaitSeconds,
					"default": defaultWaitSeconds,
				},
				"max_turns": map[string]any{"type": "integer", "minimum": 1},
			},
			"required":             []string{"model", "task"},
			"additionalProperties": false,
		},
	}
}

// Run starts a delegated sub-agent session.
func (t *Spawn) Run(ctx context.Context, args string) (string, error) {
	var a struct {
		Model, Task, Context string
		Tools                []string `json:"tools"`
		Wait                 *int     `json:"wait_seconds"`
		MaxTurns             int      `json:"max_turns"`
	}
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "", fmt.Errorf("invalid arguments JSON: %w", err)
	}
	s, err := t.manager.Start(ctx, a.Model, a.Task, a.Context, a.Tools, a.MaxTurns)
	if err != nil {
		return "", err
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
	if wait > 0 {
		select {
		case <-s.done:
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
	return fmt.Sprintf("Session-ID: %s\nStatus: %s\nMessage-Appended: true", s.ID, s.Status), nil
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
					"type":    "string",
					"enum":    []string{"all", "running", "completed", "failed", "killed"},
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
		fmt.Fprintf(
			&b,
			"Session-ID: %s\nModel: %s\nStatus: %s\nTask: %s\nCreated-At: %s\n\n",
			s.ID,
			s.Model,
			s.Status,
			truncate(s.Task, 200),
			s.CreatedAt.Format(time.RFC3339),
		)
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
	return fmt.Sprintf("Session-ID: %s\nStatus: %s", s.ID, s.Status), nil
}

type workspaceOnlyPolicy struct{}

func (workspaceOnlyPolicy) CheckToolCall(call agent.ToolCall) error {
	if strings.Contains(call.Arguments, "/memory") {
		return fmt.Errorf("sub-agent filesystem policy denies /memory access")
	}
	return nil
}
func truncate(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
