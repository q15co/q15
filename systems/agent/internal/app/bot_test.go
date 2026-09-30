package app

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/bus"
	"github.com/q15co/q15/systems/agent/internal/channel/telegram"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
	"github.com/q15co/q15/systems/agent/internal/modelselection"
	"github.com/q15co/q15/systems/agent/internal/schedule"
	q15tools "github.com/q15co/q15/systems/agent/internal/tools"
	"github.com/q15co/q15/systems/agent/internal/turnctx"
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

// TestRunRuntimeRequiresItsParts pins the guards runRuntime applies before it
// marks itself ready: a runtime with no bus to report into, no agent to run, no
// scheduler to run, or no channel endpoint to be reached through refuses to come
// up rather than starting half of itself. The runtime's parts are keyed at the
// call site, so omitting one is otherwise silent until it fails in a goroutine.
func TestRunRuntimeRequiresItsParts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := runRuntime(ctx, cancel, runtimeInputs{})
	if err == nil || !strings.Contains(err.Error(), "message bus") {
		t.Fatalf("runRuntime() error = %v, want a missing message bus", err)
	}

	parts := runtimeInputs{messageBus: bus.New(1)}
	err = runRuntime(ctx, cancel, parts)
	if err == nil || !strings.Contains(err.Error(), "bot agent") {
		t.Fatalf("runRuntime() error = %v, want a missing bot agent", err)
	}

	parts.botAgent = &fakeObservedAgent{}
	err = runRuntime(ctx, cancel, parts)
	if err == nil || !strings.Contains(err.Error(), "schedule manager") {
		t.Fatalf("runRuntime() error = %v, want a missing schedule manager", err)
	}

	parts.scheduleManager = &schedule.Manager{}
	err = runRuntime(ctx, cancel, parts)
	if err == nil || !strings.Contains(err.Error(), "channel endpoint") {
		t.Fatalf("runRuntime() error = %v, want a missing channel endpoint", err)
	}
}

// TestNewChatBridgeDisabledWithoutTarget pins the bind-failure decision: an
// unset listen target disables the bridge instead of refusing to boot, so
// deployments run unchanged until the socket volume exists.
func TestNewChatBridgeDisabledWithoutTarget(t *testing.T) {
	bridge, err := bridgeSettings{}.newChatBridge(nil, nil)
	if err != nil {
		t.Fatalf("newChatBridge() error = %v", err)
	}
	if bridge != nil {
		t.Fatal("newChatBridge() = a bridge, want nil while disabled")
	}
}

// TestNewChatBridgeBuildsBothHalves pins the invariant the group exists for: a
// bridge with a listener always carries its endpoint too, so no caller has to
// check one half without the other.
func TestNewChatBridgeBuildsBothHalves(t *testing.T) {
	settings := bridgeSettings{
		listenTarget: "unix://" + filepath.Join(t.TempDir(), "bridge.sock"),
	}

	bridge, err := settings.newChatBridge(nil, nil)
	if err != nil {
		// Binding chgrps the socket to the bridge's group, which a test process
		// that is neither root nor a member of that group cannot do. The bridge
		// package's own bind tests guard on the same thing; without this, a CI
		// runner fails here for a reason that has nothing to do with the
		// invariant under test.
		if errors.Is(err, fs.ErrPermission) {
			t.Skipf("binding the bridge socket needs the socket group's permission: %v", err)
		}
		t.Fatalf("newChatBridge() error = %v", err)
	}
	if bridge == nil {
		t.Fatal("newChatBridge() = nil, want a bridge for a configured target")
	}
	t.Cleanup(func() { bridge.server.Close() })
	if bridge.server == nil || bridge.endpoint == nil {
		t.Fatalf("newChatBridge() = %+v, want both halves", bridge)
	}
}

// TestNewChatBridgeLeavesNoHalfBuiltBridge pins the other half of the same
// invariant, where it can be pinned anywhere: when the listener cannot bind, no
// bridge comes back alongside the error, so a caller never holds one half of a
// bridge that never listened.
func TestNewChatBridgeLeavesNoHalfBuiltBridge(t *testing.T) {
	settings := bridgeSettings{
		listenTarget: "unix://" + filepath.Join(t.TempDir(), "missing-dir", "bridge.sock"),
	}

	bridge, err := settings.newChatBridge(nil, nil)
	if err == nil {
		t.Fatal("newChatBridge() error = nil, want a bind failure")
	}
	if bridge != nil {
		t.Fatalf("newChatBridge() = %+v, want nil alongside the error", bridge)
	}
}

// TestNewChatBridgeSurfacesConfiguredBindFailure pins the other half of the
// same decision: a configured target that cannot bind fails loudly, with the
// socket path in the error.
func TestNewChatBridgeSurfacesConfiguredBindFailure(t *testing.T) {
	settings := bridgeSettings{
		// Built by concatenation on purpose: filepath.Join would clean the
		// double slash away, the target would stop looking like unix://, and
		// the test would exercise the TCP branch instead of the unix one.
		listenTarget: "unix://" + filepath.Join(t.TempDir(), "missing-dir", "bridge.sock"),
	}

	_, err := settings.newChatBridge(nil, nil)
	if err == nil {
		t.Fatal("newChatBridge() error = nil, want bind failure")
	}
	if !strings.Contains(err.Error(), filepath.Join("missing-dir", "bridge.sock")) {
		t.Fatalf("newChatBridge() error = %v, want the socket path named", err)
	}
	// The whole point of building the target by concatenation above: this must
	// fail on the unix branch. If the target lost its unix:// prefix the error
	// would come from the TCP branch and the unix bind path would never be
	// exercised.
	if !strings.Contains(err.Error(), "listen unix ") {
		t.Fatalf("newChatBridge() error = %v, want a unix listen failure", err)
	}
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
		buildInteractiveSystemTextHints(registry, selection, store)...,
	)

	runCtx := turnctx.WithOrigin(context.Background(), turnctx.Origin{
		Channel: bus.ChannelTelegram,
	})
	out, err := loop.Reply(runCtx, conversation.UserMessage("switch"), nil)
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
	for i, prompt := range []string{firstPrompt, secondPrompt} {
		if !strings.Contains(prompt, `<response_format channel="telegram">`) ||
			!strings.Contains(prompt, "spoilers (`||text||`)") {
			t.Fatalf("model call %d missing Telegram response format:\n%s", i, prompt)
		}
	}
}
