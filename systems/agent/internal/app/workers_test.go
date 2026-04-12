package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/bus"
	channelport "github.com/q15co/q15/systems/agent/internal/channel"
	"github.com/q15co/q15/systems/agent/internal/conversation"
)

type fakeObservedAgent struct {
	mu         sync.Mutex
	replyCalls int
	reply      func(context.Context, conversation.Message, agent.RunObserver) (agent.ReplyResult, error)
}

func (f *fakeObservedAgent) Reply(
	ctx context.Context,
	userInput conversation.Message,
	observer agent.RunObserver,
) (agent.ReplyResult, error) {
	f.mu.Lock()
	f.replyCalls++
	f.mu.Unlock()

	if f.reply != nil {
		return f.reply(ctx, userInput, observer)
	}
	return agent.ReplyResult{Text: "ok"}, nil
}

func (f *fakeObservedAgent) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.replyCalls
}

type fakeEndpoint struct {
	channel string

	mu        sync.Mutex
	openCalls int
	open      func(context.Context, bus.InboundMessage) (channelport.AgentSession, error)
}

func (f *fakeEndpoint) Channel() string {
	return f.channel
}

func (f *fakeEndpoint) OpenSession(
	ctx context.Context,
	msg bus.InboundMessage,
) (channelport.AgentSession, error) {
	f.mu.Lock()
	f.openCalls++
	f.mu.Unlock()

	if f.open != nil {
		return f.open(ctx, msg)
	}
	return nil, nil
}

func (f *fakeEndpoint) OpenCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.openCalls
}

type fakeSession struct {
	mu sync.Mutex

	events       []agent.RunEvent
	finished     []agent.ReplyResult
	abortReasons []string
}

func (f *fakeSession) OnRunEvent(_ context.Context, event agent.RunEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, event)
}

func (f *fakeSession) Finish(_ context.Context, result agent.ReplyResult) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finished = append(f.finished, result)
}

func (f *fakeSession) Abort(_ context.Context, reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.abortReasons = append(f.abortReasons, reason)
}

func waitForCondition(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}

	t.Fatal("timed out waiting for condition")
}

func TestBuildEndpointRegistry_RejectsDuplicates(t *testing.T) {
	_, err := buildEndpointRegistry(
		&fakeEndpoint{channel: bus.ChannelTelegram},
		&fakeEndpoint{channel: bus.ChannelTelegram},
	)
	if err == nil {
		t.Fatal("buildEndpointRegistry() error = nil, want non-nil")
	}
}

func TestFormatReplyError_StopReasons(t *testing.T) {
	t.Run("tool turn limit", func(t *testing.T) {
		got := formatReplyError(&agent.StopError{
			Reason: agent.StopReasonToolTurnLimit,
			Detail: "max tool-call turns reached (96)",
		})
		want := "I stopped this run after reaching an internal tool-call safety limit. Progress was saved."
		if got != want {
			t.Fatalf("formatReplyError() = %q, want %q", got, want)
		}
	})

	t.Run("tool loop detected", func(t *testing.T) {
		got := formatReplyError(&agent.StopError{
			Reason: agent.StopReasonToolLoopDetected,
			Detail: "tool loop detected",
		})
		want := "I stopped this run because tool calls appeared stuck in a loop. Progress was saved."
		if got != want {
			t.Fatalf("formatReplyError() = %q, want %q", got, want)
		}
	})
}

func TestFormatReplyError_Generic(t *testing.T) {
	err := errors.New("boom")
	got := formatReplyError(err)
	want := "reply error: boom"
	if got != want {
		t.Fatalf("formatReplyError() = %q, want %q", got, want)
	}
}

func TestRunAgentWorker_FinishesSessionAndForwardsEvents(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	messageBus := bus.New(8)
	session := &fakeSession{}
	agentImpl := &fakeObservedAgent{
		reply: func(ctx context.Context, _ conversation.Message, observer agent.RunObserver) (agent.ReplyResult, error) {
			observer.OnRunEvent(ctx, agent.RunEvent{Type: agent.RunEventRunStarted})
			return agent.ReplyResult{Text: "done"}, nil
		},
	}
	endpoint := &fakeEndpoint{
		channel: bus.ChannelTelegram,
		open: func(context.Context, bus.InboundMessage) (channelport.AgentSession, error) {
			return session, nil
		},
	}

	done := make(chan error, 1)
	go func() {
		done <- runAgentWorker(ctx, messageBus, agentImpl, endpoint)
	}()

	if err := messageBus.PublishInbound(ctx, bus.InboundMessage{
		Channel: bus.ChannelTelegram,
		ChatID:  "123",
		Text:    "hello",
	}); err != nil {
		t.Fatalf("PublishInbound() error = %v", err)
	}

	waitForCondition(t, 2*time.Second, func() bool {
		session.mu.Lock()
		defer session.mu.Unlock()
		return len(session.finished) == 1 && len(session.events) == 1
	})

	session.mu.Lock()
	if got := session.finished[0].Text; got != "done" {
		session.mu.Unlock()
		t.Fatalf("finished[0].Text = %q, want %q", got, "done")
	}
	if len(session.finished[0].MediaRefs) != 0 {
		session.mu.Unlock()
		t.Fatalf("finished[0].MediaRefs = %#v, want empty", session.finished[0].MediaRefs)
	}
	if got := session.events[0].Type; got != agent.RunEventRunStarted {
		session.mu.Unlock()
		t.Fatalf("events[0].Type = %q, want %q", got, agent.RunEventRunStarted)
	}
	session.mu.Unlock()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runAgentWorker() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for worker exit")
	}
}

func TestUserMessageFromInboundBuildsOrderedTextAndImageParts(t *testing.T) {
	sentAt := time.Date(2026, time.April, 12, 10, 11, 12, 0, time.FixedZone("UTC+2", 2*60*60))
	got := userMessageFromInbound(bus.InboundMessage{
		SentAt: sentAt,
		Text:   "hello",
		Media:  []string{"media://sha256/abc"},
	})

	if got.Role != conversation.UserRole {
		t.Fatalf("role = %q, want user", got.Role)
	}
	if stored, ok := conversation.UserMessageTimeLocal(got); !ok ||
		!stored.Equal(sentAt.In(time.Local)) {
		t.Fatalf("stored user timestamp = %v, %t, want %v", stored, ok, sentAt.In(time.Local))
	}
	if len(got.Parts) != 2 {
		t.Fatalf("parts len = %d, want 2", len(got.Parts))
	}
	if got.Parts[0].Type != conversation.TextPartType || got.Parts[0].Text != "hello" {
		t.Fatalf("parts[0] = %#v", got.Parts[0])
	}
	if got.Parts[1].Type != conversation.ImagePartType ||
		got.Parts[1].MediaRef != "media://sha256/abc" {
		t.Fatalf("parts[1] = %#v", got.Parts[1])
	}
}

func TestUserMessageFromInboundDefaultsMissingSentAt(t *testing.T) {
	got := userMessageFromInbound(bus.InboundMessage{
		Text: "hello",
	})

	stored, ok := conversation.UserMessageTimeLocal(got)
	if !ok {
		t.Fatalf("stored user timestamp missing: %#v", got.UserTemporal)
	}
	if stored.IsZero() {
		t.Fatal("stored user timestamp = zero, want non-zero")
	}
}

func TestRunAgentWorker_AcceptsMediaOnlyInboundMessage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	messageBus := bus.New(8)
	session := &fakeSession{}
	agentImpl := &fakeObservedAgent{
		reply: func(_ context.Context, msg conversation.Message, _ agent.RunObserver) (agent.ReplyResult, error) {
			if len(msg.Parts) != 1 {
				t.Fatalf("reply message parts len = %d, want 1", len(msg.Parts))
			}
			if msg.Parts[0].Type != conversation.ImagePartType ||
				msg.Parts[0].MediaRef != "media://sha256/abc" {
				t.Fatalf("reply image part = %#v", msg.Parts[0])
			}
			return agent.ReplyResult{Text: "done"}, nil
		},
	}
	endpoint := &fakeEndpoint{
		channel: bus.ChannelTelegram,
		open: func(context.Context, bus.InboundMessage) (channelport.AgentSession, error) {
			return session, nil
		},
	}

	done := make(chan error, 1)
	go func() {
		done <- runAgentWorker(ctx, messageBus, agentImpl, endpoint)
	}()

	if err := messageBus.PublishInbound(ctx, bus.InboundMessage{
		Channel: bus.ChannelTelegram,
		ChatID:  "123",
		Media:   []string{"media://sha256/abc"},
	}); err != nil {
		t.Fatalf("PublishInbound() error = %v", err)
	}

	waitForCondition(t, 2*time.Second, func() bool {
		session.mu.Lock()
		defer session.mu.Unlock()
		return len(session.finished) == 1
	})

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runAgentWorker() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for worker exit")
	}
}

func TestRunAgentWorker_PassesStructuredReplyResultToSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	messageBus := bus.New(8)
	session := &fakeSession{}
	agentImpl := &fakeObservedAgent{
		reply: func(_ context.Context, _ conversation.Message, _ agent.RunObserver) (agent.ReplyResult, error) {
			return agent.ReplyResult{
				Text:      "done",
				MediaRefs: []string{"media://sha256/reply"},
			}, nil
		},
	}
	endpoint := &fakeEndpoint{
		channel: bus.ChannelTelegram,
		open: func(context.Context, bus.InboundMessage) (channelport.AgentSession, error) {
			return session, nil
		},
	}

	done := make(chan error, 1)
	go func() {
		done <- runAgentWorker(ctx, messageBus, agentImpl, endpoint)
	}()

	if err := messageBus.PublishInbound(ctx, bus.InboundMessage{
		Channel: bus.ChannelTelegram,
		ChatID:  "123",
		Text:    "hello",
	}); err != nil {
		t.Fatalf("PublishInbound() error = %v", err)
	}

	waitForCondition(t, 2*time.Second, func() bool {
		session.mu.Lock()
		defer session.mu.Unlock()
		return len(session.finished) == 1
	})

	session.mu.Lock()
	defer session.mu.Unlock()
	if got := session.finished[0].Text; got != "done" {
		t.Fatalf("finished[0].Text = %q, want done", got)
	}
	if got := session.finished[0].MediaRefs; len(got) != 1 || got[0] != "media://sha256/reply" {
		t.Fatalf("finished[0].MediaRefs = %#v, want reply media ref", got)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runAgentWorker() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for worker exit")
	}
}

func TestRunAgentWorker_FormatsStopErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	messageBus := bus.New(8)
	session := &fakeSession{}
	agentImpl := &fakeObservedAgent{
		reply: func(context.Context, conversation.Message, agent.RunObserver) (agent.ReplyResult, error) {
			return agent.ReplyResult{}, &agent.StopError{
				Reason: agent.StopReasonToolLoopDetected,
				Detail: "tool loop detected",
			}
		},
	}
	endpoint := &fakeEndpoint{
		channel: bus.ChannelTelegram,
		open: func(context.Context, bus.InboundMessage) (channelport.AgentSession, error) {
			return session, nil
		},
	}

	done := make(chan error, 1)
	go func() {
		done <- runAgentWorker(ctx, messageBus, agentImpl, endpoint)
	}()

	if err := messageBus.PublishInbound(ctx, bus.InboundMessage{
		Channel: bus.ChannelTelegram,
		ChatID:  "123",
		Text:    "hello",
	}); err != nil {
		t.Fatalf("PublishInbound() error = %v", err)
	}

	waitForCondition(t, 2*time.Second, func() bool {
		session.mu.Lock()
		defer session.mu.Unlock()
		return len(session.finished) == 1
	})

	session.mu.Lock()
	got := session.finished[0].Text
	session.mu.Unlock()

	want := "I stopped this run because tool calls appeared stuck in a loop. Progress was saved."
	if got != want {
		t.Fatalf("finished[0].Text = %q, want %q", got, want)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runAgentWorker() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for worker exit")
	}
}

func TestRunAgentWorker_LocalControlMessageSkipsAgent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	messageBus := bus.New(8)
	agentImpl := &fakeObservedAgent{}
	endpoint := &fakeEndpoint{
		channel: bus.ChannelTelegram,
		open: func(context.Context, bus.InboundMessage) (channelport.AgentSession, error) {
			return nil, nil
		},
	}

	done := make(chan error, 1)
	go func() {
		done <- runAgentWorker(ctx, messageBus, agentImpl, endpoint)
	}()

	if err := messageBus.PublishInbound(ctx, bus.InboundMessage{
		Channel: bus.ChannelTelegram,
		ChatID:  "123",
		Text:    "/progress verbose",
	}); err != nil {
		t.Fatalf("PublishInbound() error = %v", err)
	}

	waitForCondition(t, 2*time.Second, func() bool {
		return endpoint.OpenCalls() == 1
	})

	if agentImpl.Calls() != 0 {
		t.Fatalf("agent calls = %d, want 0", agentImpl.Calls())
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runAgentWorker() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for worker exit")
	}
}

func TestRunAgentWorker_CancellationAbortsSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	messageBus := bus.New(8)
	session := &fakeSession{}
	agentImpl := &fakeObservedAgent{
		reply: func(ctx context.Context, _ conversation.Message, _ agent.RunObserver) (agent.ReplyResult, error) {
			<-ctx.Done()
			return agent.ReplyResult{}, ctx.Err()
		},
	}
	endpoint := &fakeEndpoint{
		channel: bus.ChannelTelegram,
		open: func(context.Context, bus.InboundMessage) (channelport.AgentSession, error) {
			return session, nil
		},
	}

	done := make(chan error, 1)
	go func() {
		done <- runAgentWorker(ctx, messageBus, agentImpl, endpoint)
	}()

	if err := messageBus.PublishInbound(ctx, bus.InboundMessage{
		Channel: bus.ChannelTelegram,
		ChatID:  "123",
		Text:    "hello",
	}); err != nil {
		t.Fatalf("PublishInbound() error = %v", err)
	}

	waitForCondition(t, 2*time.Second, func() bool {
		return agentImpl.Calls() == 1
	})

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runAgentWorker() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for worker exit")
	}

	session.mu.Lock()
	defer session.mu.Unlock()
	if len(session.abortReasons) != 1 || session.abortReasons[0] != "canceled" {
		t.Fatalf("abortReasons = %#v, want [canceled]", session.abortReasons)
	}
}
