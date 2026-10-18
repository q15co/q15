package app

import (
	"context"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/embed"
	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
)

type recordingEmbedder struct{ calls int }

func (e *recordingEmbedder) EmbedQuery(context.Context, string) ([]float32, error) {
	e.calls++
	return []float32{1}, nil
}

func (e *recordingEmbedder) EmbedDocuments(
	context.Context,
	[]embed.EmbeddingRequest,
) ([][]float32, error) {
	e.calls++
	return [][]float32{{1}}, nil
}

// Embed dispatch needs a runtime embed selection; without one it fails closed
// with the switch_model guidance instead of reaching a provider.
func TestEmbedDispatchRequiresSelectedEmbedModel(t *testing.T) {
	registry := testRegistry(t, map[string][]modelcatalog.Model{
		"api": {{ProviderModel: "vector", Capabilities: modelcatalog.Capabilities{Embed: true}}},
	})
	embedder := newSlotEmbedder(registry, embed.Settings{})

	_, err := embedder.EmbedQuery(context.Background(), "context")
	if err == nil ||
		!strings.Contains(err.Error(), "role embed: no model selected") ||
		!strings.Contains(err.Error(), "switch_model with slot embed") {
		t.Fatalf("empty embed slot dispatch = %v", err)
	}
	if _, err := embedder.EmbedDocuments(
		context.Background(),
		[]embed.EmbeddingRequest{{Text: "context"}},
	); err == nil || !strings.Contains(err.Error(), "role embed: no model selected") {
		t.Fatalf("empty embed slot document dispatch = %v", err)
	}
}

// The embed slot is re-resolved per dispatch: a roster model losing its embed
// capability stops dispatch, and the dispatch-time check happens before any
// provider call.
func TestEmbedDispatchRechecksCapability(t *testing.T) {
	models := map[string][]modelcatalog.Model{
		"api": {{ProviderModel: "vector", Capabilities: modelcatalog.Capabilities{Embed: true}}},
	}
	registry := testRegistry(t, models)
	registry.SetEmbed(modelcatalog.RoleSelection{Provider: "api", Model: "vector"})
	inner := &recordingEmbedder{}
	embedder := newSlotEmbedder(registry, embed.Settings{})
	embedder.embedders["vector"] = inner

	if _, err := embedder.EmbedQuery(context.Background(), "context"); err != nil {
		t.Fatal(err)
	}

	models["api"][0].Capabilities.Embed = false
	registry.Refresh(context.Background())
	if _, err := embedder.EmbedQuery(context.Background(), "context"); err == nil ||
		!strings.Contains(err.Error(), "missing capability embed") {
		t.Fatalf("missing embed capability accepted: %v", err)
	}
	if _, err := embedder.EmbedDocuments(
		context.Background(),
		[]embed.EmbeddingRequest{{Text: "context"}},
	); err == nil {
		t.Fatal("document embedding bypassed the guard")
	}
	if inner.calls != 1 {
		t.Fatalf("embedding calls = %d, want 1", inner.calls)
	}

	// A model removed from the roster fails closed too.
	models["api"] = nil
	registry.Refresh(context.Background())
	if _, err := embedder.EmbedQuery(context.Background(), "context"); err == nil ||
		!strings.Contains(err.Error(), "in the live catalog") {
		t.Fatalf("removed embed model dispatched: %v", err)
	}
}
