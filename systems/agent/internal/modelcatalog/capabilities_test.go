package modelcatalog

import (
	"context"
	"testing"
	"time"
)

func TestCapabilityProvenancePrecedence(t *testing.T) {
	declared := Capabilities{}
	declared.Set("decision", true, "declared")
	derived := Capabilities{}
	derived.Set("decision", false, "derived")
	merged := mergeCapabilities(derived, declared)
	if merged.Decision || merged.Provenance["decision"] != "derived" {
		t.Fatalf("derived lost: %+v", merged)
	}
	merged.Set("decision", true, "override")
	merged = mergeCapabilities(merged, derived)
	if !merged.Decision || merged.Provenance["decision"] != "override" {
		t.Fatalf("override lost: %+v", merged)
	}
}

func TestCapabilityOverridesCorrectProviderClaims(t *testing.T) {
	reg := New(
		[]Provider{
			{
				Name: "api",
				CapabilityOverrides: map[string]map[string]bool{
					"model": {"decision": true, "chat": false},
				},
			},
		},
		&fakeCatalog{
			models: map[string][]Model{
				"api": {
					{
						ProviderModel: "model",
						Capabilities:  Capabilities{Chat: true}.WithProvenance("derived"),
					},
				},
			},
		},
		time.Hour,
		time.Second,
	)
	reg.Refresh(context.Background())
	model, ok := reg.Lookup("api", "model")
	if !ok || !model.Capabilities.Decision || model.Capabilities.Chat ||
		model.Capabilities.Provenance["decision"] != "override" ||
		model.Capabilities.Provenance["chat"] != "override" {
		t.Fatalf("effective capabilities = %+v", model.Capabilities)
	}
}
