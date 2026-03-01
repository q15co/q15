package openaicompatible

import (
	"testing"

	"github.com/q15co/q15/systems/agent/internal/agent"
)

func TestMapMessagesIgnoresNonAssistantProviderRaw(t *testing.T) {
	messages, err := mapMessages([]agent.Message{
		{
			Role:        agent.AssistantRole,
			Content:     "hello",
			ProviderRaw: []byte(`{"id":"resp_123","output":[]}`),
		},
	})
	if err != nil {
		t.Fatalf("mapMessages error = %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(messages))
	}
	if messages[0].OfAssistant == nil {
		t.Fatalf("expected assistant fallback message, got %#v", messages[0])
	}
}
