package bridge

import (
	"testing"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/bus"
	"github.com/q15co/q15/systems/agent/internal/conversation"
)

func TestContinuationStreamsMainAgentReplyToOriginalSession(t *testing.T) {
	endpoint := NewAgentEndpoint(&fakePublisher{})
	original := endpoint.newSession("default")
	session, err := endpoint.OpenSession(t.Context(), bus.InboundMessage{
		Channel: bus.ChannelBridge, ChatID: "default", SessionID: original.id,
		Continuation: []conversation.Message{conversation.SystemMessage("A child finished.")},
	})
	if err != nil || session == nil {
		t.Fatalf("OpenSession = %v, %v", session, err)
	}
	session.OnRunEvent(t.Context(), agent.RunEvent{Type: agent.RunEventRunStarted, Seq: 8})
	session.OnRunEvent(
		t.Context(),
		agent.RunEvent{Type: agent.RunEventModelTurnDelta, Seq: 8, Delta: "I verified the answer."},
	)
	session.Finish(t.Context(), agent.ReplyResult{Text: "I verified the answer."})
	var terminal int
	for _, event := range original.events.retained() {
		if event.GetNotice() != nil {
			t.Fatal("main-agent answer became a notice")
		}
		if result := event.GetRunFinished(); result != nil {
			terminal++
			if result.GetFullText() != "I verified the answer." || event.GetTurnSeq() != 8 {
				t.Fatalf("wrong main-agent result: %#v", result)
			}
		}
	}
	if terminal != 1 || original.isRunning() {
		t.Fatalf("terminal events=%d running=%v", terminal, original.isRunning())
	}
}
