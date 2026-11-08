package app

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/bus"
	channelport "github.com/q15co/q15/systems/agent/internal/channel"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/turnctx"
)

func TestCompletionWaitsForBusyAgentAndUsesNormalChatSession(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	messageBus := bus.New(2)
	parentStarted, releaseParent := make(chan struct{}), make(chan struct{})
	continued := make(chan struct{}, 1)
	session := &fakeSession{}
	input := []conversation.Message{
		conversation.SystemMessage("Review the result."),
		conversation.ToolResultMessage("completion", "child answer", false),
	}
	main := &fakeObservedAgent{
		reply: func(ctx context.Context, _ conversation.Message, _ agent.RunObserver) (agent.ReplyResult, error) {
			close(parentStarted)
			select {
			case <-releaseParent:
				return agent.ReplyResult{Text: "started"}, nil
			case <-ctx.Done():
				return agent.ReplyResult{}, ctx.Err()
			}
		},
		continueRun: func(ctx context.Context, parent int64, messages []conversation.Message, observer agent.RunObserver) (agent.ReplyResult, error) {
			origin, _ := turnctx.OriginFrom(ctx)
			if parent != 7 || !reflect.DeepEqual(messages, input) || origin.SessionID != "sess-17" {
				t.Errorf(
					"continuation lost its input or target: parent=%d origin=%#v messages=%#v",
					parent,
					origin,
					messages,
				)
			}
			observer.OnRunEvent(ctx, agent.RunEvent{Type: agent.RunEventRunStarted, Seq: 8})
			continued <- struct{}{}
			return agent.ReplyResult{Text: "I checked the answer."}, nil
		},
	}
	endpoint := &fakeEndpoint{
		channel: bus.ChannelBridge,
		open: func(_ context.Context, msg bus.InboundMessage) (channelport.AgentSession, error) {
			if msg.ChatID != "default" || msg.SessionID != "sess-17" {
				t.Errorf("wrong conversation: %#v", msg)
			}
			return session, nil
		},
	}
	done := make(chan error, 1)
	go func() { done <- runAgentWorker(ctx, messageBus, main, endpoint) }()
	if err := messageBus.PublishInbound(ctx, bus.InboundMessage{Channel: bus.ChannelBridge, ChatID: "default", SessionID: "sess-17", Text: "start work"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-parentStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("parent turn did not start")
	}
	if err := messageBus.PublishInbound(ctx, bus.InboundMessage{Channel: bus.ChannelBridge, ChatID: "default", SessionID: "sess-17", ParentTurn: 7, Continuation: input}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-continued:
		t.Fatal("completion interrupted the active turn")
	default:
	}
	close(releaseParent)
	waitForCondition(t, 2*time.Second, func() bool {
		session.mu.Lock()
		defer session.mu.Unlock()
		return len(session.finished) == 2
	})
	session.mu.Lock()
	if session.finished[1].Text != "I checked the answer." || len(session.events) != 1 {
		t.Errorf(
			"continuation did not finish through the normal chat session: %#v",
			session.finished,
		)
	}
	session.mu.Unlock()
	if main.Calls() != 1 {
		t.Fatalf("completion ran as a user request: Reply calls=%d", main.Calls())
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not stop")
	}
}
