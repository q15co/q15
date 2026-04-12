package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/conversation"
)

func TestEngineRun_ReturnsOnlyGeneratedMessages(t *testing.T) {
	model := &fakeModelClient{
		results: []ModelClientResult{assistantResult("done")},
	}

	engine := NewEngine(model, nil, []string{"m1"})
	result, err := engine.Run(context.Background(), EngineRequest{
		Messages: []conversation.Message{
			conversation.SystemMessage("prompt"),
			conversation.UserMessage("hello"),
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.FinalText != "done" {
		t.Fatalf("result.FinalText = %q, want %q", result.FinalText, "done")
	}
	if len(result.Messages) != 1 {
		t.Fatalf("result.Messages len = %d, want 1", len(result.Messages))
	}
	if result.Messages[0].Role != conversation.AssistantRole {
		t.Fatalf("result.Messages[0].Role = %q, want assistant", result.Messages[0].Role)
	}
	if messageText(result.Messages[0]) != "done" {
		t.Fatalf("result.Messages[0] = %#v", result.Messages[0])
	}
	if len(model.callMsgs) != 1 {
		t.Fatalf("model calls = %d, want 1", len(model.callMsgs))
	}
	if len(model.callMsgs[0]) != 2 {
		t.Fatalf("model input len = %d, want 2", len(model.callMsgs[0]))
	}
}

func TestEngineRun_RequiresToolCallingWhenRequested(t *testing.T) {
	planner := &fakePlanner{}
	var gotTools []ToolDefinition
	model := &fakeModelClient{
		complete: func(
			_ context.Context,
			_ string,
			_ []conversation.Message,
			tools []ToolDefinition,
		) (ModelClientResult, error) {
			gotTools = append([]ToolDefinition(nil), tools...)
			return assistantResult("done"), nil
		},
	}
	registry, err := NewToolRegistry(&testTool{
		def: ToolDefinition{Name: "echo"},
	})
	if err != nil {
		t.Fatalf("NewToolRegistry() error = %v", err)
	}

	engine := NewEngineWithPlanner(model, planner, registry, []string{"m1"})
	result, err := engine.Run(context.Background(), EngineRequest{
		Messages: []conversation.Message{
			conversation.SystemMessage("prompt"),
		},
		UseTools:           true,
		RequireToolCalling: true,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.FinalText != "done" {
		t.Fatalf("result.FinalText = %q, want %q", result.FinalText, "done")
	}
	if len(planner.plannedRequirements) != 1 {
		t.Fatalf("planned requirements len = %d, want 1", len(planner.plannedRequirements))
	}
	if !planner.plannedRequirements[0].ToolCalling {
		t.Fatalf("requirements = %#v, want tool_calling", planner.plannedRequirements[0])
	}
	if len(gotTools) != 1 || gotTools[0].Name != "echo" {
		t.Fatalf("gotTools = %#v, want one echo tool", gotTools)
	}
}

func TestEngineRun_AllModelsFailed_ReportsPerModelErrors(t *testing.T) {
	model := &fakeModelClient{
		complete: func(
			_ context.Context,
			model string,
			_ []conversation.Message,
			_ []ToolDefinition,
		) (ModelClientResult, error) {
			if model == "gpt-5.4" {
				return ModelClientResult{}, errors.New("responses API error: internal server error")
			}
			return ModelClientResult{}, errors.New("messages parameter is illegal (code 1214)")
		},
	}

	engine := NewEngine(model, nil, []string{"gpt-5.4", "glm-5-turbo"})
	_, err := engine.Run(context.Background(), EngineRequest{
		Messages: []conversation.Message{
			conversation.SystemMessage("prompt"),
			conversation.UserMessage("hello"),
		},
	})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	errMsg := err.Error()
	if !strings.Contains(errMsg, "per-model errors:") {
		t.Fatalf("error should contain 'per-model errors:', got: %s", errMsg)
	}
	if !strings.Contains(errMsg, "gpt-5.4:") {
		t.Fatalf("error should mention gpt-5.4, got: %s", errMsg)
	}
	if !strings.Contains(errMsg, "glm-5-turbo:") {
		t.Fatalf("error should mention glm-5-turbo, got: %s", errMsg)
	}
	// lastErr should still be unwrappable
	if !strings.Contains(errMsg, "all models failed") {
		t.Fatalf("error should contain 'all models failed', got: %s", errMsg)
	}
}

func TestEngineRun_SingleModelFailed_NoPerModelSection(t *testing.T) {
	model := &fakeModelClient{
		results: []ModelClientResult{},
		complete: func(
			_ context.Context,
			_ string,
			_ []conversation.Message,
			_ []ToolDefinition,
		) (ModelClientResult, error) {
			return ModelClientResult{}, errors.New("connection refused")
		},
	}

	engine := NewEngine(model, nil, []string{"only-model"})
	_, err := engine.Run(context.Background(), EngineRequest{
		Messages: []conversation.Message{
			conversation.SystemMessage("prompt"),
			conversation.UserMessage("hello"),
		},
	})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	errMsg := err.Error()
	if strings.Contains(errMsg, "per-model errors:") {
		t.Fatalf("single model failure should not include per-model section, got: %s", errMsg)
	}
}

func TestEngineRun_ToolProducedImageRequiresImageInputOnNextTurn(t *testing.T) {
	planner := &fakePlanner{}
	model := &fakeModelClient{
		results: []ModelClientResult{
			toolCallResult("call-1", "load_image", `{"path":"artifact.png"}`),
			assistantResult("done"),
		},
	}
	registry, err := NewToolRegistry(&structuredTestTool{
		def: ToolDefinition{Name: "load_image"},
		runResult: func(context.Context, string) (ToolResult, error) {
			return ToolResult{
				Output:    "Loaded image: /workspace/artifact.png",
				MediaRefs: []string{"media://sha256/abc"},
			}, nil
		},
	})
	if err != nil {
		t.Fatalf("NewToolRegistry() error = %v", err)
	}

	engine := NewEngineWithPlanner(model, planner, registry, []string{"m1"})
	result, err := engine.Run(context.Background(), EngineRequest{
		Messages: []conversation.Message{
			conversation.SystemMessage("prompt"),
		},
		UseTools: true,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.FinalText != "done" {
		t.Fatalf("result.FinalText = %q, want %q", result.FinalText, "done")
	}
	if len(planner.plannedRequirements) != 2 {
		t.Fatalf("planned requirements len = %d, want 2", len(planner.plannedRequirements))
	}
	if planner.plannedRequirements[0].ImageInput {
		t.Fatalf("first turn requirements = %#v, want text-only", planner.plannedRequirements[0])
	}
	if !planner.plannedRequirements[1].ImageInput {
		t.Fatalf(
			"second turn requirements = %#v, want image_input",
			planner.plannedRequirements[1],
		)
	}
}
