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

// fakeNotifier records the outbound messages a manager publishes and can be
// made to fail, so a terminal notification can be asserted on both axes:
// what it said, and that a transport failure stayed a transport failure.
type fakeNotifier struct {
	mu       sync.Mutex
	messages []bus.OutboundMessage
	err      error
}

func (f *fakeNotifier) PublishOutboundAndWaitForDelivery(
	_ context.Context,
	msg bus.OutboundMessage,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.messages = append(f.messages, msg)
	return f.err
}

func (f *fakeNotifier) published() []bus.OutboundMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]bus.OutboundMessage, len(f.messages))
	copy(out, f.messages)
	return out
}

func testOrigin() turnctx.Origin {
	return turnctx.Origin{Channel: "telegram", ChatID: "chat-42", UserID: "user-7"}
}

func TestTerminalNotificationReachesOriginChat(t *testing.T) {
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
	notifier := &fakeNotifier{}
	manager.notifier = notifier

	session, err := manager.Start(StartRequest{
		ModelRef: "child",
		Task:     "summarise the repository",
		Origin:   testOrigin(),
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitDone(t, session)

	msgs := notifier.published()
	if len(msgs) != 1 {
		t.Fatalf("notifications = %d, want exactly 1: %#v", len(msgs), msgs)
	}
	msg := msgs[0]
	if msg.Channel != testOrigin().Channel || msg.ChatID != testOrigin().ChatID {
		t.Fatalf("notification target = %q/%q, want the spawning chat %q/%q",
			msg.Channel, msg.ChatID, testOrigin().Channel, testOrigin().ChatID)
	}
	for _, want := range []string{session.ID, "completed", "the work is done", "summarise the repository"} {
		if !strings.Contains(msg.Text, want) {
			t.Fatalf("notification text %q does not mention %q", msg.Text, want)
		}
	}
}

func TestTerminalNotificationReportsFailureReason(t *testing.T) {
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
	notifier := &fakeNotifier{}
	manager.notifier = notifier

	session, err := manager.Start(StartRequest{
		ModelRef: "child",
		Task:     "do the thing",
		Origin:   testOrigin(),
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitDone(t, session)

	msgs := notifier.published()
	if len(msgs) != 1 {
		t.Fatalf("notifications = %d, want exactly 1", len(msgs))
	}
	if !strings.Contains(msgs[0].Text, "failed") {
		t.Fatalf("notification text %q does not report the failed status", msgs[0].Text)
	}
	if !strings.Contains(msgs[0].Text, "provider exploded") {
		t.Fatalf("notification text %q does not carry the failure reason", msgs[0].Text)
	}
}

func TestKilledSessionNotifiesAsKilled(t *testing.T) {
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
	notifier := &fakeNotifier{}
	manager.notifier = notifier

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

	msgs := notifier.published()
	if len(msgs) != 1 {
		t.Fatalf("notifications = %d, want exactly 1", len(msgs))
	}
	if !strings.Contains(msgs[0].Text, "killed") {
		t.Fatalf("notification text %q does not report the killed status", msgs[0].Text)
	}
}

func TestNoOriginDoesNotNotify(t *testing.T) {
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
	notifier := &fakeNotifier{}
	manager.notifier = notifier

	session, err := manager.Start(StartRequest{ModelRef: "child", Task: "work"})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitDone(t, session)

	if msgs := notifier.published(); len(msgs) != 0 {
		t.Fatalf("notifications = %#v, want none for a session with no origin", msgs)
	}
}

// TestNotificationDeliveryFailureKeepsTerminalStatus pins the asymmetry: the
// session record is the source of truth and a broken transport must not turn a
// completed session into a failed one.
func TestNotificationDeliveryFailureKeepsTerminalStatus(t *testing.T) {
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
	notifier := &fakeNotifier{err: errors.New("transport down")}
	manager.notifier = notifier

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
	if msgs := notifier.published(); len(msgs) != 1 {
		t.Fatalf("delivery attempts = %d, want 1 (no retry)", len(msgs))
	}
}

func TestNotificationIsBounded(t *testing.T) {
	// Multibyte on purpose: a byte bound would cut this far below the stated
	// rune budget, and the assertion would not notice.
	huge := strings.Repeat("ä", maxNotificationRunes*2)
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
	notifier := &fakeNotifier{}
	manager.notifier = notifier

	session, err := manager.Start(StartRequest{
		ModelRef: "child",
		Task:     "work",
		Origin:   testOrigin(),
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitDone(t, session)

	msgs := notifier.published()
	if len(msgs) != 1 {
		t.Fatalf("notifications = %d, want exactly 1", len(msgs))
	}
	// Equality, not an upper bound: a byte-based cut would produce a message of
	// roughly half this length, and only the exact budget catches that.
	if got := utf8.RuneCountInString(msgs[0].Text); got != maxNotificationRunes {
		t.Fatalf(
			"notification length = %d runes, want exactly %d: the bound did not use its budget",
			got,
			maxNotificationRunes,
		)
	}
	if !strings.Contains(msgs[0].Text, "truncated") {
		t.Fatalf("notification %q does not say the answer was truncated", msgs[0].Text)
	}
	if !utf8.ValidString(msgs[0].Text) {
		t.Fatal("notification is not valid UTF-8: truncation split a rune")
	}
}

// TestInterruptedSessionDoesNotNotify pins the spec's exclusion: Close marks
// every live session interrupted, so announcing that state would deliver one
// message per live session on every restart.
func TestInterruptedSessionDoesNotNotify(t *testing.T) {
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
	notifier := &fakeNotifier{}
	manager.notifier = notifier

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
	if msgs := notifier.published(); len(msgs) != 0 {
		t.Fatalf("notifications = %#v, want none for an interrupted session", msgs)
	}
}
