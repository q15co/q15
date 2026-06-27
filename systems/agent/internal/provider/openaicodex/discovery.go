package openaicodex

import (
	"context"

	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
)

// RosterClient handles the openai-codex provider type. Codex access is static
// (no public model-list endpoint), so discovery always returns an empty roster.
type RosterClient struct{}

// NewRosterClient constructs a static Codex model-roster discovery adapter.
func NewRosterClient() *RosterClient { return &RosterClient{} }

// Discover returns no models and no error for the codex provider type.
func (*RosterClient) Discover(
	_ context.Context,
	_ modelcatalog.Provider,
) ([]modelcatalog.Model, error) {
	return nil, nil
}
