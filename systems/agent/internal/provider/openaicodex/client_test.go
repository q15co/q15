package openaicodex

import (
	"encoding/json"
	"strings"
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

func TestMapMessagesConcatenatesSystemInstructions(t *testing.T) {
	_, instructions, err := mapMessages([]agent.Message{
		{Role: agent.SystemRole, Content: "base"},
		{Role: agent.UserRole, Content: "hello"},
		{Role: agent.SystemRole, Content: "steering"},
	})
	if err != nil {
		t.Fatalf("mapMessages() error = %v", err)
	}
	if instructions != "base\n\nsteering" {
		t.Fatalf("instructions = %q, want %q", instructions, "base\n\nsteering")
	}
}

func TestMapMessagesPreservesAssistantPhaseOnReplay(t *testing.T) {
	input, _, err := mapMessages([]agent.Message{
		{
			Role:    agent.AssistantRole,
			Content: "resumed assistant message",
			Phase:   "commentary",
		},
	})
	if err != nil {
		t.Fatalf("mapMessages() error = %v", err)
	}
	if len(input) != 1 {
		t.Fatalf("input len = %d, want 1", len(input))
	}

	data, err := json.Marshal(input[0])
	if err != nil {
		t.Fatalf("json.Marshal(input[0]) error = %v", err)
	}
	body := string(data)
	for _, want := range []string{
		`"role":"assistant"`,
		`"phase":"commentary"`,
		`"text":"resumed assistant message"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("serialized input missing %q: %s", want, body)
		}
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

func TestParseResponsePreservesAssistantPhaseAndRawMessage(t *testing.T) {
	var resp responses.Response
	if err := json.Unmarshal([]byte(`{
		"status": "completed",
		"output": [
			{
				"type": "message",
				"role": "assistant",
				"phase": "commentary",
				"content": [{"type": "output_text", "text": "hello"}]
			}
		]
	}`), &resp); err != nil {
		t.Fatalf("json.Unmarshal(response) error = %v", err)
	}

	got := parseResponse(&resp)
	if got.Content != "hello" {
		t.Fatalf("content = %q, want %q", got.Content, "hello")
	}
	if got.Phase != "commentary" {
		t.Fatalf("phase = %q, want %q", got.Phase, "commentary")
	}
	var raw struct {
		Phase string `json:"phase"`
		Role  string `json:"role"`
	}
	if err := json.Unmarshal(got.ProviderRaw, &raw); err != nil {
		t.Fatalf("json.Unmarshal(provider raw) error = %v", err)
	}
	if raw.Role != "assistant" || raw.Phase != "commentary" {
		t.Fatalf("provider raw = %#v, want assistant commentary", raw)
	}
}

func TestBuildRequestParamsSetsToolChoiceAndParallelToolCalls(t *testing.T) {
	params, err := buildRequestParams(
		"gpt-5-codex",
		[]agent.Message{
			{Role: agent.SystemRole, Content: "sys"},
			{Role: agent.UserRole, Content: "hello"},
		},
		[]agent.ToolDefinition{
			{
				Name: "shell",
				Parameters: map[string]any{
					"type": "object",
				},
			},
		},
	)
	if err != nil {
		t.Fatalf("buildRequestParams() error = %v", err)
	}

	body := marshalParamsToMap(t, params)
	instructions, ok := body["instructions"].(string)
	if !ok {
		t.Fatalf("instructions = %#v, want string", body["instructions"])
	}
	for _, want := range []string{
		"sys",
		`provider="openai-codex"`,
		"Brief commentary is allowed",
	} {
		if !strings.Contains(instructions, want) {
			t.Fatalf("instructions missing %q:\n%s", want, instructions)
		}
	}
	if got := body["store"]; got != false {
		t.Fatalf("store = %#v, want false", got)
	}
	if got := body["tool_choice"]; got != "auto" {
		t.Fatalf("tool_choice = %#v, want %q", got, "auto")
	}
	if got := body["parallel_tool_calls"]; got != true {
		t.Fatalf("parallel_tool_calls = %#v, want true", got)
	}
}

func TestBuildRequestParamsUsesDefaultInstructionsWhenMissingSystemMessage(t *testing.T) {
	params, err := buildRequestParams(
		"gpt-5-codex",
		[]agent.Message{
			{Role: agent.UserRole, Content: "hello"},
		},
		nil,
	)
	if err != nil {
		t.Fatalf("buildRequestParams() error = %v", err)
	}

	body := marshalParamsToMap(t, params)
	instructions, ok := body["instructions"].(string)
	if !ok {
		t.Fatalf("instructions = %#v, want string", body["instructions"])
	}
	if !strings.Contains(instructions, agent.DefaultSystemPrompt) {
		t.Fatalf("instructions should include default prompt:\n%s", instructions)
	}
	if !strings.Contains(instructions, `provider="openai-codex"`) {
		t.Fatalf("instructions should include codex prompt profile:\n%s", instructions)
	}
	if _, ok := body["tool_choice"]; ok {
		t.Fatalf(
			"tool_choice should be omitted when no tools are provided: %#v",
			body["tool_choice"],
		)
	}
	if _, ok := body["parallel_tool_calls"]; ok {
		t.Fatalf(
			"parallel_tool_calls should be omitted when no tools are provided: %#v",
			body["parallel_tool_calls"],
		)
	}
}

func TestAppendPromptProfileInstructionsAddsCodexProfile(t *testing.T) {
	got := appendPromptProfileInstructions("base")
	for _, want := range []string{
		"base",
		`provider="openai-codex"`,
		"Brief commentary is allowed",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("instructions missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, `model="`) {
		t.Fatalf("codex profile should not include model identity:\n%s", got)
	}
}

func TestValidateFinalResponse(t *testing.T) {
	tests := []struct {
		name      string
		resp      *responses.Response
		wantErr   bool
		errSubstr string
	}{
		{
			name: "completed",
			resp: &responses.Response{
				Status: responses.ResponseStatusCompleted,
			},
		},
		{
			name: "incomplete",
			resp: &responses.Response{
				Status: responses.ResponseStatusIncomplete,
			},
		},
		{
			name: "failed",
			resp: &responses.Response{
				Status: responses.ResponseStatusFailed,
				Error: responses.ResponseError{
					Message: "boom",
				},
			},
			wantErr:   true,
			errSubstr: "response failed: boom",
		},
		{
			name: "cancelled",
			resp: &responses.Response{
				Status: responses.ResponseStatusCancelled,
			},
			wantErr:   true,
			errSubstr: "response cancelled",
		},
		{
			name: "queued",
			resp: &responses.Response{
				Status: responses.ResponseStatusQueued,
			},
			wantErr:   true,
			errSubstr: "response not finalized",
		},
		{
			name: "in_progress",
			resp: &responses.Response{
				Status: responses.ResponseStatusInProgress,
			},
			wantErr:   true,
			errSubstr: "response not finalized",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateFinalResponse(tc.resp)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("validateFinalResponse() error = nil, want non-nil")
				}
				if tc.errSubstr != "" && !strings.Contains(err.Error(), tc.errSubstr) {
					t.Fatalf("error = %q, want substring %q", err.Error(), tc.errSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateFinalResponse() error = %v, want nil", err)
			}
		})
	}
}

func TestStreamSnapshotRecordsTextRefusalAndToolCalls(t *testing.T) {
	snapshot := newStreamSnapshot()
	snapshot.Record(responses.ResponseStreamEventUnion{
		Type:   "response.output_text.delta",
		ItemID: "msg-1",
		Delta:  "Hello ",
	})
	// Done text for the same item should be ignored when deltas were already seen.
	snapshot.Record(responses.ResponseStreamEventUnion{
		Type:   "response.output_text.done",
		ItemID: "msg-1",
		Text:   "Hello ",
	})
	// Done text for a different item should still be captured.
	snapshot.Record(responses.ResponseStreamEventUnion{
		Type:   "response.output_text.done",
		ItemID: "msg-2",
		Text:   "world",
	})
	snapshot.Record(responses.ResponseStreamEventUnion{
		Type:    "response.refusal.done",
		ItemID:  "ref-1",
		Refusal: "cannot comply",
	})
	snapshot.Record(responses.ResponseStreamEventUnion{
		Type: "response.output_item.done",
		Item: responses.ResponseOutputItemUnion{
			ID:        "fc-1",
			Type:      "function_call",
			CallID:    "call-1",
			Name:      "exec_nix_shell_bash",
			Arguments: `{"command":"pwd"}`,
		},
	})

	if got := snapshot.Text(); got != "Hello world" {
		t.Fatalf("snapshot text = %q, want %q", got, "Hello world")
	}
	if got := snapshot.Refusal(); got != "cannot comply" {
		t.Fatalf("snapshot refusal = %q, want %q", got, "cannot comply")
	}
	if len(snapshot.toolCalls) != 1 {
		t.Fatalf("snapshot tool calls len = %d, want 1", len(snapshot.toolCalls))
	}
	if snapshot.toolCalls[0].ID != "call-1" || snapshot.toolCalls[0].Name != "exec_nix_shell_bash" {
		t.Fatalf("unexpected snapshot tool call: %#v", snapshot.toolCalls[0])
	}
}

func TestMergeResultWithStreamSnapshotUsesSnapshotFallback(t *testing.T) {
	snapshot := newStreamSnapshot()
	snapshot.Record(responses.ResponseStreamEventUnion{
		Type:   "response.output_text.delta",
		ItemID: "msg-1",
		Delta:  "Recovered text",
	})
	snapshot.Record(responses.ResponseStreamEventUnion{
		Type: "response.output_item.done",
		Item: responses.ResponseOutputItemUnion{
			ID:        "fc-1",
			Type:      "function_call",
			CallID:    "call-1",
			Name:      "exec_nix_shell_bash",
			Arguments: `{"command":"pwd"}`,
		},
	})

	result := mergeResultWithStreamSnapshot(agent.ModelClientResult{}, snapshot)
	if result.Content != "Recovered text" {
		t.Fatalf("content = %q, want %q", result.Content, "Recovered text")
	}
	if len(result.ToolCalls) != 1 {
		t.Fatalf("tool calls len = %d, want 1", len(result.ToolCalls))
	}
	if result.FinishReason != "tool_calls" {
		t.Fatalf("finish reason = %q, want %q", result.FinishReason, "tool_calls")
	}
}

func TestMergeResultWithStreamSnapshotPreservesParsedContent(t *testing.T) {
	snapshot := newStreamSnapshot()
	snapshot.Record(responses.ResponseStreamEventUnion{
		Type:   "response.output_text.delta",
		ItemID: "msg-1",
		Delta:  "Ignored",
	})

	result := mergeResultWithStreamSnapshot(agent.ModelClientResult{
		Content:      "parsed",
		FinishReason: "stop",
	}, snapshot)
	if result.Content != "parsed" {
		t.Fatalf("content = %q, want %q", result.Content, "parsed")
	}
	if len(result.ToolCalls) != 0 {
		t.Fatalf("tool calls len = %d, want 0", len(result.ToolCalls))
	}
}

func marshalParamsToMap(t *testing.T, params responses.ResponseNewParams) map[string]any {
	t.Helper()

	data, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("json.Marshal(params) error = %v", err)
	}

	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("json.Unmarshal(params) error = %v", err)
	}
	return out
}
