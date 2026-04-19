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

func TestEngineRun_ImageOnlyReplyLeavesFinalTextEmpty(t *testing.T) {
	model := &fakeModelClient{
		results: []ModelClientResult{
			{
				Messages: []conversation.Message{
					conversation.AssistantMessage(
						conversation.Image("media://sha256/only", ""),
					),
				},
			},
		},
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
	if result.FinalText != "" {
		t.Fatalf("result.FinalText = %q, want empty", result.FinalText)
	}
	if got := result.MediaRefs; len(got) != 1 || got[0] != "media://sha256/only" {
		t.Fatalf("result.MediaRefs = %#v, want image ref", got)
	}
	if got := result.Attachments; len(got) != 1 || got[0].MediaRef != "media://sha256/only" {
		t.Fatalf("result.Attachments = %#v, want image attachment", got)
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
	if got := result.MediaRefs; len(got) != 1 || got[0] != "media://sha256/abc" {
		t.Fatalf("result.MediaRefs = %#v, want tool image ref", got)
	}
	if got := result.Attachments; len(got) != 1 || got[0].MediaRef != "media://sha256/abc" {
		t.Fatalf("result.Attachments = %#v, want final image attachment", got)
	}
	if len(result.Messages) != 3 {
		t.Fatalf("result.Messages len = %d, want 3", len(result.Messages))
	}
	if toolParts := result.Messages[1].Parts; len(toolParts) != 1 ||
		toolParts[0].Type != conversation.ToolResultPartType {
		t.Fatalf("result.Messages[1].Parts = %#v, want tool_result only", toolParts)
	}
	last := result.Messages[2]
	if last.Role != conversation.AssistantRole || len(last.Parts) != 2 {
		t.Fatalf("result.Messages[2] = %#v, want final assistant text plus image", last)
	}
	if last.Parts[1].Type != conversation.ImagePartType ||
		last.Parts[1].MediaRef != "media://sha256/abc" {
		t.Fatalf("result.Messages[2].Parts[1] = %#v, want promoted image", last.Parts[1])
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

func TestEngineRun_PromotesOnlyTrailingToolBatchAttachments(t *testing.T) {
	model := &fakeModelClient{
		results: []ModelClientResult{
			toolCallResult("call-1", "load_image", `{"path":"preview.png"}`),
			toolCallResult("call-2", "load_image", `{"path":"final.png"}`),
			assistantResult("done"),
		},
	}
	registry, err := NewToolRegistry(&structuredTestTool{
		def: ToolDefinition{Name: "load_image"},
		runResult: func(_ context.Context, arguments string) (ToolResult, error) {
			switch arguments {
			case `{"path":"preview.png"}`:
				return ToolResult{
					Output:    "Loaded image: /workspace/preview.png",
					MediaRefs: []string{"media://sha256/preview"},
				}, nil
			case `{"path":"final.png"}`:
				return ToolResult{
					Output:    "Loaded image: /workspace/final.png",
					MediaRefs: []string{"media://sha256/final"},
				}, nil
			default:
				t.Fatalf("unexpected tool arguments %q", arguments)
				return ToolResult{}, nil
			}
		},
	})
	if err != nil {
		t.Fatalf("NewToolRegistry() error = %v", err)
	}

	engine := NewEngine(model, registry, []string{"m1"})
	result, err := engine.Run(context.Background(), EngineRequest{
		Messages: []conversation.Message{
			conversation.SystemMessage("prompt"),
		},
		UseTools: true,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := result.MediaRefs; len(got) != 1 || got[0] != "media://sha256/final" {
		t.Fatalf("result.MediaRefs = %#v, want only final image", got)
	}
	if len(result.Messages) != 5 {
		t.Fatalf("result.Messages len = %d, want 5", len(result.Messages))
	}
	if previewParts := result.Messages[1].Parts; len(previewParts) != 2 {
		t.Fatalf("preview tool parts = %#v, want preview tool result plus image", previewParts)
	}
	if finalToolParts := result.Messages[3].Parts; len(finalToolParts) != 1 ||
		finalToolParts[0].Type != conversation.ToolResultPartType {
		t.Fatalf("final tool parts = %#v, want tool_result only", finalToolParts)
	}
	final := result.Messages[4]
	if final.Role != conversation.AssistantRole || len(final.Parts) != 2 {
		t.Fatalf("final assistant = %#v, want text plus image", final)
	}
	if final.Parts[1].MediaRef != "media://sha256/final" {
		t.Fatalf("final attachment = %#v, want final image", final.Parts[1])
	}
}

func TestEngineRun_PrefersAssistantImageRefsOverToolFallback(t *testing.T) {
	model := &fakeModelClient{
		results: []ModelClientResult{
			toolCallResult("call-1", "load_image", `{"path":"artifact.png"}`),
			{
				Messages: []conversation.Message{
					conversation.AssistantMessage(
						conversation.Text("done", ""),
						conversation.Image("media://sha256/assistant-1", ""),
						conversation.Image("media://sha256/assistant-1", ""),
						conversation.Image("media://sha256/assistant-2", ""),
					),
				},
			},
		},
	}
	registry, err := NewToolRegistry(&structuredTestTool{
		def: ToolDefinition{Name: "load_image"},
		runResult: func(context.Context, string) (ToolResult, error) {
			return ToolResult{
				Output:    "Loaded image: /workspace/artifact.png",
				MediaRefs: []string{"media://sha256/tool-1", "media://sha256/tool-1"},
			}, nil
		},
	})
	if err != nil {
		t.Fatalf("NewToolRegistry() error = %v", err)
	}

	engine := NewEngine(model, registry, []string{"m1"})
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
		t.Fatalf("result.FinalText = %q, want done", result.FinalText)
	}
	if got, want := result.MediaRefs, []string{
		"media://sha256/assistant-1",
		"media://sha256/assistant-2",
	}; !equalStrings(got, want) {
		t.Fatalf("result.MediaRefs = %#v, want %#v", got, want)
	}
	if len(result.Messages) != 3 {
		t.Fatalf("result.Messages len = %d, want 3", len(result.Messages))
	}
	if toolParts := result.Messages[1].Parts; len(toolParts) != 3 ||
		toolParts[0].Type != conversation.ToolResultPartType {
		t.Fatalf(
			"result.Messages[1].Parts = %#v, want tool result plus original duplicate images",
			toolParts,
		)
	}
}

func equalStrings(got, want []string) bool {
	return strings.Join(got, "\x00") == strings.Join(want, "\x00")
}

func TestEngineRun_EmptyAllowedToolsPreservesAllTools(t *testing.T) {
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
	registry, err := NewToolRegistry(
		&testTool{def: ToolDefinition{Name: "one"}},
		&testTool{def: ToolDefinition{Name: "two"}},
	)
	if err != nil {
		t.Fatalf("NewToolRegistry() error = %v", err)
	}

	engine := NewEngine(model, registry, []string{"m1"})
	if _, err := engine.Run(context.Background(), EngineRequest{
		Messages: []conversation.Message{conversation.SystemMessage("prompt")},
		UseTools: true,
	}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if len(gotTools) != 2 || gotTools[0].Name != "one" || gotTools[1].Name != "two" {
		t.Fatalf("gotTools = %#v, want [one two]", gotTools)
	}
}

func TestEngineRun_AllowedToolsFilterExposedDefinitions(t *testing.T) {
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
	registry, err := NewToolRegistry(
		&testTool{def: ToolDefinition{Name: "one"}},
		&testTool{def: ToolDefinition{Name: "two"}},
	)
	if err != nil {
		t.Fatalf("NewToolRegistry() error = %v", err)
	}

	engine := NewEngine(model, registry, []string{"m1"})
	if _, err := engine.Run(context.Background(), EngineRequest{
		Messages:     []conversation.Message{conversation.SystemMessage("prompt")},
		UseTools:     true,
		AllowedTools: []string{"missing", "two", " "},
	}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if len(gotTools) != 1 || gotTools[0].Name != "two" {
		t.Fatalf("gotTools = %#v, want [two]", gotTools)
	}
}

func TestEngineRun_DisallowedToolCallReturnsUnsupportedToolResult(t *testing.T) {
	model := &fakeModelClient{
		results: []ModelClientResult{
			toolCallResult("call-1", "two", `{}`),
			assistantResult("done"),
		},
	}
	registry, err := NewToolRegistry(
		&testTool{def: ToolDefinition{Name: "one"}},
		&testTool{def: ToolDefinition{Name: "two"}},
	)
	if err != nil {
		t.Fatalf("NewToolRegistry() error = %v", err)
	}

	engine := NewEngine(model, registry, []string{"m1"})
	result, err := engine.Run(context.Background(), EngineRequest{
		Messages:     []conversation.Message{conversation.SystemMessage("prompt")},
		UseTools:     true,
		AllowedTools: []string{"one"},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	foundToolError := false
	for _, msg := range result.Messages {
		if msg.Role != conversation.ToolRole || len(msg.Parts) == 0 {
			continue
		}
		part := msg.Parts[0]
		if part.Type != conversation.ToolResultPartType {
			continue
		}
		if part.IsError && strings.Contains(part.Content, "unsupported tool: two") {
			foundToolError = true
			break
		}
	}
	if !foundToolError {
		t.Fatalf("result.Messages = %#v, want unsupported tool result", result.Messages)
	}
}

func TestEngineRun_PreservesOrderedModelAttemptFailures(t *testing.T) {
	err1 := errors.New("first failure")
	err2 := errors.New("second failure")
	model := &fakeModelClient{
		complete: func(
			_ context.Context,
			model string,
			_ []conversation.Message,
			_ []ToolDefinition,
		) (ModelClientResult, error) {
			switch model {
			case "m1":
				return ModelClientResult{}, err1
			case "m2":
				return ModelClientResult{}, err2
			default:
				t.Fatalf("unexpected model %q", model)
				return ModelClientResult{}, nil
			}
		},
	}

	engine := NewEngine(model, nil, []string{"m1", "m2"})
	_, err := engine.Run(context.Background(), EngineRequest{
		Messages: []conversation.Message{
			conversation.SystemMessage("prompt"),
			conversation.UserMessage("hello"),
		},
	})
	if err == nil {
		t.Fatal("Run() error = nil, want fallback failure")
	}

	var fallbackErr *ModelFallbackError
	if !errors.As(err, &fallbackErr) {
		t.Fatalf("Run() error = %v, want ModelFallbackError", err)
	}
	if got, want := len(fallbackErr.AttemptFailures), 2; got != want {
		t.Fatalf("attempt failures len = %d, want %d", got, want)
	}
	if fallbackErr.AttemptFailures[0].ModelRef != "m1" ||
		fallbackErr.AttemptFailures[0].Err != err1 {
		t.Fatalf("attempt[0] = %#v", fallbackErr.AttemptFailures[0])
	}
	if fallbackErr.AttemptFailures[1].ModelRef != "m2" ||
		fallbackErr.AttemptFailures[1].Err != err2 {
		t.Fatalf("attempt[1] = %#v", fallbackErr.AttemptFailures[1])
	}
	if !strings.Contains(err.Error(), "m1: first failure") ||
		!strings.Contains(err.Error(), "m2: second failure") {
		t.Fatalf("error = %q, want both ordered model failures", err.Error())
	}
	if unwrapped := errors.Unwrap(fallbackErr); unwrapped != err2 {
		t.Fatalf("Unwrap() = %#v, want %#v", unwrapped, err2)
	}
}
