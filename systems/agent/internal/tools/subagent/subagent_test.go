package subagent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/config"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	q15media "github.com/q15co/q15/systems/agent/internal/media"
)

type recordingModel struct {
	mu       sync.Mutex
	calls    [][]conversation.Message
	tools    [][]agent.ToolDefinition
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
	_ = model
	m.mu.Lock()
	m.calls = append(m.calls, conversation.CloneMessages(messages))
	m.tools = append(m.tools, append([]agent.ToolDefinition(nil), tools...))
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

	session, err := manager.Start(
		context.Background(),
		"child",
		"summarize this",
		"",
		nil,
		0,
	)
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
		inner: textOnlyModel,
		store: store,
	}
	if _, err := textOnlyClient.Complete(context.Background(), "child", messages, nil); err != nil {
		t.Fatalf("Complete(text-only) error = %v", err)
	}
	textOnly := textOnlyModel.snapshotCalls()[0][0]
	if subagentTestHasPartType(textOnly, conversation.ImagePartType) ||
		subagentTestHasPartType(textOnly, conversation.AudioPartType) {
		t.Fatalf("text-only subagent received inline media: %#v", textOnly.Parts)
	}
	if !subagentTestTextContains(textOnly, "[Media: image]") ||
		!subagentTestTextContains(textOnly, "[Media: audio]") {
		t.Fatalf("text-only subagent parts = %#v, want image and audio hints", textOnly.Parts)
	}

	visionModel := &recordingModel{}
	visionClient := &mediaAdaptiveClient{
		inner:   visionModel,
		support: q15media.Support{Image: true},
		store:   store,
	}
	if _, err := visionClient.Complete(context.Background(), "child", messages, nil); err != nil {
		t.Fatalf("Complete(vision) error = %v", err)
	}
	vision := visionModel.snapshotCalls()[0][0]
	if !subagentTestHasPartType(vision, conversation.ImagePartType) {
		t.Fatalf("vision subagent parts = %#v, want image retained", vision.Parts)
	}
	if subagentTestHasPartType(vision, conversation.AudioPartType) ||
		!subagentTestTextContains(vision, "[Media: audio]") {
		t.Fatalf("vision subagent parts = %#v, want audio downgraded", vision.Parts)
	}

	if messages[0].Parts[1].Type != conversation.ImagePartType ||
		messages[0].Parts[2].Type != conversation.AudioPartType {
		t.Fatalf("canonical subagent transcript mutated: %#v", messages[0].Parts)
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

	session, err := manager.Start(
		context.Background(),
		"child",
		"initial task",
		"",
		nil,
		0,
	)
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

	session, err := manager.Start(context.Background(), "child", "initial task", "", nil, 0)
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

	session, err := manager.Start(context.Background(), "child", "initial task", "", nil, 0)
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

func newTestManager(t *testing.T, model agent.ModelClient) *Manager {
	t.Helper()
	registry, err := agent.NewToolRegistry(noopTool{name: "read_file"})
	if err != nil {
		t.Fatalf("NewToolRegistry() error = %v", err)
	}
	return NewManager(
		[]config.AgentModelRuntime{{Ref: "child"}},
		func(config.AgentModelRuntime, q15media.Store) (agent.ModelClient, error) {
			return model, nil
		},
		registry,
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

func subagentTestHasPartType(message conversation.Message, partType conversation.PartType) bool {
	for _, part := range message.Parts {
		if part.Type == partType {
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
	select {
	case <-session.done:
	case <-time.After(2 * time.Second):
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
