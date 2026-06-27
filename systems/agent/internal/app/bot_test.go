package app

import (
	"context"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/bus"
	"github.com/q15co/q15/systems/agent/internal/channel/telegram"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
	"github.com/q15co/q15/systems/agent/internal/modelselection"
	q15tools "github.com/q15co/q15/systems/agent/internal/tools"
)

func TestCognitionJobsRegistersBuiltInCognitionJobs(t *testing.T) {
	jobs := cognitionJobs()
	if len(jobs) < 3 {
		t.Fatalf("cognitionJobs() returned %d jobs, want at least 3", len(jobs))
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

// TestSwitchModelUpdatesNextModelTurnPrompt exercises the full turn path: a
// switch_model call must update both the next model ref the engine selects and
// the current-model section rendered into the next turn's system prompt.
func TestSwitchModelUpdatesNextModelTurnPrompt(t *testing.T) {
	registry := testRegistry(t, map[string][]modelcatalog.Model{
		"p": {{ProviderModel: "old", Capabilities: modelcatalog.Capabilities{Text: true}}},
		"q": {{ProviderModel: "new", Capabilities: modelcatalog.Capabilities{Text: true}}},
	})
	selection := modelcatalog.NewSelection(registry, "p", "old")
	store := openTestSelectionStore(t)
	toolRegistry, err := agent.NewToolRegistry(q15tools.NewSwitchModel(registry, selection, store))
	if err != nil {
		t.Fatalf("NewToolRegistry() error = %v", err)
	}
	model := &fakeModelClient{results: []agent.ModelClientResult{
		{
			Messages: []conversation.Message{conversation.AssistantMessage(conversation.ToolCall(
				"switch-1",
				"switch_model",
				`{"provider":"q","model":"new","reason":"test"}`,
			))},
			FinishReason: "tool_calls",
		},
		{
			Messages: []conversation.Message{
				conversation.AssistantMessage(conversation.Text("done", "")),
			},
		},
	}}
	loop := agent.NewLoopWithPlannerAndModelRefSource(
		model,
		modelselection.Passthrough{},
		toolRegistry,
		func() []string { return buildModelRefs(selection.CurrentModel(), registry) },
		"base",
		nil,
		0,
		func() string { return renderCurrentModelPrompt(registry, selection, store) },
	)

	out, err := loop.Reply(context.Background(), conversation.UserMessage("switch"), nil)
	if err != nil {
		t.Fatalf("Reply() error = %v", err)
	}
	if out.Text != "done" {
		t.Fatalf("Reply().Text = %q, want done", out.Text)
	}
	if got, want := len(model.calls), 2; got != want {
		t.Fatalf("model calls = %d, want %d", got, want)
	}
	if model.calls[0].model != "old" || model.calls[1].model != "new" {
		t.Fatalf("models = [%s, %s], want [old, new]", model.calls[0].model, model.calls[1].model)
	}
	firstPrompt := conversation.TextValue(model.calls[0].messages[0])
	secondPrompt := conversation.TextValue(model.calls[1].messages[0])
	if !strings.Contains(firstPrompt, `provider: "p"`) ||
		!strings.Contains(firstPrompt, `model: "old"`) {
		t.Fatalf("first prompt missing old selection:\n%s", firstPrompt)
	}
	if !strings.Contains(secondPrompt, `provider: "q"`) ||
		!strings.Contains(secondPrompt, `model: "new"`) {
		t.Fatalf("second prompt missing updated selection:\n%s", secondPrompt)
	}
}
