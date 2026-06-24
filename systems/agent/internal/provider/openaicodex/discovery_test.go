package openaicodex

import (
	"context"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
)

func TestRosterClient_EmptyRoster(t *testing.T) {
	client := NewRosterClient()
	models, err := client.Discover(
		context.Background(),
		modelcatalog.Provider{Type: "openai-codex"},
	)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(models) != 0 {
		t.Fatalf("expected 0 models from codex, got %d", len(models))
	}
}
