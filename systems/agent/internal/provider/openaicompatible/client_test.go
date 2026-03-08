package openaicompatible

import (
	"strings"
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

func TestMapMessagesIgnoresAssistantPhase(t *testing.T) {
	messages, err := mapMessages([]agent.Message{
		{
			Role:    agent.AssistantRole,
			Content: "hello",
			Phase:   "commentary",
		},
	})
	if err != nil {
		t.Fatalf("mapMessages error = %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(messages))
	}
	if messages[0].OfAssistant == nil {
		t.Fatalf("expected assistant message, got %#v", messages[0])
	}
}

func TestWithPromptProfileAppendsOpenAICompatibleProfile(t *testing.T) {
	base := []agent.Message{
		{Role: agent.SystemRole, Content: "base"},
		{Role: agent.UserRole, Content: "hello"},
	}

	tuned := withPromptProfile(base)
	if len(tuned) != len(base)+1 {
		t.Fatalf("tuned len = %d, want %d", len(tuned), len(base)+1)
	}
	last := tuned[len(tuned)-1]
	if last.Role != agent.SystemRole {
		t.Fatalf("last role = %q, want system", last.Role)
	}
	for _, want := range []string{
		`provider="openai-compatible"`,
		"does not round-trip assistant commentary phase metadata",
	} {
		if !strings.Contains(last.Content, want) {
			t.Fatalf("profile missing %q:\n%s", want, last.Content)
		}
	}
	if strings.Contains(last.Content, `model="`) {
		t.Fatalf("profile should not depend on model name:\n%s", last.Content)
	}
}
