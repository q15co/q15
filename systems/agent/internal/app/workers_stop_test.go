package app

import (
	"context"
	"testing"
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/bus"
	channelport "github.com/q15co/q15/systems/agent/internal/channel"
	"github.com/q15co/q15/systems/agent/internal/conversation"
)

type stoppableTestSession struct {
	fakeSession
	cancel          context.CancelFunc
	abortContextErr error
}

func (s *stoppableTestSession) SetCancel(cancel context.CancelFunc) {
	s.cancel = cancel
}

func (s *stoppableTestSession) Abort(ctx context.Context, reason string) {
	s.abortContextErr = ctx.Err()
	s.fakeSession.Abort(ctx, reason)
}

func TestRunAgentWorkerUserStopAbortsOnlyCurrentRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	messageBus := bus.New(2)
	stopped := &stoppableTestSession{}
	next := &fakeSession{}
	endpoint := &fakeEndpoint{channel: bus.ChannelTelegram, open: func(
		_ context.Context, msg bus.InboundMessage,
	) (channelport.AgentSession, error) {
		if msg.Text == "stop this" {
			return stopped, nil
		}
		return next, nil
	}}
	finished := make(chan struct{})
	impl := &fakeObservedAgent{reply: func(
		runCtx context.Context, _ conversation.Message, observer agent.RunObserver,
	) (agent.ReplyResult, error) {
		if observer == stopped {
			if stopped.cancel == nil {
				t.Error("session has no cancel function before Reply")
				cancel()
				return agent.ReplyResult{}, context.Canceled
			}
			stopped.cancel()
			return agent.ReplyResult{Text: "must not be sent"}, runCtx.Err()
		}
		close(finished)
		return agent.ReplyResult{Text: "next reply"}, nil
	}}
	done := make(chan error, 1)
	go func() { done <- runAgentWorker(ctx, messageBus, impl, endpoint) }()
	for _, text := range []string{"stop this", "next message"} {
		if err := messageBus.PublishInbound(ctx, bus.InboundMessage{
			Channel: bus.ChannelTelegram, ChatID: "123", Text: text,
		}); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not process the next message after user stop")
	}
	waitForCondition(t, 2*time.Second, func() bool {
		next.mu.Lock()
		defer next.mu.Unlock()
		return len(next.finished) == 1
	})
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not stop")
	}
	if stopped.abortContextErr != nil || len(stopped.abortReasons) != 1 ||
		len(stopped.finished) != 0 {
		t.Fatalf(
			"stop cleanup: ctx=%v reasons=%v final=%v",
			stopped.abortContextErr,
			stopped.abortReasons,
			stopped.finished,
		)
	}
	if next.finished[0].Text != "next reply" {
		t.Fatalf("next final = %#v", next.finished)
	}
}
