package models

import (
	"context"
	"testing"
	"time"

	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
)

func TestDecisionModelSwitchesDoNotPersist(t *testing.T) {
	registry := modelcatalog.New(
		[]modelcatalog.Provider{{Name: "provider", Type: "ollama"}},
		testCatalog{
			models: map[string][]modelcatalog.Model{
				"provider": {
					{ProviderModel: "chat"},
					{
						ProviderModel: "decision",
						Capabilities:  modelcatalog.Capabilities{Decision: true},
					},
				},
			},
		},
		time.Hour,
		time.Second,
	)
	registry.Refresh(context.Background())
	store, _ := openTestStore(t)
	if err := store.SetInteractive("provider", "chat"); err != nil {
		t.Fatal(err)
	}
	selection := modelcatalog.NewSelection(registry, "provider", "chat")
	if _, err := NewSwitchModel(registry, selection, store).Run(context.Background(), `{"provider":"provider","model":"decision"}`); err == nil {
		t.Fatal("decision model selected for chat")
	}
	if current := store.Interactive(); current.Model != "chat" ||
		selection.CurrentModel() != "chat" {
		t.Fatalf("interactive selection changed: %+v", current)
	}
	if _, err := NewSwitchCognitionModel(registry, store, []string{"job"}).Run(context.Background(), `{"job_type":"job","provider":"provider","model":"decision"}`); err == nil {
		t.Fatal("decision model selected for cognition chat")
	}
	if len(store.CognitionSelections()) != 0 {
		t.Fatal("refused cognition selection was persisted")
	}
}
