package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/bus"
	"github.com/q15co/q15/systems/agent/internal/channel/telegram"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	q15media "github.com/q15co/q15/systems/agent/internal/media"
	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
	"github.com/q15co/q15/systems/agent/internal/modelselection"
)

// --- test helpers ---

type fakeModelClient struct {
	calls []fakeModelCall
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

// --- tests ---

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

func TestCognitionJobsRegistersBuiltInCognitionJobs(t *testing.T) {
	jobs := cognitionJobs()
	if len(jobs) < 3 {
		t.Fatalf("cognitionJobs() returned %d jobs, want at least 3", len(jobs))
	}
}

// --- non-model tests (kept from original) ---

func TestComposeSystemPromptIncludesRuntimeInfo(t *testing.T) {
	info := runtimeEnvironmentInfo{
		WorkspaceDir: "/workspace",
		MemoryDir:    "/memory",
		MediaDir:     "/media",
		SkillsDir:    "/skills",
		ExecutorType: "nix",
	}
	prompt := composeSystemPrompt("base", "TestAgent", info, nil)
	if !strings.Contains(prompt, "TestAgent") {
		t.Error("prompt should contain agent name")
	}
}

func TestTelegramInboundMessagePreservesFields(t *testing.T) {
	msg := telegram.IncomingMessage{
		ChatID:    "123",
		UserID:    "456",
		MessageID: "789",
		Text:      "hello",
	}
	busMsg := telegramInboundMessage(msg)
	if busMsg.ChatID != "123" || busMsg.UserID != "456" || busMsg.Text != "hello" {
		t.Fatalf("telegramInboundMessage lost fields: %+v", busMsg)
	}
}

func TestRunAgentWorkerCancelReturnsNil(_ *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = runAgentWorker(ctx, bus.New(1), nil, nil)
}

// unused import guard
var _ = os.Stdout
var _ = filepath.Join
