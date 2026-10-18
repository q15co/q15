package app

import (
	"context"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/agent"
	q15media "github.com/q15co/q15/systems/agent/internal/media"
	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
	"github.com/q15co/q15/systems/agent/internal/modelselection"
)

func TestDecisionOnlyModelsAreRefusedByChatSelectionAndCompletion(t *testing.T) {
	registry := testRegistry(t, map[string][]modelcatalog.Model{"provider": {
		{
			ProviderModel: "a-decision",
			Capabilities:  modelcatalog.Capabilities{Decision: true, Text: true, ToolCalling: true},
		},
		{ProviderModel: "chat", Capabilities: modelcatalog.Capabilities{Chat: true, Text: true}},
	}})
	selection := modelcatalog.NewSelection(registry, "provider", "chat")
	if err := selection.Set("provider", "a-decision"); err == nil {
		t.Error("decision model accepted as chat selection")
	}
	calls := 0
	adapter, err := newModelAdapterWithFactory(
		registry,
		nil,
		func(modelcatalog.Model, q15media.Store) (agent.ModelClient, error) {
			calls++
			return &fakeModelClient{}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := adapter.Plan(
		[]string{"a-decision", "chat"},
		modelselection.Requirements{Text: true},
	)
	if err != nil || len(plan.EligibleRefs) != 1 || plan.EligibleRefs[0] != "chat" {
		t.Errorf("plan = %+v, %v", plan, err)
	}
	if _, err := adapter.Complete(context.Background(), "a-decision", nil, nil); err == nil {
		t.Error("decision model sent to chat provider")
	}
	if calls != 0 {
		t.Errorf("chat factory called for decision model: %d", calls)
	}
}

func TestChatPortRefusesModelWithoutChat(t *testing.T) {
	registry := testRegistry(
		t,
		map[string][]modelcatalog.Model{
			"api": {
				{ProviderModel: "typed", Capabilities: modelcatalog.Capabilities{Text: true}},
			},
		},
	)
	calls := 0
	adapter, err := newModelAdapterWithFactory(
		registry,
		nil,
		func(modelcatalog.Model, q15media.Store) (agent.ModelClient, error) {
			calls++
			return &fakeModelClient{}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Complete(context.Background(), "typed", nil, nil); err == nil {
		t.Fatal("chat port accepted model without chat")
	}
	if calls != 0 {
		t.Fatal("provider called before capability guard")
	}
}
