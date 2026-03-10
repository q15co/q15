package app

import (
	"context"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/config"
)

type fakeModelClient struct {
	calls []fakeModelCall
}

type fakeModelCall struct {
	model string
	tools []agent.ToolDefinition
}

func (f *fakeModelClient) Complete(
	_ context.Context,
	model string,
	_ []agent.Message,
	tools []agent.ToolDefinition,
) (agent.ModelClientResult, error) {
	call := fakeModelCall{model: model}
	if len(tools) > 0 {
		call.tools = append([]agent.ToolDefinition(nil), tools...)
	}
	f.calls = append(f.calls, call)
	return agent.ModelClientResult{Content: "ok"}, nil
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
	}, func(modelCfg config.AgentModelRuntime) (agent.ModelClient, error) {
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
