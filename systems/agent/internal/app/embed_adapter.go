package app

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/q15co/q15/systems/agent/internal/embed"
	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
)

type guardedEmbedder struct {
	inner    embed.Embedder
	registry *modelcatalog.Registry
	binding  modelcatalog.RoleBinding
}

func resolveEmbeddingRole(
	registry *modelcatalog.Registry,
	binding modelcatalog.RoleBinding,
) (modelcatalog.Model, error) {
	for _, m := range registry.Snapshot() {
		if m.ProviderName == binding.Provider &&
			(m.Ref == binding.Model || m.ProviderModel == binding.Model) {
			if !m.Capabilities.Embed {
				return modelcatalog.Model{}, fmt.Errorf(
					"role embed model %q: missing capability embed",
					binding.Model,
				)
			}
			return m, nil
		}
	}
	return modelcatalog.Model{}, fmt.Errorf(
		"role embed model %q: missing capability embed in the live catalog",
		binding.Model,
	)
}

func (g *guardedEmbedder) authorize(request any) error {
	model, err := resolveEmbeddingRole(g.registry, g.binding)
	if err != nil {
		return err
	}
	data, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("role embed model %q: request is not JSON encodable", g.binding.Model)
	}
	return g.registry.ReserveRole("embed", model, len(data))
}

func (g *guardedEmbedder) EmbedDocuments(
	ctx context.Context,
	requests []embed.EmbeddingRequest,
) ([][]float32, error) {
	if err := g.authorize(requests); err != nil {
		return nil, err
	}
	return g.inner.EmbedDocuments(ctx, requests)
}
func (g *guardedEmbedder) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	if err := g.authorize(text); err != nil {
		return nil, err
	}
	return g.inner.EmbedQuery(ctx, text)
}
