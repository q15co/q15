package app

import (
	"context"
	"fmt"
	"sync"

	"github.com/q15co/q15/systems/agent/internal/embed"
	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
)

// slotEmbedder dispatches embedding calls through the model currently selected
// in the embed slot. The selection is runtime state, re-read on every call, so
// a switch_model slot embed takes effect without a restart. Embedders are
// cached per selected provider model; the transport (backend provider, base
// URL, credentials, dimensions) still comes from agent.tools.embeddings.
type slotEmbedder struct {
	registry *modelcatalog.Registry
	settings embed.Settings

	mu        sync.Mutex
	embedders map[string]embed.Embedder
}

func newSlotEmbedder(registry *modelcatalog.Registry, settings embed.Settings) *slotEmbedder {
	return &slotEmbedder{
		registry:  registry,
		settings:  settings,
		embedders: map[string]embed.Embedder{},
	}
}

// resolveEmbeddingSelection resolves the embed slot to a live roster model and
// rechecks its effective embed capability at dispatch.
func (e *slotEmbedder) resolveEmbeddingSelection() (modelcatalog.Model, error) {
	if e == nil || e.registry == nil {
		return modelcatalog.Model{}, fmt.Errorf("model registry is not configured")
	}
	selection, ok := e.registry.Embed()
	if !ok {
		return modelcatalog.Model{}, fmt.Errorf(
			"role embed: no model selected; use switch_model with slot embed",
		)
	}
	if m, found := e.registry.Lookup(selection.Provider, selection.Model); found {
		if !m.Capabilities.Embed {
			return modelcatalog.Model{}, fmt.Errorf(
				"role embed model %q: missing capability embed",
				m.ProviderModel,
			)
		}
		return m, nil
	}
	return modelcatalog.Model{}, fmt.Errorf(
		"role embed model %q: missing capability embed in the live catalog",
		selection.Model,
	)
}

func (e *slotEmbedder) embedderFor(
	ctx context.Context,
	m modelcatalog.Model,
) (embed.Embedder, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if cached, ok := e.embedders[m.ProviderModel]; ok {
		return cached, nil
	}
	settings := e.settings
	settings.Model = m.ProviderModel
	embedder, err := embed.NewEmbedder(ctx, settings)
	if err != nil {
		return nil, err
	}
	e.embedders[m.ProviderModel] = embedder
	return embedder, nil
}

func (e *slotEmbedder) EmbedDocuments(
	ctx context.Context,
	requests []embed.EmbeddingRequest,
) ([][]float32, error) {
	m, err := e.resolveEmbeddingSelection()
	if err != nil {
		return nil, err
	}
	embedder, err := e.embedderFor(ctx, m)
	if err != nil {
		return nil, err
	}
	return embedder.EmbedDocuments(ctx, requests)
}

func (e *slotEmbedder) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	m, err := e.resolveEmbeddingSelection()
	if err != nil {
		return nil, err
	}
	embedder, err := e.embedderFor(ctx, m)
	if err != nil {
		return nil, err
	}
	return embedder.EmbedQuery(ctx, text)
}
