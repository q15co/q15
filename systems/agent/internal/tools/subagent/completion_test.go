package subagent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/bus"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/turnctx"
)

type fakeCompletionPublisher struct {
	mu       sync.Mutex
	messages []bus.InboundMessage
	err      error
	deadline time.Time
}

func (f *fakeCompletionPublisher) PublishInbound(
	ctx context.Context,
	msg bus.InboundMessage,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.messages = append(f.messages, msg)
	f.deadline, _ = ctx.Deadline()
	return f.err
}

func (f *fakeCompletionPublisher) published() []bus.InboundMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]bus.InboundMessage, len(f.messages))
	copy(out, f.messages)
	return out
}

func testOrigin() turnctx.Origin {
	return turnctx.Origin{
		Channel:   "bridge",
		ChatID:    "chat-42",
		UserID:    "owner",
		SessionID: "sess-17",
	}
}

func TestTerminalCompletionReachesInteractiveAgent(t *testing.T) {
	model := &recordingModel{complete: func(
		context.Context,
		string,
		[]conversation.Message,
		[]agent.ToolDefinition,
		int,
	) (agent.ModelClientResult, error) {
		return assistantResult("the work is done"), nil
	}}
	manager := newTestManager(t, model)
	publisher := &fakeCompletionPublisher{}
	manager.publisher = publisher

	session, err := manager.Start(StartRequest{
		ModelRef:   "child",
		Task:       "summarise the repository",
		Origin:     testOrigin(),
		ParentTurn: 73,
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitDone(t, session)
	if remaining := time.Until(publisher.deadline); remaining <= 0 ||
		remaining > completionEnqueueTimeout {
		t.Fatalf("completion enqueue is not bounded: remaining=%v", remaining)
	}

	msgs := publisher.published()
	if len(msgs) != 1 {
		t.Fatalf("completions = %d, want exactly 1: %#v", len(msgs), msgs)
	}
	msg := msgs[0]
	if msg.Channel != testOrigin().Channel || msg.ChatID != testOrigin().ChatID {
		t.Fatalf("completion target = %q/%q, want the spawning chat %q/%q",
			msg.Channel, msg.ChatID, testOrigin().Channel, testOrigin().ChatID)
	}
	if msg.SessionID != testOrigin().SessionID || msg.ParentTurn != 73 || msg.Text != "" {
		t.Fatalf("completion routing = %#v", msg)
	}
	if len(msg.Continuation) != 3 || msg.Continuation[0].Role != conversation.SystemRole ||
		msg.Continuation[1].Role != conversation.AssistantRole || msg.Continuation[2].Role != conversation.ToolRole {
		t.Fatalf(
			"completion is not a trusted runtime instruction followed by an untrusted tool result: %#v",
			msg.Continuation,
		)
	}
	call := msg.Continuation[1].Parts[0]
	if call.Name != conversation.SubagentCompletionName ||
		!conversation.IsSubagentCompletion(call) {
		t.Fatalf("completion is disguised as an assistant tool invocation: %#v", call)
	}
	for _, want := range []string{session.ID, "completed", "the work is done", "summarise the repository"} {
		if !strings.Contains(completionText(msg), want) {
			t.Fatalf("completion text %q does not mention %q", completionText(msg), want)
		}
	}
}

func TestTerminalCompletionReportsFailureReason(t *testing.T) {
	model := &recordingModel{complete: func(
		context.Context,
		string,
		[]conversation.Message,
		[]agent.ToolDefinition,
		int,
	) (agent.ModelClientResult, error) {
		return agent.ModelClientResult{}, errors.New("provider exploded")
	}}
	manager := newTestManager(t, model)
	publisher := &fakeCompletionPublisher{}
	manager.publisher = publisher

	session, err := manager.Start(StartRequest{
		ModelRef: "child",
		Task:     "do the thing",
		Origin:   testOrigin(),
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitDone(t, session)

	msgs := publisher.published()
	if len(msgs) != 1 {
		t.Fatalf("completions = %d, want exactly 1", len(msgs))
	}
	if !strings.Contains(completionText(msgs[0]), "failed") {
		t.Fatalf("completion text %q does not report the failed status", completionText(msgs[0]))
	}
	if !strings.Contains(completionText(msgs[0]), "provider exploded") {
		t.Fatalf("completion text %q does not carry the failure reason", completionText(msgs[0]))
	}
}

func TestKilledSessionReturnsCompletionAsKilled(t *testing.T) {
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
	publisher := &fakeCompletionPublisher{}
	manager.publisher = publisher

	session, err := manager.Start(StartRequest{
		ModelRef: "child",
		Task:     "long job",
		Origin:   testOrigin(),
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("model call did not start")
	}
	session.Kill()
	waitDone(t, session)

	msgs := publisher.published()
	if len(msgs) != 1 {
		t.Fatalf("completions = %d, want exactly 1", len(msgs))
	}
	if !strings.Contains(completionText(msgs[0]), "killed") {
		t.Fatalf("completion text %q does not report the killed status", completionText(msgs[0]))
	}
}

func TestNoOriginDoesNotReturnCompletion(t *testing.T) {
	model := &recordingModel{complete: func(
		context.Context,
		string,
		[]conversation.Message,
		[]agent.ToolDefinition,
		int,
	) (agent.ModelClientResult, error) {
		return assistantResult("done"), nil
	}}
	manager := newTestManager(t, model)
	publisher := &fakeCompletionPublisher{}
	manager.publisher = publisher

	session, err := manager.Start(StartRequest{ModelRef: "child", Task: "work"})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitDone(t, session)

	if msgs := publisher.published(); len(msgs) != 0 {
		t.Fatalf("completions = %#v, want none for a session with no origin", msgs)
	}
}

// TestCompletionQueueFailureKeepsTerminalStatus pins the asymmetry: the
// session record is the source of truth and a full completion queue must not turn a
// completed session into a failed one.
func TestCompletionQueueFailureKeepsTerminalStatus(t *testing.T) {
	model := &recordingModel{complete: func(
		context.Context,
		string,
		[]conversation.Message,
		[]agent.ToolDefinition,
		int,
	) (agent.ModelClientResult, error) {
		return assistantResult("finished anyway"), nil
	}}
	manager := newTestManager(t, model)
	publisher := &fakeCompletionPublisher{err: errors.New("queue full")}
	manager.publisher = publisher

	session, err := manager.Start(StartRequest{
		ModelRef: "child",
		Task:     "work",
		Origin:   testOrigin(),
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitDone(t, session)

	if session.Status != "completed" {
		t.Fatalf("session.Status = %q, want completed despite the delivery failure", session.Status)
	}
	if msgs := publisher.published(); len(msgs) != 1 {
		t.Fatalf("delivery attempts = %d, want 1 (no retry)", len(msgs))
	}
}

func TestCompletionIsBounded(t *testing.T) {
	// Multibyte on purpose: a byte bound would cut this far below the stated
	// rune budget, and the assertion would not notice.
	huge := strings.Repeat("ä", maxCompletionRunes*2)
	model := &recordingModel{complete: func(
		context.Context,
		string,
		[]conversation.Message,
		[]agent.ToolDefinition,
		int,
	) (agent.ModelClientResult, error) {
		return assistantResult(huge), nil
	}}
	manager := newTestManager(t, model)
	publisher := &fakeCompletionPublisher{}
	manager.publisher = publisher

	session, err := manager.Start(StartRequest{
		ModelRef: "child",
		Task:     "work",
		Origin:   testOrigin(),
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitDone(t, session)

	msgs := publisher.published()
	if len(msgs) != 1 {
		t.Fatalf("completions = %d, want exactly 1", len(msgs))
	}
	// Equality, not an upper bound: a byte-based cut would produce a message of
	// roughly half this length, and only the exact budget catches that.
	if got := utf8.RuneCountInString(completionText(msgs[0])); got != maxCompletionRunes {
		t.Fatalf(
			"completion length = %d runes, want exactly %d: the bound did not use its budget",
			got,
			maxCompletionRunes,
		)
	}
	if !strings.Contains(completionText(msgs[0]), "truncated") {
		t.Fatalf("completion %q does not say the answer was truncated", completionText(msgs[0]))
	}
	if !utf8.ValidString(completionText(msgs[0])) {
		t.Fatal("completion is not valid UTF-8: truncation split a rune")
	}
}

// TestInterruptedSessionDoesNotReturnCompletion pins the spec's exclusion: Close marks
// every live session interrupted, so announcing that state would deliver one
// message per live session on every restart.
func TestInterruptedSessionDoesNotReturnCompletion(t *testing.T) {
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
	publisher := &fakeCompletionPublisher{}
	manager.publisher = publisher

	session, err := manager.Start(StartRequest{
		ModelRef: "child",
		Task:     "work",
		Origin:   testOrigin(),
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("model call did not start")
	}

	if err := manager.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if session.Status != "interrupted" {
		t.Fatalf(
			"session.Status = %q, want interrupted: the test did not reach the path",
			session.Status,
		)
	}
	if msgs := publisher.published(); len(msgs) != 0 {
		t.Fatalf("completions = %#v, want none for an interrupted session", msgs)
	}
}

func completionText(message bus.InboundMessage) string {
	if len(message.Continuation) != 3 || len(message.Continuation[2].Parts) != 1 {
		return ""
	}
	return message.Continuation[2].Parts[0].Content
}

func TestSynchronousSpawnDoesNotQueueAnotherInteractiveTurn(t *testing.T) {
	model := &recordingModel{
		complete: func(context.Context, string, []conversation.Message, []agent.ToolDefinition, int) (agent.ModelClientResult, error) {
			return assistantResult("the answer is already returned inline"), nil
		},
	}
	manager := newTestManager(t, model)
	publisher := &fakeCompletionPublisher{}
	manager.publisher = publisher
	ctx := turnctx.WithOrigin(t.Context(), testOrigin())
	output, err := NewSpawn(
		manager,
	).Run(ctx, `{"model":"child","task":"short work","wait_seconds":1}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "the answer is already returned inline") {
		t.Fatalf("spawn output = %q", output)
	}
	for _, session := range manager.List("all", 10) {
		waitDone(t, session)
	}
	if messages := publisher.published(); len(messages) != 0 {
		t.Fatalf("synchronous result also queued another turn: %#v", messages)
	}
}

func TestAsynchronousSpawnQueuesCompletionAfterParentContextEnds(t *testing.T) {
	release := make(chan struct{})
	model := &recordingModel{
		complete: func(ctx context.Context, _ string, _ []conversation.Message, _ []agent.ToolDefinition, _ int) (agent.ModelClientResult, error) {
			select {
			case <-release:
				return assistantResult("background answer"), nil
			case <-ctx.Done():
				return agent.ModelClientResult{}, ctx.Err()
			}
		},
	}
	manager := newTestManager(t, model)
	publisher := &fakeCompletionPublisher{}
	manager.publisher = publisher
	ctx, cancel := context.WithCancel(turnctx.WithOrigin(t.Context(), testOrigin()))
	defer cancel()
	if _, err := NewSpawn(manager).Run(ctx, `{"model":"child","task":"background work","wait_seconds":0}`); err != nil {
		t.Fatal(err)
	}
	cancel()
	close(release)
	for _, session := range manager.List("all", 10) {
		waitDone(t, session)
	}
	messages := publisher.published()
	if len(messages) != 1 || !strings.Contains(completionText(messages[0]), "background answer") {
		t.Fatalf("asynchronous completion = %#v", messages)
	}
}
