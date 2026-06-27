package app

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	q15media "github.com/q15co/q15/systems/agent/internal/media"
	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
	"github.com/q15co/q15/systems/agent/internal/modelselection"
	"github.com/q15co/q15/systems/agent/internal/selectionstore"
)

// --- shared model test doubles (reused by runtime_entrypoints_test.go) ---

// fakeModelClient records each Complete call and returns queued results in
// order, defaulting to an "ok" assistant reply when no results are queued.
type fakeModelClient struct {
	calls   []fakeModelCall
	results []agent.ModelClientResult
}

type fakeModelCall struct {
	model    string
	tools    []agent.ToolDefinition
	messages []conversation.Message
}

func (f *fakeModelClient) Complete(
	_ context.Context,
	model string,
	messages []conversation.Message,
	tools []agent.ToolDefinition,
) (agent.ModelClientResult, error) {
	call := fakeModelCall{
		model:    model,
		messages: conversation.CloneMessages(messages),
	}
	if len(tools) > 0 {
		call.tools = append([]agent.ToolDefinition(nil), tools...)
	}
	f.calls = append(f.calls, call)
	if len(f.results) > 0 {
		result := f.results[0]
		f.results = f.results[1:]
		return result, nil
	}
	return agent.ModelClientResult{
		Messages: []conversation.Message{
			conversation.AssistantMessage(conversation.Text("ok", "")),
		},
	}, nil
}

type mockCatalog struct {
	models map[string][]modelcatalog.Model
}

func (m *mockCatalog) Discover(
	_ context.Context,
	p modelcatalog.Provider,
) ([]modelcatalog.Model, error) {
	return m.models[p.Name], nil
}

// testRegistry builds a registry from provider→models and refreshes it once.
func testRegistry(
	t *testing.T,
	providerModels map[string][]modelcatalog.Model,
) *modelcatalog.Registry {
	t.Helper()
	providers := make([]modelcatalog.Provider, 0, len(providerModels))
	for name, ms := range providerModels {
		ptype := "ollama"
		if len(ms) > 0 && ms[0].ProviderType != "" {
			ptype = ms[0].ProviderType
		}
		providers = append(providers, modelcatalog.Provider{
			Name: name,
			Type: ptype,
		})
	}
	reg := modelcatalog.New(providers, &mockCatalog{models: providerModels}, time.Hour, time.Second)
	reg.Refresh(context.Background())
	return reg
}

func openTestSelectionStore(t *testing.T) *selectionstore.Store {
	t.Helper()
	store, err := selectionstore.Open(filepath.Join(t.TempDir(), "selection.json"))
	if err != nil {
		t.Fatalf("selectionstore.Open() error = %v", err)
	}
	return store
}

// --- routed model adapter tests ---

func TestRoutedModelAdapterRoutesModelsAndSuppressesTools(t *testing.T) {
	clients := make(map[string]*fakeModelClient)
	factoryCalls := make(map[string]int)

	registry := testRegistry(t, map[string][]modelcatalog.Model{
		"moonshot": {
			{ProviderModel: "kimi-k2.5", Capabilities: modelcatalog.Capabilities{Text: true}},
			{
				ProviderModel: "kimi-k2",
				Capabilities:  modelcatalog.Capabilities{Text: true, ToolCalling: true},
			},
		},
		"openai-sub": {
			{
				ProviderModel: "gpt-5-codex",
				Capabilities:  modelcatalog.Capabilities{Text: true, ToolCalling: true},
			},
		},
	})

	adapter, err := newModelAdapterWithFactory(
		registry,
		nil,
		func(m modelcatalog.Model, _ q15media.Store) (agent.ModelClient, error) {
			factoryCalls[m.ProviderName]++
			client := &fakeModelClient{}
			clients[m.ProviderName] = client
			return client, nil
		},
	)
	if err != nil {
		t.Fatalf("newModelAdapterWithFactory() error = %v", err)
	}

	tools := []agent.ToolDefinition{{Name: "shell"}}

	// primary: text-only → tools suppressed
	if _, err := adapter.Complete(context.Background(), "kimi-k2.5", nil, tools); err != nil {
		t.Fatalf("Complete(kimi-k2.5) error = %v", err)
	}
	if got := factoryCalls["moonshot"]; got != 1 {
		t.Fatalf("factoryCalls[moonshot] = %d, want 1", got)
	}
	if len(clients["moonshot"].calls) != 1 {
		t.Fatalf("moonshot calls = %d, want 1", len(clients["moonshot"].calls))
	}
	if len(clients["moonshot"].calls[0].tools) != 0 {
		t.Fatalf("tools not suppressed for text-only model")
	}

	// secondary: tool-capable → tools passed through
	if _, err := adapter.Complete(context.Background(), "kimi-k2", nil, tools); err != nil {
		t.Fatalf("Complete(kimi-k2) error = %v", err)
	}
	if got := factoryCalls["moonshot"]; got != 1 {
		t.Fatalf("factoryCalls[moonshot] after second = %d, want 1 (cached)", got)
	}
	if len(clients["moonshot"].calls[1].tools) != 1 {
		t.Fatalf("tools not passed for tool-capable model")
	}

	// backup: different provider
	if _, err := adapter.Complete(context.Background(), "gpt-5-codex", nil, tools); err != nil {
		t.Fatalf("Complete(gpt-5-codex) error = %v", err)
	}
	if got := factoryCalls["openai-sub"]; got != 1 {
		t.Fatalf("factoryCalls[openai-sub] = %d, want 1", got)
	}
}

func TestRoutedModelAdapterUsesSelectionToDisambiguateCurrentRef(t *testing.T) {
	registry := modelcatalog.New(
		[]modelcatalog.Provider{{Name: "p1", Type: "ollama"}, {Name: "p2", Type: "ollama"}},
		&mockCatalog{models: map[string][]modelcatalog.Model{
			"p1": {{ProviderModel: "shared", Capabilities: modelcatalog.Capabilities{Text: true}}},
			"p2": {
				{
					ProviderModel: "shared",
					Capabilities:  modelcatalog.Capabilities{Text: true, ToolCalling: true},
				},
			},
		}},
		time.Hour,
		time.Second,
	)
	registry.Refresh(context.Background())
	selection := modelcatalog.NewSelection(registry, "p2", "shared")
	var routedProvider string

	adapter, err := newModelAdapterWithSelectionAndFactory(
		registry,
		selection,
		nil,
		func(m modelcatalog.Model, _ q15media.Store) (agent.ModelClient, error) {
			routedProvider = m.ProviderName
			return &fakeModelClient{}, nil
		},
	)
	if err != nil {
		t.Fatalf("newModelAdapterWithSelectionAndFactory() error = %v", err)
	}
	if _, err := adapter.Complete(context.Background(), "shared", nil, nil); err != nil {
		t.Fatalf("Complete(shared) error = %v", err)
	}
	if routedProvider != "p2" {
		t.Fatalf("routed provider = %q, want p2", routedProvider)
	}

	plan, err := adapter.Plan(
		[]string{"shared"},
		modelselection.Requirements{Text: true, ToolCalling: true},
	)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if len(plan.EligibleRefs) != 1 || plan.EligibleRefs[0] != "shared" {
		t.Fatalf("eligible = %#v, want [shared]", plan.EligibleRefs)
	}
}

func TestRoutedModelAdapterPlanFiltersByCapabilities(t *testing.T) {
	registry := testRegistry(t, map[string][]modelcatalog.Model{
		"p": {
			{ProviderModel: "text-only", Capabilities: modelcatalog.Capabilities{Text: true}},
			{
				ProviderModel: "tools",
				Capabilities:  modelcatalog.Capabilities{Text: true, ToolCalling: true},
			},
		},
	})

	adapter, err := newModelAdapterWithFactory(
		registry,
		nil,
		func(_ modelcatalog.Model, _ q15media.Store) (agent.ModelClient, error) {
			return &fakeModelClient{}, nil
		},
	)
	if err != nil {
		t.Fatalf("newModelAdapterWithFactory() error = %v", err)
	}

	plan, err := adapter.Plan(
		[]string{"text-only", "tools"},
		modelselection.Requirements{Text: true, ToolCalling: true},
	)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if len(plan.EligibleRefs) != 1 || plan.EligibleRefs[0] != "tools" {
		t.Fatalf("eligible = %#v, want [tools]", plan.EligibleRefs)
	}
}

func TestRoutedModelAdapterPlanSkipsMissingFromRoster(t *testing.T) {
	registry := testRegistry(t, map[string][]modelcatalog.Model{
		"p": {
			{ProviderModel: "real-model", Capabilities: modelcatalog.Capabilities{Text: true}},
		},
	})

	adapter, err := newModelAdapterWithFactory(
		registry,
		nil,
		func(_ modelcatalog.Model, _ q15media.Store) (agent.ModelClient, error) {
			return &fakeModelClient{}, nil
		},
	)
	if err != nil {
		t.Fatalf("newModelAdapterWithFactory() error = %v", err)
	}

	plan, err := adapter.Plan(
		[]string{"real-model", "gone-model"},
		modelselection.Requirements{Text: true},
	)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if len(plan.EligibleRefs) != 1 || plan.EligibleRefs[0] != "real-model" {
		t.Fatalf("eligible = %#v, want [real-model]", plan.EligibleRefs)
	}
	if len(plan.Skipped) != 1 || plan.Skipped[0].Ref != "gone-model" {
		t.Fatalf("skipped = %#v, want [gone-model]", plan.Skipped)
	}
}

func TestRoutedModelAdapterCompleteRejectsMissingModel(t *testing.T) {
	registry := testRegistry(t, map[string][]modelcatalog.Model{
		"p": {
			{ProviderModel: "real-model", Capabilities: modelcatalog.Capabilities{Text: true}},
		},
	})

	adapter, err := newModelAdapterWithFactory(
		registry,
		nil,
		func(_ modelcatalog.Model, _ q15media.Store) (agent.ModelClient, error) {
			return &fakeModelClient{}, nil
		},
	)
	if err != nil {
		t.Fatalf("newModelAdapterWithFactory() error = %v", err)
	}

	if _, err := adapter.Complete(context.Background(), "nonexistent", nil, nil); err == nil {
		t.Fatal("expected error for model not in roster")
	}
}

func TestRoutedModelAdapterAdaptsMediaPerModelCapabilities(t *testing.T) {
	store := q15media.Store(nil)
	registry := testRegistry(t, map[string][]modelcatalog.Model{
		"p": {
			{ProviderModel: "text-only", Capabilities: modelcatalog.Capabilities{Text: true}},
			{
				ProviderModel: "vision",
				Capabilities:  modelcatalog.Capabilities{Text: true, ImageInput: true},
			},
		},
	})

	adapter, err := newModelAdapterWithFactory(
		registry,
		store,
		func(_ modelcatalog.Model, _ q15media.Store) (agent.ModelClient, error) {
			return &fakeModelClient{}, nil
		},
	)
	if err != nil {
		t.Fatalf("newModelAdapterWithFactory() error = %v", err)
	}

	// Both calls should succeed without panic — media adaptation is internal.
	if _, err := adapter.Complete(context.Background(), "text-only", nil, nil); err != nil {
		t.Fatalf("Complete(text-only) error = %v", err)
	}
	if _, err := adapter.Complete(context.Background(), "vision", nil, nil); err != nil {
		t.Fatalf("Complete(vision) error = %v", err)
	}
}

func TestBuildModelRefsCurrentFirst(t *testing.T) {
	registry := testRegistry(t, map[string][]modelcatalog.Model{
		"p": {
			{ProviderModel: "alpha"},
			{ProviderModel: "beta"},
			{ProviderModel: "gamma"},
		},
	})

	refs := buildModelRefs("beta", registry)
	if len(refs) != 3 {
		t.Fatalf("refs = %v, want 3", refs)
	}
	if refs[0] != "beta" {
		t.Fatalf("refs[0] = %q, want beta (current first)", refs[0])
	}
}

func TestBuildModelRefsCurrentNotInSnapshot(t *testing.T) {
	registry := testRegistry(t, map[string][]modelcatalog.Model{
		"p": {
			{ProviderModel: "alpha"},
			{ProviderModel: "beta"},
		},
	})

	// Current model gone from roster → still first, rest follow.
	refs := buildModelRefs("gone", registry)
	if len(refs) != 3 {
		t.Fatalf("refs = %v, want 3 (gone + alpha + beta)", refs)
	}
	if refs[0] != "gone" {
		t.Fatalf("refs[0] = %q, want gone (current first even if missing)", refs[0])
	}
}

// --- selection bootstrap tests ---

func TestAutoSelectModelPrefersToolCallingThenText(t *testing.T) {
	registry := testRegistry(t, map[string][]modelcatalog.Model{
		"p": {{
			ProviderModel: "text-only",
			Capabilities:  modelcatalog.Capabilities{Text: true},
		}},
		"q": {{
			ProviderModel: "tools",
			Capabilities:  modelcatalog.Capabilities{Text: true, ToolCalling: true},
		}},
	})
	provider, model, ok := autoSelectModel(registry)
	if !ok {
		t.Fatal("autoSelectModel() ok=false, want true")
	}
	if provider != "q" || model != "tools" {
		t.Fatalf("autoSelectModel() = %q/%q, want q/tools", provider, model)
	}
}

func TestLoadInteractiveSelectionAutoSelectsAndPersistsOnFirstRun(t *testing.T) {
	registry := testRegistry(t, map[string][]modelcatalog.Model{
		"p": {
			{
				ProviderModel: "alpha",
				Capabilities:  modelcatalog.Capabilities{Text: true, ToolCalling: true},
			},
		},
	})
	storePath := filepath.Join(t.TempDir(), "selection.json")
	store1, err := selectionstore.Open(storePath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	selection, err := loadInteractiveSelection(registry, store1)
	if err != nil {
		t.Fatalf("loadInteractiveSelection() error = %v", err)
	}
	if provider, model := selection.Current(); provider != "p" || model != "alpha" {
		t.Fatalf("Current() = %q/%q, want p/alpha", provider, model)
	}
	if got := store1.Interactive(); got.Provider != "p" || got.Model != "alpha" {
		t.Fatalf("persisted interactive = %+v, want p/alpha", got)
	}

	store2, err := selectionstore.Open(storePath)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	if got := store2.Interactive(); got.Provider != "p" || got.Model != "alpha" {
		t.Fatalf("reopened interactive = %+v, want persisted p/alpha", got)
	}
}

func TestSwitchCognitionModelPersistsAndResolverFallsBack(t *testing.T) {
	registry := testRegistry(t, map[string][]modelcatalog.Model{
		"p": {{ProviderModel: "alpha", Capabilities: modelcatalog.Capabilities{Text: true}}},
		"q": {{ProviderModel: "beta", Capabilities: modelcatalog.Capabilities{Text: true}}},
	})
	selection := modelcatalog.NewSelection(registry, "p", "alpha")
	store := openTestSelectionStore(t)

	resolver := func(jobType string) []string {
		if override, ok := store.Cognition(jobType); ok {
			return buildModelRefs(override.Model, registry)
		}
		return buildModelRefs(selection.CurrentModel(), registry)
	}
	if refs := resolver("working_memory.consolidate"); len(refs) == 0 || refs[0] != "alpha" {
		t.Fatalf("fallback resolver refs = %v, want alpha first", refs)
	}

	if err := store.SetCognition("working_memory.consolidate", "q", "beta"); err != nil {
		t.Fatalf("SetCognition() error = %v", err)
	}
	if refs := resolver("working_memory.consolidate"); len(refs) == 0 || refs[0] != "beta" {
		t.Fatalf("override resolver refs = %v, want beta first", refs)
	}
}
