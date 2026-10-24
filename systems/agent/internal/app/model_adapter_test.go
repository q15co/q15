package app

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/decision"
	q15media "github.com/q15co/q15/systems/agent/internal/media"
	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
	"github.com/q15co/q15/systems/agent/internal/modelselection"
	"github.com/q15co/q15/systems/agent/internal/selectionstore"
	modeltools "github.com/q15co/q15/systems/agent/internal/tools/models"
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

func TestRoutedModelAdapterUsesReloadedProviderSettings(t *testing.T) {
	ctx := context.Background()
	watcher, registry, _, path := reloadFixture(t)
	var created []modelcatalog.Model
	adapter, err := newModelAdapterWithFactory(registry, nil,
		func(m modelcatalog.Model, _ q15media.Store) (agent.ModelClient, error) {
			created = append(created, m)
			return &fakeModelClient{}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	bound, err := adapter.BindProviderModel("local", "first")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Complete(ctx, "first", nil, nil); err != nil {
		t.Fatal(err)
	}
	writeConfigFile(t, path, "http://two.test:11434")
	watcher.tick(ctx)
	if _, err := adapter.Complete(ctx, "second", nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(created) != 2 || created[1].ProviderBaseURL != "http://two.test:11434" {
		t.Fatalf(
			"client configurations = %+v, want a new client for the reloaded endpoint",
			created,
		)
	}
	if _, err := bound.Complete(ctx, "first", nil, nil); err == nil {
		t.Fatal("bound target removed by reload was still dispatched")
	}
	if len(created) != 2 {
		t.Fatalf("bound client recreated: %d factories, want 2", len(created))
	}
}

func TestRoutedModelAdapterRoutesModelsAndSuppressesTools(t *testing.T) {
	clients := make(map[string]*fakeModelClient)
	factoryCalls := make(map[string]int)

	registry := testRegistry(t, map[string][]modelcatalog.Model{
		"moonshot": {
			{
				ProviderModel: "kimi-k2.5",
				Capabilities:  modelcatalog.Capabilities{Chat: true, Text: true},
			},
			{
				ProviderModel: "kimi-k2",
				Capabilities:  modelcatalog.Capabilities{Chat: true, Text: true, ToolCalling: true},
			},
		},
		"openai-sub": {
			{
				ProviderModel: "gpt-5-codex",
				Capabilities:  modelcatalog.Capabilities{Chat: true, Text: true, ToolCalling: true},
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
			"p1": {
				{
					ProviderModel: "shared",
					Capabilities:  modelcatalog.Capabilities{Chat: true, Text: true},
				},
			},
			"p2": {
				{
					ProviderModel: "shared",
					Capabilities: modelcatalog.Capabilities{
						Chat:        true,
						Text:        true,
						ToolCalling: true,
					},
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

func TestRoutedModelAdapterBindProviderModelUsesExactProvider(t *testing.T) {
	registry := modelcatalog.New(
		[]modelcatalog.Provider{{Name: "p1", Type: "ollama"}, {Name: "p2", Type: "ollama"}},
		&mockCatalog{models: map[string][]modelcatalog.Model{
			"p1": {
				{
					ProviderModel: "shared",
					Capabilities:  modelcatalog.Capabilities{Chat: true, Text: true},
				},
			},
			"p2": {
				{
					ProviderModel: "shared:cloud",
					Capabilities: modelcatalog.Capabilities{
						Chat:        true,
						Text:        true,
						ToolCalling: true,
					},
				},
			},
		}},
		time.Hour,
		time.Second,
	)
	registry.Refresh(context.Background())
	selection := modelcatalog.NewSelection(registry, "p1", "shared")
	clients := make(map[string]*fakeModelClient)

	adapter, err := newModelAdapterWithSelectionAndFactory(
		registry,
		selection,
		nil,
		func(m modelcatalog.Model, _ q15media.Store) (agent.ModelClient, error) {
			client := &fakeModelClient{}
			clients[m.ProviderName] = client
			return client, nil
		},
	)
	if err != nil {
		t.Fatalf("newModelAdapterWithSelectionAndFactory() error = %v", err)
	}

	tools := []agent.ToolDefinition{{Name: "shell"}}
	client, err := adapter.BindProviderModel(
		" p2 ",
		" shared ",
	)
	if err != nil {
		t.Fatalf("BindProviderModel(p2, shared) error = %v", err)
	}
	if _, err := client.Complete(context.Background(), "shared", nil, tools); err != nil {
		t.Fatalf("bound Complete(shared) error = %v", err)
	}
	if _, ok := clients["p1"]; ok {
		t.Fatal("provider p1 client was created despite exact p2 target")
	}
	p2Client, ok := clients["p2"]
	if !ok {
		t.Fatal("provider p2 client was not created")
	}
	if len(p2Client.calls) != 1 {
		t.Fatalf("p2 calls = %d, want 1", len(p2Client.calls))
	}
	if got := p2Client.calls[0].model; got != "shared:cloud" {
		t.Fatalf("provider model = %q, want shared:cloud", got)
	}
	if got := len(p2Client.calls[0].tools); got != 1 {
		t.Fatalf("tools = %d, want 1 for p2 tool-capable model", got)
	}
}

func TestRoutedModelAdapterBindProviderModelRejectsMissingExactTarget(t *testing.T) {
	registry := testRegistry(t, map[string][]modelcatalog.Model{
		"p1": {
			{
				ProviderModel: "shared",
				Capabilities:  modelcatalog.Capabilities{Chat: true, Text: true},
			},
		},
		"p2": {
			{
				ProviderModel: "other",
				Capabilities:  modelcatalog.Capabilities{Chat: true, Text: true},
			},
		},
	})
	factoryCalls := 0
	adapter, err := newModelAdapterWithFactory(
		registry,
		nil,
		func(_ modelcatalog.Model, _ q15media.Store) (agent.ModelClient, error) {
			factoryCalls++
			return &fakeModelClient{}, nil
		},
	)
	if err != nil {
		t.Fatalf("newModelAdapterWithFactory() error = %v", err)
	}

	_, err = adapter.BindProviderModel("p2", "shared")
	if err == nil {
		t.Fatal("expected error for missing exact provider/model target")
	}
	if !errors.Is(err, errProviderModelUnavailable) {
		t.Fatalf("error = %v, want errProviderModelUnavailable", err)
	}
	if factoryCalls != 0 {
		t.Fatalf("factory calls = %d, want 0 (must not fall back to p1)", factoryCalls)
	}
}

func TestBoundProviderModelRefusesRemovedTargetWithoutFallback(t *testing.T) {
	catalog := &mockCatalog{models: map[string][]modelcatalog.Model{
		"p1": {
			{
				ProviderModel: "shared",
				Capabilities:  modelcatalog.Capabilities{Chat: true, Text: true},
			},
		},
		"p2": {
			{
				ProviderModel: "shared:cloud",
				Capabilities:  modelcatalog.Capabilities{Chat: true, Text: true},
			},
		},
	}}
	registry := modelcatalog.New(
		[]modelcatalog.Provider{{Name: "p1", Type: "ollama"}, {Name: "p2", Type: "ollama"}},
		catalog,
		time.Hour,
		time.Second,
	)
	registry.Refresh(context.Background())

	var routedProvider string
	adapter, err := newModelAdapterWithFactory(
		registry,
		nil,
		func(model modelcatalog.Model, _ q15media.Store) (agent.ModelClient, error) {
			routedProvider = model.ProviderName
			return &fakeModelClient{}, nil
		},
	)
	if err != nil {
		t.Fatalf("newModelAdapterWithFactory() error = %v", err)
	}
	bound, err := adapter.BindProviderModel("p2", "shared")
	if err != nil {
		t.Fatalf("BindProviderModel(p2, shared) error = %v", err)
	}

	catalog.models["p2"] = nil
	registry.Refresh(context.Background())

	if _, err := bound.Complete(context.Background(), "shared", nil, nil); err == nil {
		t.Fatal("bound completion accepted a removed target")
	}
	if routedProvider != "" {
		t.Fatalf("bound completion reached provider %q", routedProvider)
	}
	if _, err := adapter.BindProviderModel("p2", "shared"); !errors.Is(
		err,
		errProviderModelUnavailable,
	) {
		t.Fatalf("new BindProviderModel() error = %v, want unavailable", err)
	}
}

func TestRoutedModelAdapterPlanFiltersByCapabilities(t *testing.T) {
	registry := testRegistry(t, map[string][]modelcatalog.Model{
		"p": {
			{
				ProviderModel: "text-only",
				Capabilities:  modelcatalog.Capabilities{Chat: true, Text: true},
			},
			{
				ProviderModel: "tools",
				Capabilities:  modelcatalog.Capabilities{Chat: true, Text: true, ToolCalling: true},
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
			{
				ProviderModel: "real-model",
				Capabilities:  modelcatalog.Capabilities{Chat: true, Text: true},
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
			{
				ProviderModel: "real-model",
				Capabilities:  modelcatalog.Capabilities{Chat: true, Text: true},
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

	if _, err := adapter.Complete(context.Background(), "nonexistent", nil, nil); err == nil {
		t.Fatal("expected error for model not in roster")
	}
}

func TestRoutedModelAdapterAdaptsMediaPerModelCapabilities(t *testing.T) {
	store := q15media.Store(nil)
	registry := testRegistry(t, map[string][]modelcatalog.Model{
		"p": {
			{
				ProviderModel: "text-only",
				Capabilities:  modelcatalog.Capabilities{Chat: true, Text: true},
			},
			{
				ProviderModel: "vision",
				Capabilities:  modelcatalog.Capabilities{Chat: true, Text: true, ImageInput: true},
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
			{ProviderModel: "alpha", Capabilities: modelcatalog.Capabilities{Chat: true}},
			{ProviderModel: "beta", Capabilities: modelcatalog.Capabilities{Chat: true}},
			{ProviderModel: "gamma", Capabilities: modelcatalog.Capabilities{Chat: true}},
		},
	})

	refs := buildModelRefs("beta", registry)
	if len(refs) != 3 {
		t.Fatalf("refs = %v, want 3", refs)
	}
	if refs[0] != "beta" {
		t.Fatalf("refs[0] = %q, want beta (current first)", refs[0])
	}
	if refs[1] != "alpha" || refs[2] != "gamma" {
		t.Fatalf("refs = %v, want roster order after the current ref", refs)
	}
}

func TestBuildModelRefsCurrentNotInSnapshot(t *testing.T) {
	registry := testRegistry(t, map[string][]modelcatalog.Model{
		"p": {
			{ProviderModel: "alpha", Capabilities: modelcatalog.Capabilities{Chat: true}},
			{ProviderModel: "beta", Capabilities: modelcatalog.Capabilities{Chat: true}},
		},
	})

	// Current model gone from roster → still first, chat-capable roster follows.
	refs := buildModelRefs("gone", registry)
	if len(refs) != 3 {
		t.Fatalf("refs = %v, want 3 (gone + alpha + beta)", refs)
	}
	if refs[0] != "gone" {
		t.Fatalf("refs[0] = %q, want gone (current first even if missing)", refs[0])
	}
}

// Chat dispatch excludes models without the chat capability, including deciders.
func TestFallbackChainSkipsNonChatModels(t *testing.T) {
	registry := testRegistry(t, map[string][]modelcatalog.Model{
		"p": {
			{
				ProviderModel: "chatty",
				Capabilities:  modelcatalog.Capabilities{Chat: true, Text: true, ToolCalling: true},
			},
			{
				ProviderModel: "decider",
				Capabilities:  modelcatalog.Capabilities{Decision: true, Text: true},
			},
			{ProviderModel: "silent", Capabilities: modelcatalog.Capabilities{Text: true}},
			{
				ProviderModel: "helper",
				Capabilities:  modelcatalog.Capabilities{Chat: true, Text: true},
			},
		},
	})
	registry.SetDecision(modelcatalog.RoleSelection{Provider: "p", Model: "decider"})

	refs := buildModelRefs("chatty", registry)
	if len(refs) != 2 || refs[0] != "chatty" || refs[1] != "helper" {
		t.Fatalf("fallback refs = %v, want [chatty helper]", refs)
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
		[]string{"chatty", "decider", "silent", "helper"},
		modelselection.Requirements{Text: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.EligibleRefs) != 2 ||
		plan.EligibleRefs[0] != "chatty" || plan.EligibleRefs[1] != "helper" {
		t.Fatalf("eligible = %#v", plan.EligibleRefs)
	}
	if len(plan.Skipped) != 2 {
		t.Fatalf("skipped = %#v", plan.Skipped)
	}
	if !strings.Contains(plan.Skipped[0].Reason, "missing capability chat") {
		t.Fatalf("decision skip reason = %q", plan.Skipped[0].Reason)
	}
	if !strings.Contains(plan.Skipped[1].Reason, "missing capability chat") {
		t.Fatalf("non-chat skip reason = %q", plan.Skipped[1].Reason)
	}
	if _, err := adapter.Complete(context.Background(), "decider", nil, nil); err == nil ||
		!strings.Contains(err.Error(), "missing capability chat") {
		t.Fatalf("dispatch to the decision selection = %v", err)
	}
	if calls != 0 {
		t.Fatalf("decision-selected model reached the provider: %d calls", calls)
	}
}

// --- selection bootstrap tests ---

// The interactive slot auto-selects the first roster model with Text and
// ToolCalling ahead of a plainer chat model, and persists the choice.
func TestAutoSelectPrefersTextAndToolCalling(t *testing.T) {
	registry := testRegistry(t, map[string][]modelcatalog.Model{
		"p": {
			{ProviderModel: "plain", Capabilities: modelcatalog.Capabilities{Chat: true}},
			{
				ProviderModel: "equipped",
				Capabilities:  modelcatalog.Capabilities{Chat: true, Text: true, ToolCalling: true},
			},
		},
	})
	store := openTestSelectionStore(t)
	selection, err := loadRuntimeSelections(registry, store)
	if err != nil {
		t.Fatal(err)
	}
	if provider, model := selection.Current(); provider != "p" || model != "equipped" {
		t.Fatalf("Current() = %q/%q, want p/equipped", provider, model)
	}
	if got := store.Interactive(); got.Provider != "p" || got.Model != "equipped" {
		t.Fatalf("persisted interactive = %+v, want p/equipped", got)
	}
}

// Text input and tool metadata do not make a decision-only model eligible for chat.
func TestAutoSelectSkipsDecisionOnlyModels(t *testing.T) {
	registry := testRegistry(t, map[string][]modelcatalog.Model{
		"p": {
			{
				ProviderModel: "decider",
				Capabilities: modelcatalog.Capabilities{
					Decision: true, Text: true, ToolCalling: true,
				},
			},
			{
				ProviderModel: "chatter",
				Capabilities:  modelcatalog.Capabilities{Chat: true, Text: true, ToolCalling: true},
			},
		},
	})
	store := openTestSelectionStore(t)
	if err := store.SetDecision("p", "decider"); err != nil {
		t.Fatal(err)
	}
	selection, err := loadRuntimeSelections(registry, store)
	if err != nil {
		t.Fatal(err)
	}
	if provider, model := selection.Current(); provider != "p" || model != "chatter" {
		t.Fatalf("Current() = %q/%q, want p/chatter", provider, model)
	}
	if refs := buildModelRefs(selection.CurrentModel(), registry); len(refs) != 1 {
		t.Fatalf("fallback chain = %v, want only the interactive model", refs)
	}
	// Decision dispatch still routes to the decider.
	adapter := newRoutedDecisionAdapter(
		registry,
		func(modelcatalog.Model) (decision.Client, error) { return &fakeDecisionClient{}, nil },
	)
	if _, err := adapter.Check(context.Background(), decision.Request{
		State:     "context",
		Questions: map[string]decision.Question{"check": decision.Noul("True?")},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDecisionSelectionAllowsModelSupportingBothOperations(t *testing.T) {
	registry := testRegistry(t, map[string][]modelcatalog.Model{
		"p": {
			{
				ProviderModel: "both",
				Capabilities:  modelcatalog.Capabilities{Chat: true, Decision: true, Text: true},
			},
		},
	})
	store := openTestSelectionStore(t)
	if err := store.SetInteractive("p", "both"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetDecision("p", "both"); err != nil {
		t.Fatal(err)
	}
	selection, err := loadRuntimeSelections(registry, store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := modeltools.NewSwitchModel(registry, selection, store).Run(t.Context(), `{"provider":"p","model":"both","slot":"decision"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := modeltools.NewSwitchModel(registry, selection, store).Run(t.Context(), `{"provider":"p","model":"both"}`); err != nil {
		t.Fatal(err)
	}
	if got := store.Decision(); got.Provider != "p" || got.Model != "both" {
		t.Fatalf("decision = %+v", got)
	}
}

func TestLoadInteractiveSelectionRestoresPersistedSelection(t *testing.T) {
	registry := testRegistry(t, map[string][]modelcatalog.Model{
		"p": {
			{
				ProviderModel: "alpha",
				Capabilities:  modelcatalog.Capabilities{Chat: true, Text: true, ToolCalling: true},
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

func TestRoutedModelAdapterCacheIncludesProviderConnectionSettings(t *testing.T) {
	tests := []struct {
		name   string
		change func(*modelcatalog.Model)
	}{
		{"name", func(m *modelcatalog.Model) { m.ProviderName = "another" }},
		{"type", func(m *modelcatalog.Model) { m.ProviderType = "ollama" }},
		{"url", func(m *modelcatalog.Model) { m.ProviderBaseURL = "https://new.test/v1" }},
		{"key", func(m *modelcatalog.Model) { m.ProviderAPIKey = "rotated" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			creates := 0
			adapter := &routedModelAdapter{
				factory: func(_ modelcatalog.Model, _ q15media.Store) (agent.ModelClient, error) {
					creates++
					return &fakeModelClient{}, nil
				},
			}
			original := modelcatalog.Model{
				ProviderName:    "p",
				ProviderType:    "openai-compatible",
				ProviderBaseURL: "https://old.test/v1",
				ProviderAPIKey:  "original",
			}
			first, err := adapter.clientFor(original)
			if err != nil {
				t.Fatal(err)
			}
			changed := original
			tt.change(&changed)
			second, err := adapter.clientFor(changed)
			if err != nil {
				t.Fatal(err)
			}
			reused, err := adapter.clientFor(original)
			if err != nil {
				t.Fatal(err)
			}
			if first == second || reused != first || creates != 2 {
				t.Errorf("cache failed to separate %s changes: factories=%d", tt.name, creates)
			}
		})
	}
}
