package models

import (
	"context"
	"strings"
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

func TestSwitchModelSlotPersistsPerSlot(t *testing.T) {
	registry := modelcatalog.New(
		[]modelcatalog.Provider{{Name: "p", Type: "ollama"}},
		testCatalog{
			models: map[string][]modelcatalog.Model{
				"p": {
					{
						ProviderModel: "chat-model",
						Capabilities:  modelcatalog.Capabilities{Chat: true, Text: true},
					},
					{
						ProviderModel: "judge",
						Capabilities: modelcatalog.Capabilities{
							Chat:     true,
							Decision: true,
							Text:     true,
						},
					},
					{
						ProviderModel: "embedder",
						Capabilities:  modelcatalog.Capabilities{Embed: true},
					},
				},
			},
		},
		time.Hour,
		time.Second,
	)
	registry.Refresh(context.Background())
	store, path := openTestStore(t)
	selection := modelcatalog.NewSelection(registry, "p", "chat-model")
	tool := NewSwitchModel(registry, selection, store)

	// The default slot is interactive.
	if _, err := tool.Run(
		context.Background(),
		`{"provider":"p","model":"chat-model"}`,
	); err != nil {
		t.Fatal(err)
	}
	if got := store.Interactive(); got.Provider != "p" || got.Model != "chat-model" {
		t.Fatalf("interactive slot = %+v", got)
	}

	out, err := tool.Run(
		context.Background(),
		`{"provider":"p","model":"judge","slot":"decision","reason":"smarter decider"}`,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"slot": "decision"`) ||
		!strings.Contains(out, "smarter decider") {
		t.Fatalf("decision switch output = %s", out)
	}
	if got := store.Decision(); got.Provider != "p" || got.Model != "judge" {
		t.Fatalf("decision slot = %+v", got)
	}
	if sel, ok := registry.Decision(); !ok || sel.Provider != "p" || sel.Model != "judge" {
		t.Fatalf("registry decision = %+v, %v", sel, ok)
	}

	if _, err := tool.Run(
		context.Background(),
		`{"provider":"p","model":"embedder","slot":"embed"}`,
	); err != nil {
		t.Fatal(err)
	}
	if got := store.Embed(); got.Provider != "p" || got.Model != "embedder" {
		t.Fatalf("embed slot = %+v", got)
	}
	if sel, ok := registry.Embed(); !ok || sel.Model != "embedder" {
		t.Fatalf("registry embed = %+v, %v", sel, ok)
	}

	// All three slots survive a store reopen independently.
	reopened := reopenTestStore(t, path)
	if got := reopened.Interactive(); got.Model != "chat-model" {
		t.Fatalf("reopened interactive = %+v", got)
	}
	if got := reopened.Decision(); got.Model != "judge" {
		t.Fatalf("reopened decision = %+v", got)
	}
	if got := reopened.Embed(); got.Model != "embedder" {
		t.Fatalf("reopened embed = %+v", got)
	}

	// Per-slot validation: the interactive slot refuses the decision-selected
	// model; the decision slot refuses the interactive model; the embed slot
	// requires the embed capability; an unknown slot is rejected.
	if _, err := tool.Run(
		context.Background(),
		`{"provider":"p","model":"judge"}`,
	); err == nil || !strings.Contains(err.Error(), "decision model") {
		t.Fatalf("interactive accepted the decision selection: %v", err)
	}
	if _, err := tool.Run(
		context.Background(),
		`{"provider":"p","model":"chat-model","slot":"decision"}`,
	); err == nil || !strings.Contains(err.Error(), "role decision") {
		t.Fatalf("decision slot accepted the chat-only interactive model: %v", err)
	}
	if _, err := tool.Run(
		context.Background(),
		`{"provider":"p","model":"chat-model","slot":"embed"}`,
	); err == nil || !strings.Contains(err.Error(), "missing capability embed") {
		t.Fatalf("embed slot accepted a chat-only model: %v", err)
	}
	if _, err := tool.Run(
		context.Background(),
		`{"provider":"p","model":"chat-model","slot":"nonsense"}`,
	); err == nil || !strings.Contains(err.Error(), "slot must be") {
		t.Fatalf("unknown slot accepted: %v", err)
	}
}
