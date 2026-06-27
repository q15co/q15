package modelcatalog

import (
	"context"
	"testing"
	"time"
)

type selectionTestCatalog struct {
	models map[string][]Model
}

func (c selectionTestCatalog) Discover(_ context.Context, p Provider) ([]Model, error) {
	return c.models[p.Name], nil
}

func selectionTestRegistry(t *testing.T) *Registry {
	t.Helper()
	registry := New(
		[]Provider{
			{Name: "ollama-cloud", Type: "ollama", APIKey: "secret"},
			{Name: "moonshot", Type: "openai-compatible"},
		},
		selectionTestCatalog{models: map[string][]Model{
			"ollama-cloud": {
				{ProviderModel: "kimi-k2.7-code:cloud"},
				{ProviderModel: "shared-model"},
			},
			"moonshot": {
				{ProviderModel: "shared-model"},
			},
		}},
		time.Hour,
		time.Second,
	)
	registry.Refresh(context.Background())
	return registry
}

func TestSelectionSetValidatesProviderModelPair(t *testing.T) {
	selection := NewSelection(selectionTestRegistry(t), "ollama-cloud", "kimi-k2.7-code")

	if err := selection.Set("moonshot", "shared-model"); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	provider, model := selection.Current()
	if provider != "moonshot" || model != "shared-model" {
		t.Fatalf("Current() = %q/%q, want moonshot/shared-model", provider, model)
	}

	if err := selection.Set("moonshot", "kimi-k2.7-code"); err == nil {
		t.Fatal("expected missing provider/model pair to be rejected")
	}
	provider, model = selection.Current()
	if provider != "moonshot" || model != "shared-model" {
		t.Fatalf("failed Set changed selection to %q/%q", provider, model)
	}
}

func TestRegistryProvidersRedactsAPIKeys(t *testing.T) {
	providers := selectionTestRegistry(t).Providers()
	if len(providers) != 2 {
		t.Fatalf("Providers() len = %d, want 2", len(providers))
	}
	if providers[0].APIKey != "" {
		t.Fatalf("Providers()[0].APIKey = %q, want redacted", providers[0].APIKey)
	}
}
