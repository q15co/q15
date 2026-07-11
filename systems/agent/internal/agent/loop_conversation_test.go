package agent

import (
	"testing"

	"github.com/q15co/q15/systems/agent/internal/conversation"
)

func TestNormalizeUserMessageAcceptsAudioInput(t *testing.T) {
	got, err := normalizeUserMessage(conversation.UserMessageParts(
		conversation.Text("listen", ""),
		conversation.Audio(" media://sha256/abc "),
	))
	if err != nil {
		t.Fatalf("normalizeUserMessage() error = %v", err)
	}
	if len(got.Parts) != 2 {
		t.Fatalf("parts len = %d, want 2", len(got.Parts))
	}
	if got.Parts[1].Type != conversation.MediaPartType ||
		got.Parts[1].MediaKind != conversation.MediaKindAudio ||
		got.Parts[1].MediaRef != "media://sha256/abc" {
		t.Fatalf("audio part = %#v", got.Parts[1])
	}
}
