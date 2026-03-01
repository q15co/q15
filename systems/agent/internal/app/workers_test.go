package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/bus"
)

type fakeAgent struct {
	reply func(context.Context, string) (string, error)
}

func (f *fakeAgent) Reply(ctx context.Context, userInput string) (string, error) {
	return f.reply(ctx, userInput)
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

func TestRunAgentWorker_FormatsStopErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	messageBus := bus.New(8)
	agentImpl := &fakeAgent{
		reply: func(context.Context, string) (string, error) {
			return "", &agent.StopError{
				Reason: agent.StopReasonToolLoopDetected,
				Detail: "tool loop detected",
			}
		},
	}

	done := make(chan error, 1)
	go func() {
		done <- runAgentWorker(ctx, messageBus, agentImpl)
	}()

	if err := messageBus.PublishInbound(ctx, bus.InboundMessage{
		Channel: bus.ChannelTelegram,
		ChatID:  "123",
		Text:    "hello",
	}); err != nil {
		t.Fatalf("PublishInbound() error = %v", err)
	}

	select {
	case out := <-messageBus.Outbound():
		want := "I stopped this run because tool calls appeared stuck in a loop. Progress was saved."
		if out.Text != want {
			t.Fatalf("outbound text = %q, want %q", out.Text, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for outbound message")
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
