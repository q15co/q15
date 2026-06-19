package subagent

import (
	"context"
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
	if err == nil || !strings.Contains(err.Error(), "denies /memory access") {
		t.Fatalf("CheckToolCall() error = %v, want /memory denial", err)
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
