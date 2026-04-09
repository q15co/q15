package app

import (
	"context"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/bus"
	"github.com/q15co/q15/systems/agent/internal/channel/telegram"
	"github.com/q15co/q15/systems/agent/internal/config"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	q15media "github.com/q15co/q15/systems/agent/internal/media"
	"github.com/q15co/q15/systems/agent/internal/modelselection"
)

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

func TestNewModelAdapterRoutesConfiguredModelsAndSuppressesTools(t *testing.T) {
	clients := make(map[string]*fakeModelClient)
	factoryCalls := make(map[string]int)

	adapter, err := newModelAdapterWithFactory([]config.AgentModelRuntime{
		{
			Ref:           "primary",
			ProviderName:  "moonshot",
			ProviderModel: "kimi-k2.5",
			Capabilities: config.ModelCapabilities{
				Text: true,
			},
		},
		{
			Ref:           "secondary",
			ProviderName:  "moonshot",
			ProviderModel: "kimi-k2",
			Capabilities: config.ModelCapabilities{
				Text:        true,
				ToolCalling: true,
			},
		},
		{
			Ref:           "backup",
			ProviderName:  "openai-sub",
			ProviderModel: "gpt-5-codex",
			Capabilities: config.ModelCapabilities{
				Text:        true,
				ToolCalling: true,
			},
		},
	}, nil, func(modelCfg config.AgentModelRuntime, _ q15media.Store) (agent.ModelClient, error) {
		factoryCalls[modelCfg.ProviderName]++
		client := &fakeModelClient{}
		clients[modelCfg.ProviderName] = client
		return client, nil
	})
	if err != nil {
		t.Fatalf("newModelAdapterWithFactory() error = %v", err)
	}

	tools := []agent.ToolDefinition{{Name: "shell"}}

	if _, err := adapter.Complete(context.Background(), "primary", nil, tools); err != nil {
		t.Fatalf("Complete(primary) error = %v", err)
	}
	if got := factoryCalls["moonshot"]; got != 1 {
		t.Fatalf("factoryCalls[moonshot] = %d, want 1", got)
	}
	if len(clients["moonshot"].calls) != 1 {
		t.Fatalf("moonshot calls = %d, want 1", len(clients["moonshot"].calls))
	}
	firstCall := clients["moonshot"].calls[0]
	if firstCall.model != "kimi-k2.5" {
		t.Fatalf("first provider model = %q, want %q", firstCall.model, "kimi-k2.5")
	}
	if len(firstCall.tools) != 0 {
		t.Fatalf("first tools = %#v, want suppressed tools", firstCall.tools)
	}

	if _, err := adapter.Complete(context.Background(), "secondary", nil, tools); err != nil {
		t.Fatalf("Complete(secondary) error = %v", err)
	}
	if got := factoryCalls["moonshot"]; got != 1 {
		t.Fatalf("factoryCalls[moonshot] after second call = %d, want 1", got)
	}
	if len(clients["moonshot"].calls) != 2 {
		t.Fatalf("moonshot calls = %d, want 2", len(clients["moonshot"].calls))
	}
	secondCall := clients["moonshot"].calls[1]
	if secondCall.model != "kimi-k2" {
		t.Fatalf("second provider model = %q, want %q", secondCall.model, "kimi-k2")
	}
	if len(secondCall.tools) != 1 || secondCall.tools[0].Name != "shell" {
		t.Fatalf("second tools = %#v, want one shell tool", secondCall.tools)
	}

	if _, err := adapter.Complete(context.Background(), "backup", nil, tools); err != nil {
		t.Fatalf("Complete(backup) error = %v", err)
	}
	if got := factoryCalls["openai-sub"]; got != 1 {
		t.Fatalf("factoryCalls[openai-sub] = %d, want 1", got)
	}
	if len(clients["openai-sub"].calls) != 1 {
		t.Fatalf("openai-sub calls = %d, want 1", len(clients["openai-sub"].calls))
	}
	if clients["openai-sub"].calls[0].model != "gpt-5-codex" {
		t.Fatalf(
			"backup provider model = %q, want %q",
			clients["openai-sub"].calls[0].model,
			"gpt-5-codex",
		)
	}
}

func TestRoutedModelAdapterPlanSelectionFiltersByCapabilitiesAndPreservesOrder(t *testing.T) {
	adapter, err := newModelAdapterWithFactory([]config.AgentModelRuntime{
		{
			Ref:           "text-only",
			ProviderName:  "moonshot",
			ProviderModel: "kimi-k2.5",
			Capabilities: config.ModelCapabilities{
				Text: true,
			},
		},
		{
			Ref:           "vision",
			ProviderName:  "vision",
			ProviderModel: "gpt-4.1",
			Capabilities: config.ModelCapabilities{
				Text:       true,
				ImageInput: true,
			},
		},
		{
			Ref:           "vision-tools",
			ProviderName:  "vision-tools",
			ProviderModel: "gpt-5",
			Capabilities: config.ModelCapabilities{
				Text:        true,
				ImageInput:  true,
				ToolCalling: true,
			},
		},
	}, nil, func(_ config.AgentModelRuntime, _ q15media.Store) (agent.ModelClient, error) {
		return &fakeModelClient{}, nil
	})
	if err != nil {
		t.Fatalf("newModelAdapterWithFactory() error = %v", err)
	}

	var planner modelselection.Planner = adapter

	plan, err := planner.Plan(
		[]string{"text-only", "vision", "vision-tools"},
		modelselection.Requirements{
			Text:       true,
			ImageInput: true,
		},
	)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if got, want := plan.EligibleRefs, []string{"vision", "vision-tools"}; len(got) != len(want) ||
		got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("eligible refs = %#v, want %#v", got, want)
	}
	if len(plan.Skipped) != 1 {
		t.Fatalf("skipped len = %d, want 1", len(plan.Skipped))
	}
	if plan.Skipped[0].Ref != "text-only" ||
		plan.Skipped[0].Reason != "missing capabilities [image_input]" {
		t.Fatalf("skipped[0] = %#v", plan.Skipped[0])
	}
}

func TestRoutedModelAdapterPlanSelectionRejectsUnknownModelRef(t *testing.T) {
	adapter, err := newModelAdapterWithFactory([]config.AgentModelRuntime{
		{
			Ref:           "primary",
			ProviderName:  "moonshot",
			ProviderModel: "kimi-k2.5",
			Capabilities: config.ModelCapabilities{
				Text: true,
			},
		},
	}, nil, func(_ config.AgentModelRuntime, _ q15media.Store) (agent.ModelClient, error) {
		return &fakeModelClient{}, nil
	})
	if err != nil {
		t.Fatalf("newModelAdapterWithFactory() error = %v", err)
	}

	var planner modelselection.Planner = adapter
	if _, err := planner.Plan(
		[]string{"missing"},
		modelselection.Requirements{Text: true},
	); err == nil {
		t.Fatal("Plan() error = nil, want unknown model error")
	}
}

func TestCognitionJobsRegistersBuiltInCognitionJobs(t *testing.T) {
	jobs := cognitionJobs()
	if len(jobs) != 2 {
		t.Fatalf("cognitionJobs len = %d, want 2", len(jobs))
	}

	gotTypes := make([]string, 0, len(jobs))
	for _, registration := range jobs {
		job := registration.NewJob()
		if job == nil {
			t.Fatal("NewJob() = nil")
		}
		gotTypes = append(gotTypes, job.Type())
	}
	if got, want := gotTypes[0], "working_memory.consolidate"; got != want {
		t.Fatalf("jobs[0].Type() = %q, want %q", got, want)
	}
	if got, want := gotTypes[1], "verification_review"; got != want {
		t.Fatalf("jobs[1].Type() = %q, want %q", got, want)
	}
	if len(jobs[0].Policy.Startup) == 0 {
		t.Fatal("startup rules = 0, want at least 1")
	}
	if len(jobs[0].Policy.Schedule) == 0 {
		t.Fatal("schedule rules = 0, want at least 1")
	}
	if len(jobs[0].Policy.State) == 0 {
		t.Fatal("state rules = 0, want at least 1")
	}
	if len(jobs[1].Policy.Startup) != 0 {
		t.Fatalf("verification startup rules = %d, want 0", len(jobs[1].Policy.Startup))
	}
	if len(jobs[1].Policy.Schedule) != 0 {
		t.Fatalf("verification schedule rules = %d, want 0", len(jobs[1].Policy.Schedule))
	}
	if len(jobs[1].Policy.State) == 0 {
		t.Fatal("verification state rules = 0, want at least 1")
	}
}

func TestMergedModelCatalogSupportsCognitionOnlyModelRefs(t *testing.T) {
	merged := mergeModelRuntimes(
		[]config.AgentModelRuntime{
			{
				Ref:           "interactive",
				ProviderName:  "openai",
				ProviderModel: "gpt-5.4",
				Capabilities: config.ModelCapabilities{
					Text: true,
				},
			},
		},
		[]config.AgentModelRuntime{
			{
				Ref:           "interactive",
				ProviderName:  "openai",
				ProviderModel: "gpt-5.4",
				Capabilities: config.ModelCapabilities{
					Text: true,
				},
			},
			{
				Ref:           "cognition",
				ProviderName:  "moonshot",
				ProviderModel: "kimi-k2.5",
				Capabilities: config.ModelCapabilities{
					Text: true,
				},
			},
		},
	)
	if got, want := len(merged), 2; got != want {
		t.Fatalf("merged len = %d, want %d", got, want)
	}
	if merged[0].Ref != "interactive" || merged[1].Ref != "cognition" {
		t.Fatalf(
			"merged refs = %#v, want interactive-first ordering",
			[]string{merged[0].Ref, merged[1].Ref},
		)
	}

	adapter, err := newModelAdapterWithFactory(
		merged,
		nil,
		func(_ config.AgentModelRuntime, _ q15media.Store) (agent.ModelClient, error) {
			return &fakeModelClient{}, nil
		},
	)
	if err != nil {
		t.Fatalf("newModelAdapterWithFactory() error = %v", err)
	}

	var planner modelselection.Planner = adapter
	plan, err := planner.Plan([]string{"cognition"}, modelselection.Requirements{Text: true})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if got, want := plan.EligibleRefs, []string{"cognition"}; len(got) != len(want) ||
		got[0] != want[0] {
		t.Fatalf("eligible refs = %#v, want %#v", got, want)
	}
}

func TestRoutedModelAdapterPlanSelectionReturnsEmptyPlanWhenNoCandidatesMatch(t *testing.T) {
	adapter, err := newModelAdapterWithFactory([]config.AgentModelRuntime{
		{
			Ref:           "text-only",
			ProviderName:  "moonshot",
			ProviderModel: "kimi-k2.5",
			Capabilities: config.ModelCapabilities{
				Text: true,
			},
		},
	}, nil, func(_ config.AgentModelRuntime, _ q15media.Store) (agent.ModelClient, error) {
		return &fakeModelClient{}, nil
	})
	if err != nil {
		t.Fatalf("newModelAdapterWithFactory() error = %v", err)
	}

	var planner modelselection.Planner = adapter
	plan, err := planner.Plan(
		[]string{"text-only"},
		modelselection.Requirements{
			Text:        true,
			ImageInput:  true,
			ToolCalling: true,
		},
	)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if len(plan.EligibleRefs) != 0 {
		t.Fatalf("eligible refs = %#v, want empty", plan.EligibleRefs)
	}
	if len(plan.Skipped) != 1 {
		t.Fatalf("skipped len = %d, want 1", len(plan.Skipped))
	}
	if plan.Skipped[0].Reason != "missing capabilities [image_input, tool_calling]" {
		t.Fatalf("skip reason = %q", plan.Skipped[0].Reason)
	}
}

func TestTelegramInboundMessagePreservesMediaRefs(t *testing.T) {
	got := telegramInboundMessage(telegram.IncomingMessage{
		ChatID:    "chat-1",
		UserID:    "user-1",
		MessageID: "msg-1",
		Text:      "describe this",
		Media:     []string{"media://sha256/abc"},
	})

	if got.Channel != bus.ChannelTelegram {
		t.Fatalf("Channel = %q, want %q", got.Channel, bus.ChannelTelegram)
	}
	if got.ChatID != "chat-1" || got.UserID != "user-1" || got.MessageID != "msg-1" {
		t.Fatalf("inbound = %#v", got)
	}
	if got.Text != "describe this" {
		t.Fatalf("Text = %q, want describe this", got.Text)
	}
	if len(got.Media) != 1 || got.Media[0] != "media://sha256/abc" {
		t.Fatalf("Media = %#v, want telegram media ref", got.Media)
	}
}
