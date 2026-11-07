package subagent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/history"
	q15media "github.com/q15co/q15/systems/agent/internal/media"
	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
)

type recordingModel struct {
	mu       sync.Mutex
	calls    [][]conversation.Message
	tools    [][]agent.ToolDefinition
	models   []string
	complete func(
		context.Context,
		string,
		[]conversation.Message,
		[]agent.ToolDefinition,
		int,
	) (agent.ModelClientResult, error)
}

func (m *recordingModel) Complete(
	ctx context.Context,
	model string,
	messages []conversation.Message,
	tools []agent.ToolDefinition,
) (agent.ModelClientResult, error) {
	m.mu.Lock()
	m.calls = append(m.calls, conversation.CloneMessages(messages))
	m.tools = append(m.tools, append([]agent.ToolDefinition(nil), tools...))
	m.models = append(m.models, model)
	call := len(m.calls)
	m.mu.Unlock()

	if m.complete != nil {
		return m.complete(ctx, model, messages, tools, call)
	}
	return assistantResult("done"), nil
}

func (m *recordingModel) snapshotCalls() [][]conversation.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([][]conversation.Message(nil), m.calls...)
}

func (m *recordingModel) snapshotTools() [][]agent.ToolDefinition {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([][]agent.ToolDefinition(nil), m.tools...)
}

func (m *recordingModel) snapshotModels() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.models...)
}

type noopTool struct {
	name string
}

func (t noopTool) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{Name: t.name}
}

func (noopTool) Run(context.Context, string) (string, error) {
	return "ok", nil
}

func TestManagerStartDefaultsToNoTools(t *testing.T) {
	model := &recordingModel{}
	manager := newTestManager(t, model)

	session, err := manager.Start(StartRequest{
		ModelRef: "child",
		Task:     "summarize this",
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitDone(t, session)

	calls := model.snapshotCalls()
	if len(calls) != 1 {
		t.Fatalf("model calls = %d, want 1", len(calls))
	}
	toolSets := model.snapshotTools()
	if len(toolSets) != 1 || len(toolSets[0]) != 0 {
		t.Fatalf("tools exposed to sub-agent = %#v, want none", toolSets)
	}
	if got := session.Read(0, defaultMaxOutput); !strings.Contains(got, "completed: done") {
		t.Fatalf("session read = %q, want completed result", got)
	}
}

func TestMediaAdaptiveClientDocumentsSubagentMediaBehavior(t *testing.T) {
	store := newSubagentTestMediaStore(t)
	imageRef := storeSubagentTestMedia(t, store, "image.jpg", []byte{
		0xff, 0xd8, 0xff, 0xdb, 0x00, 0x43, 0x00,
		0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07,
		0xff, 0xd9,
	}, q15media.Meta{ContentType: "image/jpeg", Source: "telegram"})
	audioRef := storeSubagentTestMedia(t, store, "speech.mp3", []byte(
		"ID3\x03\x00\x00\x00\x00\x00\x00q15 mp3 fixture",
	), q15media.Meta{ContentType: "audio/mpeg", Source: "telegram"})

	messages := []conversation.Message{conversation.UserMessageParts(
		conversation.Text("inspect this media", ""),
		conversation.Image(imageRef, ""),
		conversation.Audio(audioRef),
	)}

	textOnlyModel := &recordingModel{}
	textOnlyClient := &mediaAdaptiveClient{
		inner:         textOnlyModel,
		store:         store,
		providerModel: "child",
	}
	if _, err := textOnlyClient.Complete(context.Background(), "child", messages, nil); err != nil {
		t.Fatalf("Complete(text-only) error = %v", err)
	}
	textOnly := textOnlyModel.snapshotCalls()[0][0]
	if subagentTestHasMediaKind(textOnly, conversation.MediaKindImage) ||
		subagentTestHasMediaKind(textOnly, conversation.MediaKindAudio) {
		t.Fatalf("text-only subagent received inline media: %#v", textOnly.Parts)
	}
	if !subagentTestTextContains(textOnly, "[Media: image]") ||
		!subagentTestTextContains(textOnly, "[Media: audio]") {
		t.Fatalf("text-only subagent parts = %#v, want image and audio hints", textOnly.Parts)
	}

	visionModel := &recordingModel{}
	visionClient := &mediaAdaptiveClient{
		inner:         visionModel,
		support:       q15media.Support{conversation.MediaKindImage: true},
		store:         store,
		providerModel: "child",
	}
	if _, err := visionClient.Complete(context.Background(), "child", messages, nil); err != nil {
		t.Fatalf("Complete(vision) error = %v", err)
	}
	vision := visionModel.snapshotCalls()[0][0]
	if !subagentTestHasMediaKind(vision, conversation.MediaKindImage) {
		t.Fatalf("vision subagent parts = %#v, want image retained", vision.Parts)
	}
	if subagentTestHasMediaKind(vision, conversation.MediaKindAudio) ||
		!subagentTestTextContains(vision, "[Media: audio]") {
		t.Fatalf("vision subagent parts = %#v, want audio downgraded", vision.Parts)
	}

	if messages[0].Parts[1].Type != conversation.MediaPartType ||
		messages[0].Parts[1].MediaKind != conversation.MediaKindImage ||
		messages[0].Parts[2].Type != conversation.MediaPartType ||
		messages[0].Parts[2].MediaKind != conversation.MediaKindAudio {
		t.Fatalf("canonical subagent transcript mutated: %#v", messages[0].Parts)
	}
}

// TestMediaAdaptiveClientPassesBoundProviderModel proves the wrapper sends the
// bound provider-facing model id (full tag) to the inner provider client
// rather than the engine's agent-facing ref. This is the direct unit-level
// regression for #127: a tagged Ollama model like "gemma3:4b" must reach the
// provider API intact.
func TestMediaAdaptiveClientPassesBoundProviderModel(t *testing.T) {
	model := &recordingModel{}
	client := &mediaAdaptiveClient{
		inner:         model,
		providerModel: "gemma3:4b",
	}
	if _, err := client.Complete(
		context.Background(),
		"gemma3-4b", // engine's agent-facing ref — must NOT reach the provider
		[]conversation.Message{conversation.UserMessage("hello")},
		nil,
	); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	models := model.snapshotModels()
	if len(models) != 1 {
		t.Fatalf("model calls = %d, want 1", len(models))
	}
	if got := models[0]; got != "gemma3:4b" {
		t.Fatalf("inner client model = %q, want %q (full tagged provider model)", got, "gemma3:4b")
	}
}

// TestMediaAdaptiveClientFallsBackToEngineModelWhenUnbound guards the
// defensive fallback: when providerModel is not set (direct wrapper
// construction without binding), the incoming engine model is forwarded.
func TestMediaAdaptiveClientFallsBackToEngineModelWhenUnbound(t *testing.T) {
	model := &recordingModel{}
	client := &mediaAdaptiveClient{inner: model}
	if _, err := client.Complete(
		context.Background(),
		"plain-model",
		[]conversation.Message{conversation.UserMessage("hello")},
		nil,
	); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	models := model.snapshotModels()
	if len(models) != 1 || models[0] != "plain-model" {
		t.Fatalf("inner client models = %#v, want [plain-model]", models)
	}
}

// TestManagerStartSendsTaggedProviderModelToClient is the end-to-end
// regression for #127. A sub-agent spawned with the agent-facing ref
// "gemma3-4b" (whose ProviderModel is the tagged "gemma3:4b") must send
// "gemma3:4b" to the provider client on every completion call. The session
// model stays the agent-facing ref.
func TestManagerStartSendsTaggedProviderModelToClient(t *testing.T) {
	model := &recordingModel{}
	manager := newTestManagerWithModels(t, model, []modelcatalog.Model{
		{
			ProviderModel: "gemma3:4b",
			Capabilities:  modelcatalog.Capabilities{Chat: true, Text: true},
		},
	})

	// deriveRef("gemma3:4b") == "gemma3-4b", preserving the version tag.
	session, err := manager.Start(StartRequest{
		ModelRef: "gemma3-4b", // agent-facing ref
		Task:     "summarize this",
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitDone(t, session)

	models := model.snapshotModels()
	if len(models) != 1 {
		t.Fatalf("model calls = %d, want 1", len(models))
	}
	if got := models[0]; got != "gemma3:4b" {
		t.Fatalf(
			"provider client received model %q, want %q (full tagged provider model, not the stripped ref)",
			got,
			"gemma3:4b",
		)
	}
	// The session model remains the agent-facing ref, not the provider tag.
	if session.Model != "gemma3-4b" {
		t.Fatalf("session model = %q, want agent-facing ref %q", session.Model, "gemma3-4b")
	}
}

func TestSessionWriteQueuesFollowUpForRunningSubAgent(t *testing.T) {
	firstCallStarted := make(chan struct{})
	releaseFirstCall := make(chan struct{})
	model := &recordingModel{
		complete: func(
			ctx context.Context,
			_ string,
			_ []conversation.Message,
			_ []agent.ToolDefinition,
			call int,
		) (agent.ModelClientResult, error) {
			if call == 1 {
				close(firstCallStarted)
				select {
				case <-releaseFirstCall:
				case <-ctx.Done():
					return agent.ModelClientResult{}, ctx.Err()
				}
				return assistantResult("first answer"), nil
			}
			return assistantResult("second answer"), nil
		},
	}
	manager := newTestManager(t, model)

	session, err := manager.Start(StartRequest{
		ModelRef: "child",
		Task:     "initial task",
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	select {
	case <-firstCallStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first model call did not start")
	}

	if err := session.Write("follow-up from parent"); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	close(releaseFirstCall)
	waitDone(t, session)

	calls := model.snapshotCalls()
	if len(calls) != 2 {
		t.Fatalf("model calls = %d, want 2", len(calls))
	}
	if !messagesContainText(calls[1], "follow-up from parent") {
		t.Fatalf("second model call did not include parent follow-up: %#v", calls[1])
	}
	if got := session.Read(0, defaultMaxOutput); !strings.Contains(
		got,
		"completed: second answer",
	) {
		t.Fatalf("session read = %q, want final follow-up result", got)
	}
}

func TestWriteToolRunAppendsMessageToRunningSession(t *testing.T) {
	firstCallStarted := make(chan struct{})
	releaseFirstCall := make(chan struct{})
	model := &recordingModel{
		complete: func(
			ctx context.Context,
			_ string,
			_ []conversation.Message,
			_ []agent.ToolDefinition,
			call int,
		) (agent.ModelClientResult, error) {
			if call == 1 {
				close(firstCallStarted)
				select {
				case <-releaseFirstCall:
				case <-ctx.Done():
					return agent.ModelClientResult{}, ctx.Err()
				}
				return assistantResult("first answer"), nil
			}
			return assistantResult("second answer"), nil
		},
	}
	manager := newTestManager(t, model)

	session, err := manager.Start(StartRequest{
		ModelRef: "child",
		Task:     "initial task",
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	select {
	case <-firstCallStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first model call did not start")
	}

	args := `{"session_id":"` + session.ID + `","message":"hi"}`
	out, err := NewWrite(manager).Run(context.Background(), args)
	if err != nil {
		t.Fatalf("Write.Run() error = %v", err)
	}
	if !strings.Contains(out, "Message-Appended: true") {
		t.Fatalf("Write.Run() output = %q, want Message-Appended: true", out)
	}

	close(releaseFirstCall)
	waitDone(t, session)

	calls := model.snapshotCalls()
	if len(calls) != 2 {
		t.Fatalf("model calls = %d, want 2", len(calls))
	}
	if !messagesContainText(calls[1], "hi") {
		t.Fatalf("second model call did not include appended message: %#v", calls[1])
	}
}

func TestWriteToolRunRejectsUnknownSession(t *testing.T) {
	manager := newTestManager(t, &recordingModel{})
	_, err := NewWrite(manager).Run(
		context.Background(),
		`{"session_id":"subagent-bogus","message":"hi"}`,
	)
	if err == nil {
		t.Fatal("Write.Run() error = nil, want unknown session error")
	}
	if !strings.Contains(err.Error(), `unknown subagent session "subagent-bogus"`) {
		t.Fatalf("Write.Run() error = %v, want error naming the bogus session id", err)
	}
}

func TestWriteToolRunRejectsEmptyMessage(t *testing.T) {
	firstCallStarted := make(chan struct{})
	releaseFirstCall := make(chan struct{})
	model := &recordingModel{
		complete: func(
			ctx context.Context,
			_ string,
			_ []conversation.Message,
			_ []agent.ToolDefinition,
			call int,
		) (agent.ModelClientResult, error) {
			if call == 1 {
				close(firstCallStarted)
				select {
				case <-releaseFirstCall:
				case <-ctx.Done():
					return agent.ModelClientResult{}, ctx.Err()
				}
				return assistantResult("first answer"), nil
			}
			return assistantResult("second answer"), nil
		},
	}
	manager := newTestManager(t, model)

	session, err := manager.Start(StartRequest{
		ModelRef: "child",
		Task:     "initial task",
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	select {
	case <-firstCallStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first model call did not start")
	}

	// Also guards the {"data":"..."} shape: unknown fields are silently
	// ignored by encoding/json, so Message stays empty.
	args := `{"session_id":"` + session.ID + `","message":""}`
	_, err = NewWrite(manager).Run(context.Background(), args)
	if err == nil {
		t.Fatal("Write.Run() error = nil, want message is required error")
	}
	if !strings.Contains(err.Error(), "message is required") {
		t.Fatalf("Write.Run() error = %v, want message is required", err)
	}

	close(releaseFirstCall)
	waitDone(t, session)
}

func TestWorkspaceOnlyPolicyDeniesMemory(t *testing.T) {
	err := workspaceOnlyPolicy{}.CheckToolCall(agent.ToolCall{
		Name:      "read_file",
		Arguments: `{"path":"/memory/core/AGENT.md"}`,
	})
	if err == nil {
		t.Fatal("CheckToolCall() error = nil, want /memory denial")
	}
	msg := err.Error()
	for _, want := range []string{
		"/memory/core/AGENT.md",
		"/workspace/",
		"persistent memory root",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("CheckToolCall() error = %q, want %q in denial", msg, want)
		}
	}
}

func TestWorkspaceOnlyPolicyAllowsWorkspaceInternalMemoryPath(t *testing.T) {
	// Regression for #117: "/memory" as a trailing segment of a workspace
	// source path (the Go package internal/memory) must NOT be denied.
	err := workspaceOnlyPolicy{}.CheckToolCall(agent.ToolCall{
		Name:      "edit_file",
		Arguments: `{"path":"/workspace/systems/agent/internal/memory/sanitize.go"}`,
	})
	if err != nil {
		t.Fatalf("CheckToolCall() error = %v, want nil for workspace source path", err)
	}
}

func TestWorkspaceOnlyPolicyDeniesMemoryRootInExecCommand(t *testing.T) {
	err := workspaceOnlyPolicy{}.CheckToolCall(agent.ToolCall{
		Name:      "exec",
		Arguments: `{"command":"ls /memory"}`,
	})
	if err == nil || !strings.Contains(err.Error(), "/memory") {
		t.Fatalf("CheckToolCall() error = %v, want /memory denial for exec command", err)
	}
}

// TestWorkspaceOnlyPolicyMemoryRootBoundaries locks in the leading-and-trailing
// path-boundary matching: sibling roots such as /memoryx and workspace source
// under /workspace/.../internal/memory or /workspace/memory/... stay allowed,
// while the persistent /memory root (with or without trailing slash) is denied.
func TestWorkspaceOnlyPolicyMemoryRootBoundaries(t *testing.T) {
	tests := []struct {
		name      string
		arguments string
		deny      bool
	}{
		{"bare root denied", `{"path":"/memory"}`, true},
		{"trailing slash denied", `{"path":"/memory/"}`, true},
		{"nested memory path denied", `{"path":"/memory/core/AGENT.md"}`, true},
		{"memory in exec command denied", `{"command":"cd /memory && pwd"}`, true},
		{"sibling memoryx allowed", `{"path":"/memoryx/foo"}`, false},
		{"sibling memoryback allowed", `{"path":"/memoryback"}`, false},
		{"skills path allowed (memory-only scope)", `{"path":"/skills/AGENT.md"}`, false},
		{"workspace memory subdir allowed", `{"path":"/workspace/memory/foo"}`, false},
		{
			"workspace internal memory package allowed",
			`{"path":"/workspace/systems/agent/internal/memory/sanitize.go"}`,
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := workspaceOnlyPolicy{}.CheckToolCall(agent.ToolCall{
				Name:      "read_file",
				Arguments: tt.arguments,
			})
			if tt.deny && err == nil {
				t.Fatalf("CheckToolCall() error = nil, want denial for %s", tt.arguments)
			}
			if !tt.deny && err != nil {
				t.Fatalf("CheckToolCall() error = %v, want nil for %s", err, tt.arguments)
			}
		})
	}
}

type fakeCatalog struct {
	models map[string][]modelcatalog.Model
}

func (m *fakeCatalog) Discover(
	_ context.Context,
	p modelcatalog.Provider,
) ([]modelcatalog.Model, error) {
	return m.models[p.Name], nil
}

func newTestManager(t *testing.T, model agent.ModelClient) *Manager {
	t.Helper()
	return newTestManagerWithModels(t, model, []modelcatalog.Model{
		{ProviderModel: "child", Capabilities: modelcatalog.Capabilities{Chat: true, Text: true}},
	})
}

// newTestManagerWithModels builds a sub-agent manager backed by a registry
// seeded with the given provider models. It allows tests to register tagged
// models (e.g. ProviderModel: "gemma3:4b") whose agent-facing ref is derived
// by modelcatalog.deriveRef at refresh time. The manager's base context is
// context.Background(); tests that drive session lifetime set manager.baseCtx
// after construction, the same way budget_test.go swaps manager.tools.
func newTestManagerWithModels(
	t *testing.T,
	model agent.ModelClient,
	providerModels []modelcatalog.Model,
) *Manager {
	t.Helper()
	registry, err := agent.NewToolRegistry(noopTool{name: "read_file"})
	if err != nil {
		t.Fatalf("NewToolRegistry() error = %v", err)
	}
	cat := &fakeCatalog{models: map[string][]modelcatalog.Model{"p": providerModels}}
	reg := modelcatalog.New(
		[]modelcatalog.Provider{{Name: "p", Type: "ollama"}},
		cat,
		time.Hour,
		time.Second,
	)
	reg.Refresh(context.Background())
	return NewManager(
		context.Background(),
		reg,
		func(modelcatalog.Model, q15media.Store) (agent.ModelClient, error) {
			return model, nil
		},
		registry,
		nil,
		nil,
		nil,
		newTestContextFitter(t),
		nil,
	)
}

func newSubagentTestMediaStore(t *testing.T) *q15media.FileStore {
	t.Helper()
	store, err := q15media.NewFileStore(filepath.Join(t.TempDir(), "media"))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}
	return store
}

func storeSubagentTestMedia(
	t *testing.T,
	store *q15media.FileStore,
	filename string,
	content []byte,
	meta q15media.Meta,
) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), filename)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	meta.Filename = filename
	ref, err := store.Store(path, meta, "test")
	if err != nil {
		t.Fatalf("Store() error = %v", err)
	}
	return ref
}

func subagentTestHasMediaKind(message conversation.Message, kind conversation.MediaKind) bool {
	for _, part := range message.Parts {
		if part.IsMedia(kind) {
			return true
		}
	}
	return false
}

func subagentTestTextContains(message conversation.Message, text string) bool {
	for _, part := range message.Parts {
		if part.Type == conversation.TextPartType && strings.Contains(part.Text, text) {
			return true
		}
	}
	return false
}

func waitDone(t *testing.T, session *Session) {
	t.Helper()
	timeout := 30 * time.Second
	if deadline, ok := t.Deadline(); ok && time.Until(deadline) < timeout {
		timeout = time.Until(deadline)
	}
	select {
	case <-session.done:
	case <-time.After(timeout):
		t.Fatalf("session %s did not finish", session.ID)
	}
}

func assistantResult(text string) agent.ModelClientResult {
	return agent.ModelClientResult{
		Messages: []conversation.Message{
			conversation.AssistantMessage(
				conversation.Text(text, conversation.TextDispositionFinal),
			),
		},
	}
}

func messagesContainText(messages []conversation.Message, want string) bool {
	for _, msg := range messages {
		for _, part := range msg.Parts {
			if strings.Contains(part.Text, want) {
				return true
			}
		}
	}
	return false
}

// messageText extracts the concatenated text of all text parts in a message.
func messageText(msg conversation.Message) string {
	var b strings.Builder
	for _, part := range msg.Parts {
		if part.Type == conversation.TextPartType {
			b.WriteString(part.Text)
		}
	}
	return b.String()
}

// fakeSkillResolver implements SkillResolver for tests.
type fakeSkillResolver struct {
	skills map[string]Skill
	err    error
}

func (r *fakeSkillResolver) ResolveSkill(ref string) (Skill, error) {
	if r.err != nil {
		return Skill{}, r.err
	}
	if skill, ok := r.skills[ref]; ok {
		return skill, nil
	}
	return Skill{}, fmt.Errorf("unknown skill %q", ref)
}

// newTestManagerWithResolver builds a manager with the given tools and
// skill resolver.
func newTestManagerWithResolver(
	t *testing.T,
	model agent.ModelClient,
	tools []agent.Tool,
	resolver SkillResolver,
) *Manager {
	t.Helper()
	registry, err := agent.NewToolRegistry(tools...)
	if err != nil {
		t.Fatalf("NewToolRegistry() error = %v", err)
	}
	cat := &fakeCatalog{models: map[string][]modelcatalog.Model{
		"p": {
			{
				ProviderModel: "child",
				Capabilities:  modelcatalog.Capabilities{Chat: true, Text: true},
			},
		},
	}}
	reg := modelcatalog.New(
		[]modelcatalog.Provider{{Name: "p", Type: "ollama"}},
		cat,
		time.Hour,
		time.Second,
	)
	reg.Refresh(context.Background())
	return NewManager(
		context.Background(),
		reg,
		func(modelcatalog.Model, q15media.Store) (agent.ModelClient, error) {
			return model, nil
		},
		registry,
		nil,
		resolver,
		nil,
		newTestContextFitter(t),
		nil,
	)
}

func newTestContextFitter(t *testing.T) agent.RequestContextFitter {
	t.Helper()
	fitter, err := history.NewIsolatedContextFitter(history.ContextSettings{})
	if err != nil {
		t.Fatal(err)
	}
	return fitter
}

func TestSpawnRunInjectsSkillBodyAndUnionsTools(t *testing.T) {
	t.Parallel()

	model := &recordingModel{}
	resolver := &fakeSkillResolver{skills: map[string]Skill{
		"demo-skill": {
			Name:          "demo-skill",
			Description:   "Demo skill.",
			Source:        "shared",
			SkillFilePath: "/skills/demo-skill/SKILL.md",
			Tools:         []string{"exec", "read_file"},
			Body:          "# Demo\nFollow this workflow.",
		},
	}}
	manager := newTestManagerWithResolver(
		t,
		model,
		[]agent.Tool{noopTool{name: "read_file"}, noopTool{name: "exec"}},
		resolver,
	)

	args := `{"model":"child","task":"do the thing","tools":["read_file"],"skills":["demo-skill"],"wait_seconds":1}`
	out, err := NewSpawn(manager).Run(context.Background(), args)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	_ = out // session may still be running or done; we care about the model call

	calls := model.snapshotCalls()
	if len(calls) != 1 {
		t.Fatalf("model calls = %d, want 1", len(calls))
	}
	msg := calls[0]
	if got, want := len(msg), 5; got != want {
		t.Fatalf("messages = %d, want %d", got, want)
	}
	// The delegated skills message must contain the skill body and metadata.
	delegated := messageText(msg[1])
	for _, want := range []string{
		"<delegated_skills>",
		"<skill",
		`name="demo-skill"`,
		`path="/skills/demo-skill/SKILL.md"`,
		`tools="exec,read_file"`,
		"# Demo",
		"Follow this workflow.",
		"</delegated_skills>",
	} {
		if !strings.Contains(delegated, want) {
			t.Fatalf("delegated skills message missing %q:\n%s", want, delegated)
		}
	}
	if msg[2].Role != conversation.UserRole {
		t.Fatalf("task message role = %q, want %q", msg[2].Role, conversation.UserRole)
	}

	// Tools exposed: explicit tools first (read_file), then skill-only (exec).
	toolSets := model.snapshotTools()
	if len(toolSets) != 1 {
		t.Fatalf("tool sets = %d, want 1", len(toolSets))
	}
	exposed := toolSets[0]
	if len(exposed) != 2 {
		t.Fatalf("exposed tools = %#v, want 2", exposed)
	}
	if exposed[0].Name != "read_file" || exposed[1].Name != "exec" {
		t.Fatalf("exposed tool order = [%s, %s], want [read_file, exec]",
			exposed[0].Name, exposed[1].Name)
	}
}

func TestSpawnRunNoSkillsPreservesNoToolDefault(t *testing.T) {
	t.Parallel()

	model := &recordingModel{}
	manager := newTestManager(t, model)

	args := `{"model":"child","task":"do the thing","wait_seconds":1}`
	out, err := NewSpawn(manager).Run(context.Background(), args)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	_ = out

	calls := model.snapshotCalls()
	if len(calls) != 1 {
		t.Fatalf("model calls = %d, want 1", len(calls))
	}
	msg := calls[0]
	if got, want := len(msg), 4; got != want {
		t.Fatalf("messages = %d, want %d (no skill injection expected)", got, want)
	}
	base := messageText(msg[0])
	if want := "You are a delegated sub-agent. Work only on the provided task. Do not assume access to the parent conversation or private memory."; base != want {
		t.Fatalf("base system message = %q, want %q", base, want)
	}

	toolSets := model.snapshotTools()
	if len(toolSets) != 1 || len(toolSets[0]) != 0 {
		t.Fatalf("tools exposed = %#v, want none (no-tools default)", toolSets)
	}
}

func TestSpawnRunRejectsUnknownSkill(t *testing.T) {
	t.Parallel()

	model := &recordingModel{}
	resolver := &fakeSkillResolver{err: fmt.Errorf("skill not found")}
	manager := newTestManagerWithResolver(
		t,
		model,
		[]agent.Tool{noopTool{name: "read_file"}},
		resolver,
	)

	args := `{"model":"child","task":"do the thing","skills":["nope"],"wait_seconds":1}`
	_, err := NewSpawn(manager).Run(context.Background(), args)
	if err == nil {
		t.Fatal("Run() error = nil, want error for unknown skill")
	}
	if !strings.Contains(err.Error(), "resolve delegated skill") {
		t.Fatalf("Run() error = %v, want error containing 'resolve delegated skill'", err)
	}
	if len(model.snapshotCalls()) != 0 {
		t.Fatalf("model should not be called when skill resolution fails")
	}
}

func TestSpawnRunRejectsUnavailableDeclaredSkillTool(t *testing.T) {
	t.Parallel()

	model := &recordingModel{}
	resolver := &fakeSkillResolver{skills: map[string]Skill{
		"demo": {
			Name:  "demo",
			Tools: []string{"read_file", "web_search"},
			Body:  "# Demo",
		},
	}}
	// Registry only has read_file, not web_search.
	manager := newTestManagerWithResolver(
		t,
		model,
		[]agent.Tool{noopTool{name: "read_file"}},
		resolver,
	)

	args := `{"model":"child","task":"do the thing","skills":["demo"],"wait_seconds":1}`
	_, err := NewSpawn(manager).Run(context.Background(), args)
	if err == nil {
		t.Fatal("Run() error = nil, want error for unavailable declared tool")
	}
	if !strings.Contains(err.Error(), "declares unavailable tool") {
		t.Fatalf("Run() error = %v, want error about unavailable tool", err)
	}
	if !strings.Contains(err.Error(), "web_search") {
		t.Fatalf("Run() error = %v, want error naming web_search", err)
	}
	if len(model.snapshotCalls()) != 0 {
		t.Fatalf("model should not be called when tool validation fails")
	}
}

func TestSpawnRunRejectsSkillsWithoutResolver(t *testing.T) {
	t.Parallel()

	model := &recordingModel{}
	manager := newTestManager(t, model)

	args := `{"model":"child","task":"do the thing","skills":["some-skill"],"wait_seconds":1}`
	_, err := NewSpawn(manager).Run(context.Background(), args)
	if err == nil {
		t.Fatal("Run() error = nil, want error when no resolver is configured")
	}
	if !strings.Contains(err.Error(), "skill resolver") {
		t.Fatalf("Run() error = %v, want error about missing skill resolver", err)
	}
}

func TestUnionAllowedToolsOrderStableAndDeduped(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		explicit []string
		skills   []Skill
		want     []string
	}{
		{
			name:     "both empty returns nil",
			explicit: nil,
			skills:   nil,
			want:     nil,
		},
		{
			name:     "explicit only",
			explicit: []string{"exec", "read_file"},
			skills:   nil,
			want:     []string{"exec", "read_file"},
		},
		{
			name:     "skill only",
			explicit: nil,
			skills:   []Skill{{Tools: []string{"read_file", "exec"}}},
			want:     []string{"read_file", "exec"},
		},
		{
			name:     "explicit first then skill",
			explicit: []string{"web_fetch"},
			skills:   []Skill{{Tools: []string{"exec", "read_file"}}},
			want:     []string{"web_fetch", "exec", "read_file"},
		},
		{
			name:     "dedup across explicit and skill",
			explicit: []string{"exec"},
			skills:   []Skill{{Tools: []string{"exec", "read_file"}}},
			want:     []string{"exec", "read_file"},
		},
		{
			name:     "dedup across multiple skills",
			explicit: nil,
			skills: []Skill{
				{Tools: []string{"exec"}},
				{Tools: []string{"exec", "read_file"}},
			},
			want: []string{"exec", "read_file"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := unionAllowedTools(tt.explicit, tt.skills)
			if len(got) != len(tt.want) {
				t.Fatalf("unionAllowedTools() = %#v, want %#v", got, tt.want)
			}
			for i, w := range tt.want {
				if got[i] != w {
					t.Fatalf("unionAllowedTools()[%d] = %q, want %q", i, got[i], w)
				}
			}
		})
	}
}

func TestSubagentDispatchRechecksChatCapability(t *testing.T) {
	models := map[string][]modelcatalog.Model{
		"api": {{ProviderModel: "chat", Capabilities: modelcatalog.Capabilities{Chat: true}}},
	}
	registry := modelcatalog.New(
		[]modelcatalog.Provider{{Name: "api"}},
		&fakeCatalog{models: models},
		time.Hour,
		time.Second,
	)
	registry.Refresh(context.Background())
	bound, _ := registry.Lookup("api", "chat")
	inner := &recordingModel{}
	client := &mediaAdaptiveClient{
		inner:         inner,
		registry:      registry,
		boundModel:    bound,
		providerModel: "chat",
	}
	if _, err := client.Complete(context.Background(), "chat", nil, nil); err != nil {
		t.Fatal(err)
	}
	models["api"][0].Capabilities.Chat = false
	registry.Refresh(context.Background())
	if _, err := client.Complete(context.Background(), "chat", nil, nil); err == nil {
		t.Fatal("subagent bypassed live capability guard")
	}
	if len(inner.snapshotCalls()) != 1 {
		t.Fatal("provider called after capability was removed")
	}
}

// TestSessionSurvivesParentTurnCancel is the regression for #393: a session
// spawned from the parent chat turn's context must keep running after that
// turn's context is canceled, because the session context derives from the
// manager's base context (process lifetime), not from the dispatch context.
func TestSessionSurvivesParentTurnCancel(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	model := &recordingModel{complete: func(
		ctx context.Context,
		_ string,
		_ []conversation.Message,
		_ []agent.ToolDefinition,
		_ int,
	) (agent.ModelClientResult, error) {
		close(started)
		<-release
		if err := ctx.Err(); err != nil {
			// Regressed: the session context is still a child of the dispatch
			// context and died when the parent turn ended.
			return agent.ModelClientResult{}, fmt.Errorf(
				"session died with the parent turn: %w", err,
			)
		}
		return assistantResult("survived the parent turn"), nil
	}}
	manager := newTestManager(t, model)
	parentTurn, cancelTurn := context.WithCancel(context.Background())

	out, err := NewSpawn(
		manager,
	).Run(parentTurn, `{"model":"child","task":"work","wait_seconds":0}`)
	if err != nil {
		t.Fatalf("Spawn.Run() error = %v", err)
	}
	if !strings.Contains(out, "Status: running") {
		t.Fatalf("Spawn.Run() output = %q, want a running session", out)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("model call did not start")
	}

	// The parent chat turn ends. Context cancellation is synchronous, so a
	// session context still derived from the dispatch context would already
	// be done when the model returns above.
	cancelTurn()
	close(release)

	session, ok := manager.Get(sessionID(1))
	if !ok {
		t.Fatal("Spawn.Run() did not register the session with the manager")
	}
	waitDone(t, session)
	read := session.Read(0, defaultMaxOutput)
	if session.Status != "completed" ||
		!strings.Contains(read, "completed: survived the parent turn") {
		t.Fatalf("session did not survive the parent turn ending: %s", read)
	}
}

func TestSpawnWaitStopsWhenParentTurnCanceled(t *testing.T) {
	started := make(chan context.Context, 1)
	release := make(chan struct{})
	model := &recordingModel{complete: func(
		ctx context.Context,
		_ string,
		_ []conversation.Message,
		_ []agent.ToolDefinition,
		_ int,
	) (agent.ModelClientResult, error) {
		started <- ctx
		select {
		case <-release:
			return assistantResult("finished after parent cancellation"), nil
		case <-ctx.Done():
			return agent.ModelClientResult{}, ctx.Err()
		}
	}}
	manager := newTestManager(t, model)
	base, shutdown := context.WithCancel(t.Context())
	t.Cleanup(shutdown)
	manager.baseCtx = base
	parentTurn, cancelTurn := context.WithCancel(t.Context())
	t.Cleanup(cancelTurn)
	returned := make(chan error, 1)
	go func() {
		_, err := NewSpawn(manager).Run(parentTurn,
			`{"model":"child","task":"work","wait_seconds":300}`)
		returned <- err
	}()

	var sessionCtx context.Context
	select {
	case sessionCtx = <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("model call did not start")
	}
	cancelTurn()
	select {
	case err := <-returned:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Spawn.Run() error = %v, want context canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Spawn.Run() kept waiting after the parent turn was canceled")
	}
	if err := sessionCtx.Err(); err != nil {
		t.Fatalf("session context ended with its parent turn: %v", err)
	}
	close(release)
	session, ok := manager.Get(sessionID(1))
	if !ok {
		t.Fatal("spawned session disappeared")
	}
	waitDone(t, session)
	if session.status() != "completed" {
		t.Fatalf("session did not complete: %s", session.Read(0, defaultMaxOutput))
	}
}

func TestSpawnRejectsCanceledOrInvalidRequestsBeforeStarting(t *testing.T) {
	for _, name := range []string{"canceled", "negative wait"} {
		t.Run(name, func(t *testing.T) {
			manager := newTestManager(t, &recordingModel{})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			args := `{"model":"child","task":"work","wait_seconds":0}`
			if name == "canceled" {
				cancel()
			} else {
				args = `{"model":"child","task":"work","wait_seconds":-1}`
			}
			if _, err := NewSpawn(manager).Run(ctx, args); err == nil {
				t.Fatal("Spawn.Run() succeeded")
			}
			if sessions := manager.List("all", 0); len(sessions) != 0 {
				t.Fatalf("rejected spawn started %d sessions", len(sessions))
			}
		})
	}
}

func TestFinishedSessionReleasesContext(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("failure=%t", fail), func(t *testing.T) {
			contexts := make(chan context.Context, 1)
			model := &recordingModel{complete: func(
				ctx context.Context,
				_ string,
				_ []conversation.Message,
				_ []agent.ToolDefinition,
				_ int,
			) (agent.ModelClientResult, error) {
				contexts <- ctx
				if fail {
					return agent.ModelClientResult{}, errors.New("model failed")
				}
				return assistantResult("done"), nil
			}}
			manager := newTestManager(t, model)
			session, err := manager.Start(StartRequest{ModelRef: "child", Task: "work"})
			if err != nil {
				t.Fatal(err)
			}
			waitDone(t, session)
			ctx := <-contexts
			if !errors.Is(ctx.Err(), context.Canceled) {
				t.Fatalf("finished session context error = %v, want context canceled", ctx.Err())
			}
			want := "completed"
			if fail {
				want = "failed"
			}
			if got := session.status(); got != want {
				t.Fatalf("session status = %q, want %q", got, want)
			}
		})
	}
}

// residualCancelContext opens the window between the engine's final context
// check and the manager's terminal bookkeeping: the first check after the
// model returns stays live, and subsequent checks report cancellation.
type residualCancelContext struct {
	context.Context
	modelReturned atomic.Bool
	checks        atomic.Int32
}

func (c *residualCancelContext) Err() error {
	if !c.modelReturned.Load() || c.checks.Add(1) == 1 {
		return nil
	}
	return context.Canceled
}

// TestShutdownCancelReportsFailedWithReason is #393 lifecycle requirement 1:
// a partial result returned with a nil error in the residual cancellation
// window must be failed with its cancellation reason, not completed.
func TestShutdownCancelReportsFailedWithReason(t *testing.T) {
	ctx := &residualCancelContext{Context: context.Background()}
	model := &recordingModel{complete: func(
		_ context.Context,
		_ string,
		_ []conversation.Message,
		_ []agent.ToolDefinition,
		_ int,
	) (agent.ModelClientResult, error) {
		ctx.modelReturned.Store(true)
		return assistantResult("partial answer cut off mid-flight"), nil
	}}
	manager := newTestManager(t, model)
	engine := agent.NewEngine(model, nil, []string{"child"})
	session := &Session{
		ID: "subagent-1", Status: "running", done: make(chan struct{}),
		messages: []conversation.Message{conversation.UserMessage("work")},
	}
	manager.sessions[session.ID] = session
	go manager.run(ctx, session, engine, agent.NewTurnBudget(1))
	waitDone(t, session)

	read := session.Read(0, defaultMaxOutput)
	if session.Status != "failed" {
		t.Fatalf("session.Status = %q, want failed:\n%s", session.Status, read)
	}
	if session.err == nil || !strings.Contains(session.err.Error(), "context canceled") {
		t.Fatalf("session.err = %v, want the cancellation reason", session.err)
	}
	if !strings.Contains(read, "failed:") || strings.Contains(read, "] completed") {
		t.Fatalf("read = %q, want a terminal failed event and no completed event", read)
	}
}

// TestKillStaysKilledAfterCancelVisibility guards the distinction required by
// #393: an intentional subagent_kill cancels the same session context, yet
// the session must stay killed — the cancellation-visibility check must not
// rewrite an intentional kill into a failure.
func TestKillStaysKilledAfterCancelVisibility(t *testing.T) {
	started := make(chan struct{})
	model := &recordingModel{complete: func(
		ctx context.Context,
		_ string,
		_ []conversation.Message,
		_ []agent.ToolDefinition,
		_ int,
	) (agent.ModelClientResult, error) {
		close(started)
		<-ctx.Done()
		return agent.ModelClientResult{}, ctx.Err()
	}}
	manager := newTestManager(t, model)
	session, err := manager.Start(StartRequest{ModelRef: "child", Task: "work"})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("model call did not start")
	}

	out, err := NewKill(manager).Run(context.Background(), `{"session_id":"`+session.ID+`"}`)
	if err != nil {
		t.Fatalf("Kill.Run() error = %v", err)
	}
	if !strings.Contains(out, "Status: killed") {
		t.Fatalf("Kill.Run() output = %q, want Status: killed", out)
	}
	waitDone(t, session)
	if session.Status != "killed" {
		t.Fatalf("session.Status = %q, want killed: %s",
			session.Status, session.Read(0, defaultMaxOutput))
	}
	if session.err != nil {
		t.Fatalf("session.err = %v, want nil: an intentional kill is not a failure", session.err)
	}
	read := session.Read(0, defaultMaxOutput)
	if !strings.Contains(read, "killed") || strings.Contains(read, "] failed") {
		t.Fatalf("read = %q, want killed without a failed event", read)
	}
}

// TestFinishedSessionEvictionBoundsRetention is #393 lifecycle requirement 2:
// retention of finished sessions is bounded count-wise. The oldest finished
// sessions lose their event log first while their record keeps the terminal
// state and final answer; beyond the record cap the oldest records are
// deleted entirely; and running sessions are never evicted.
func TestFinishedSessionEvictionBoundsRetention(t *testing.T) {
	manager := newTestManager(t, &recordingModel{})
	base := time.Now()
	insert := func(i int) *Session {
		s := &Session{
			ID:          fmt.Sprintf("subagent-old-%d", i),
			Model:       "child",
			Task:        "historical task",
			Status:      "completed",
			CreatedAt:   base.Add(time.Duration(i) * time.Second),
			finishedAt:  base.Add(time.Duration(i) * time.Second),
			finalAnswer: fmt.Sprintf("final answer %d", i),
			events: []Event{
				{Index: 0, Type: "model_response", Text: "working"},
				{Index: 1, Type: "completed", Text: fmt.Sprintf("final answer %d", i)},
			},
		}
		manager.sessions[s.ID] = s
		return s
	}
	// Two more finished sessions than the record cap, so eviction both drops
	// logs and deletes the oldest records entirely.
	total := maxFinishedSessionRecords + 2
	for i := 0; i < total; i++ {
		insert(i)
	}
	running := &Session{
		ID:        "subagent-running",
		Model:     "child",
		Task:      "still working",
		Status:    "running",
		CreatedAt: base.Add(time.Duration(total) * time.Second),
		events:    []Event{{Index: 0, Type: "model_response", Text: "working"}},
	}
	manager.sessions[running.ID] = running

	if err := manager.evictFinishedSessions(); err != nil {
		t.Fatal(err)
	}

	if got := len(manager.sessions); got != maxFinishedSessionRecords+1 {
		t.Fatalf("retained sessions = %d, want %d finished records plus the running one",
			got, maxFinishedSessionRecords)
	}
	for _, i := range []int{0, 1} {
		if _, ok := manager.Get(fmt.Sprintf("subagent-old-%d", i)); ok {
			t.Fatalf("finished session %d survived beyond the record cap", i)
		}
	}
	oldestRetained, ok := manager.Get("subagent-old-2")
	if !ok {
		t.Fatal("oldest finished session under the record cap was deleted")
	}
	if !oldestRetained.logEvicted || len(oldestRetained.events) != 0 {
		t.Fatalf("oldest retained session kept its event log: %#v", oldestRetained.events)
	}
	if oldestRetained.Status != "completed" || oldestRetained.finalAnswer != "final answer 2" {
		t.Fatalf("evicted session lost its terminal record: status=%q answer=%q",
			oldestRetained.Status, oldestRetained.finalAnswer)
	}
	read := oldestRetained.Read(0, defaultMaxOutput)
	for _, want := range []string{
		"Status: completed",
		"Event-Log: evicted by retention",
		"Final-Answer: final answer 2",
	} {
		if !strings.Contains(read, want) {
			t.Fatalf("evicted session read = %q, want %q", read, want)
		}
	}
	newest, ok := manager.Get(fmt.Sprintf("subagent-old-%d", total-1))
	if !ok || newest.logEvicted || len(newest.events) != 2 {
		t.Fatalf("newest finished session lost its event log: %#v", newest)
	}
	if running.logEvicted || len(running.events) != 1 {
		t.Fatal("running session was evicted")
	}
	if _, ok := manager.Get(running.ID); !ok {
		t.Fatal("running session was deleted from the manager")
	}
}

func TestFinishedSessionEvictionBreaksTimestampTies(t *testing.T) {
	manager := &Manager{sessions: make(map[string]*Session)}
	finishedAt := time.Now()
	for i := 0; i <= maxFinishedSessionLogs; i++ {
		id := fmt.Sprintf("subagent-tied-%02d", i)
		manager.sessions[id] = &Session{
			ID:         id,
			Status:     "completed",
			finishedAt: finishedAt,
			events:     []Event{{Index: 0, Type: "completed"}},
		}
	}

	// Map iteration order varies between passes. Repetition makes this test
	// discriminate the old timestamp-only comparator instead of relying on a
	// fortunate map order; the ID tie-break makes every pass deterministic.
	for attempt := 0; attempt < 32; attempt++ {
		if err := manager.evictFinishedSessions(); err != nil {
			t.Fatal(err)
		}
		for i := 0; i <= maxFinishedSessionLogs; i++ {
			session := manager.sessions[fmt.Sprintf("subagent-tied-%02d", i)]
			if want := i == 0; session.logEvicted != want {
				t.Fatalf("attempt %d: session %s logEvicted = %v, want %v",
					attempt, session.ID, session.logEvicted, want)
			}
			session.logEvicted = false
			session.events = []Event{{Index: 0, Type: "completed"}}
		}
	}
}

// TestRunCompletionDrivesLogEviction proves retention runs at the terminal
// transition itself: once more than maxFinishedSessionLogs sessions are
// finished, the oldest finished session's event log is dropped while its
// record — terminal state and final answer — stays readable.
func TestRunCompletionDrivesLogEviction(t *testing.T) {
	manager := newTestManager(t, &recordingModel{})
	sessions := make([]*Session, 0, maxFinishedSessionLogs+1)
	for i := 0; i <= maxFinishedSessionLogs; i++ {
		session, err := manager.Start(StartRequest{
			ModelRef: "child",
			Task:     fmt.Sprintf("task %d", i),
		})
		if err != nil {
			t.Fatalf("Start() error = %v", err)
		}
		waitDone(t, session)
		if session.Status != "completed" {
			t.Fatalf("session %d = %q, want completed", i, session.Status)
		}
		sessions = append(sessions, session)
	}

	oldest := sessions[0]
	if !oldest.logEvicted {
		t.Fatal("oldest finished session kept its event log past the retention cap")
	}
	read := oldest.Read(0, defaultMaxOutput)
	for _, want := range []string{
		"Status: completed",
		"Event-Log: evicted by retention",
		"Final-Answer: done",
	} {
		if !strings.Contains(read, want) {
			t.Fatalf("evicted session read = %q, want %q", read, want)
		}
	}
	newest := sessions[len(sessions)-1]
	if newest.logEvicted {
		t.Fatal("most recently finished session lost its event log")
	}
	if read := newest.Read(0, defaultMaxOutput); !strings.Contains(read, "completed: done") {
		t.Fatalf("newest session read = %q, want the full completed event", read)
	}
	if _, ok := manager.Get(oldest.ID); !ok {
		t.Fatal("log-evicted session record vanished from the manager")
	}
}

func TestShutdownCancelErrorIncludesReason(t *testing.T) {
	base, shutdown := context.WithCancel(context.Background())
	manager := newTestManager(t, &recordingModel{})
	manager.baseCtx = base
	manager.contextFitter = func(
		context.Context,
		agent.ModelContext,
		[]conversation.Message,
		[]agent.ToolDefinition,
		int,
		...conversation.Message,
	) ([]conversation.Message, error) {
		shutdown()
		return nil, fmt.Errorf("context fitting failed")
	}

	session, err := manager.Start(StartRequest{ModelRef: "child", Task: "work"})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitDone(t, session)

	if session.Status != "failed" {
		t.Fatalf("session.Status = %q, want failed", session.Status)
	}
	if session.err == nil || !strings.Contains(session.err.Error(), "context canceled") ||
		!strings.Contains(session.err.Error(), "context fitting failed") {
		t.Fatalf("session.err = %v, want both cancellation and run errors", session.err)
	}
}

func TestKilledSessionEvictionWaitsForRunToFinish(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	release := make(chan struct{})
	model := &recordingModel{complete: func(
		ctx context.Context,
		_ string,
		_ []conversation.Message,
		_ []agent.ToolDefinition,
		call int,
	) (agent.ModelClientResult, error) {
		if call != 1 {
			return assistantResult("done"), nil
		}
		close(started)
		<-ctx.Done()
		close(canceled)
		<-release
		return agent.ModelClientResult{}, ctx.Err()
	}}
	manager := newTestManager(t, model)
	killed, err := manager.Start(StartRequest{ModelRef: "child", Task: "work"})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("model call did not start")
	}
	killed.Kill()
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("model call did not observe cancellation")
	}

	finishSessions := func() {
		t.Helper()
		for i := 0; i < maxFinishedSessionLogs; i++ {
			session, err := manager.Start(StartRequest{ModelRef: "child", Task: "work"})
			if err != nil {
				t.Fatalf("Start() error = %v", err)
			}
			waitDone(t, session)
		}
	}
	finishSessions()
	killed.mu.Lock()
	evictedBeforeReturn := killed.logEvicted
	killed.mu.Unlock()
	close(release)
	waitDone(t, killed)
	if evictedBeforeReturn {
		t.Fatal("killed session log was evicted while its engine was still running")
	}

	// Once the killed run has returned, it participates in retention exactly
	// like every other terminal session.
	finishSessions()
	killed.mu.Lock()
	evictedAfterReturn := killed.logEvicted
	killed.mu.Unlock()
	if !evictedAfterReturn {
		t.Fatal("killed session log survived past the finished-log retention cap")
	}
}

func TestEvictedReadKeepsEventCursorAndReleasesResult(t *testing.T) {
	session := &Session{
		ID:          "subagent-1",
		Status:      "completed",
		finalAnswer: "answer",
		events: []Event{
			{Index: 0, Type: "model_response", Text: "answer"},
			{Index: 1, Type: "completed", Text: "answer"},
		},
		result: &agent.EngineResult{
			Messages: []conversation.Message{{Role: conversation.AssistantRole}},
			Attachments: []conversation.Part{{
				Type: conversation.MediaPartType, DataURL: "large retained payload",
			}},
		},
	}
	session.dropLog()

	read := session.Read(0, defaultMaxOutput)
	if !strings.Contains(read, "Next-Event-Index: 2") {
		t.Fatalf("evicted session read = %q, want its terminal event cursor", read)
	}
	if session.result != nil {
		t.Fatal("log eviction retained the engine result and its attachments")
	}
}

func TestEvictedReadBoundsFinalAnswerOutput(t *testing.T) {
	session := &Session{
		ID:            "subagent-1",
		Status:        "completed",
		finalAnswer:   strings.Repeat("long final answer ", 2000),
		logEvicted:    true,
		droppedEvents: 2,
	}
	const limit = 512
	read := session.Read(0, limit)
	if len(read) > limit {
		t.Fatalf("read returned %d bytes, want at most %d", len(read), limit)
	}
	for _, want := range []string{
		"Status: completed", "Final-Answer: long final answer", "Output-Truncated: true", "Next-Event-Index: 2",
	} {
		if !strings.Contains(read, want) {
			t.Fatalf("read = %q, want %q", read, want)
		}
	}
}

func TestListConcurrentWithSessionCompletion(t *testing.T) {
	manager := newTestManager(t, &recordingModel{})
	list := NewList(manager)
	stop := make(chan struct{})
	readerDone := make(chan error, 1)
	go func() {
		for {
			select {
			case <-stop:
				readerDone <- nil
				return
			default:
			}
			if _, err := list.Run(context.Background(), `{"state":"all"}`); err != nil {
				readerDone <- err
				return
			}
		}
	}()

	for i := 0; i < 32; i++ {
		session, err := manager.Start(StartRequest{ModelRef: "child", Task: "work"})
		if err != nil {
			close(stop)
			t.Fatalf("Start() error = %v", err)
		}
		waitDone(t, session)
	}
	close(stop)
	if err := <-readerDone; err != nil {
		t.Fatalf("List.Run() error = %v", err)
	}
}
