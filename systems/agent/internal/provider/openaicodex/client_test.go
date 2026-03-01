package openaicodex

import (
	"testing"

	"github.com/openai/openai-go/v3/responses"
	"github.com/q15co/q15/systems/agent/internal/agent"
)

func TestMapMessagesAndTools(t *testing.T) {
	input, instructions, err := mapMessages([]agent.Message{
		{Role: agent.SystemRole, Content: "sys"},
		{Role: agent.UserRole, Content: "hello"},
		{
			Role:    agent.AssistantRole,
			Content: "calling tool",
			ToolCalls: []agent.ToolCall{
				{ID: "call-1", Name: "shell", Arguments: `{"cmd":"pwd"}`},
			},
		},
		{Role: agent.ToolRole, ToolCallID: "call-1", Content: "ok"},
	})
	if err != nil {
		t.Fatalf("mapMessages error = %v", err)
	}
	if instructions != "sys" {
		t.Fatalf("instructions = %q, want %q", instructions, "sys")
	}
	if len(input) != 4 {
		t.Fatalf("input len = %d, want 4", len(input))
	}

	if got := input[0].OfMessage; got == nil || got.Role != responses.EasyInputMessageRoleUser {
		t.Fatalf("input[0] should be user message: %#v", input[0])
	}
	if got := input[1].OfMessage; got == nil ||
		got.Role != responses.EasyInputMessageRoleAssistant {
		t.Fatalf("input[1] should be assistant message: %#v", input[1])
	}
	if got := input[2].OfFunctionCall; got == nil || got.CallID != "call-1" {
		t.Fatalf("input[2] should be function call with call-1: %#v", input[2])
	}
	if got := input[3].OfFunctionCallOutput; got == nil || got.CallID != "call-1" {
		t.Fatalf("input[3] should be function call output with call-1: %#v", input[3])
	}

	tools := mapTools([]agent.ToolDefinition{
		{
			Name:        "shell",
			Description: "run shell command",
			Parameters: map[string]any{
				"type": "object",
			},
		},
		{Name: "empty-params"},
	})
	if len(tools) != 2 {
		t.Fatalf("tools len = %d, want 2", len(tools))
	}
	if tools[0].OfFunction == nil || tools[0].OfFunction.Name != "shell" {
		t.Fatalf("tools[0] should be shell function: %#v", tools[0])
	}
	if tools[1].OfFunction == nil || tools[1].OfFunction.Name != "empty-params" {
		t.Fatalf("tools[1] should be empty-params function: %#v", tools[1])
	}
	if tools[1].OfFunction.Parameters == nil {
		t.Fatalf("tools[1] parameters should be defaulted")
	}
}

func TestParseResponseContentAndToolCalls(t *testing.T) {
	resp := &responses.Response{
		Status: "completed",
		Output: []responses.ResponseOutputItemUnion{
			{
				Type: "message",
				Content: []responses.ResponseOutputMessageContentUnion{
					{Type: "output_text", Text: "hello"},
				},
			},
			{
				Type:      "function_call",
				CallID:    "call-1",
				Name:      "shell",
				Arguments: `{"cmd":"ls"}`,
			},
		},
	}

	got := parseResponse(resp)
	if got.Content != "hello" {
		t.Fatalf("content = %q, want %q", got.Content, "hello")
	}
	if len(got.ToolCalls) != 1 {
		t.Fatalf("tool calls len = %d, want 1", len(got.ToolCalls))
	}
	if got.ToolCalls[0].ID != "call-1" || got.ToolCalls[0].Name != "shell" {
		t.Fatalf("unexpected tool call: %#v", got.ToolCalls[0])
	}
	if got.FinishReason != "tool_calls" {
		t.Fatalf("finish reason = %q, want %q", got.FinishReason, "tool_calls")
	}
}

func TestParseResponseIncompleteMapsToLengthFinishReason(t *testing.T) {
	resp := &responses.Response{
		Status: "incomplete",
		Output: []responses.ResponseOutputItemUnion{
			{
				Type: "message",
				Content: []responses.ResponseOutputMessageContentUnion{
					{Type: "output_text", Text: "partial"},
				},
			},
		},
	}

	got := parseResponse(resp)
	if got.FinishReason != "length" {
		t.Fatalf("finish reason = %q, want %q", got.FinishReason, "length")
	}
	if got.Content != "partial" {
		t.Fatalf("content = %q, want %q", got.Content, "partial")
	}
}
