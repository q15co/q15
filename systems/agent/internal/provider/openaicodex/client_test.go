package openaicodex

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openai/openai-go/v3/responses"
	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	q15media "github.com/q15co/q15/systems/agent/internal/media"
)

func TestMapMessagesAndTools(t *testing.T) {
	input, instructions, err := mapMessages([]conversation.Message{
		conversation.SystemMessage("sys"),
		conversation.UserMessage("hello"),
		conversation.AssistantMessage(
			conversation.Reasoning("portable summary", map[string]json.RawMessage{
				openAIResponsesReplayKey: json.RawMessage(
					`{"type":"reasoning","encrypted_content":"abc"}`,
				),
			}),
			conversation.Text("calling tool", conversation.TextDispositionCommentary),
			conversation.ToolCall("call-1", "shell", `{"cmd":"pwd"}`),
		),
		conversation.ToolResultMessage("call-1", "ok", false),
	}, nil)
	if err != nil {
		t.Fatalf("mapMessages error = %v", err)
	}
	if instructions != "sys" {
		t.Fatalf("instructions = %q, want %q", instructions, "sys")
	}
	if len(input) != 5 {
		t.Fatalf("input len = %d, want 5", len(input))
	}

	if got := input[0].OfMessage; got == nil || got.Role != responses.EasyInputMessageRoleUser {
		t.Fatalf("input[0] should be user message: %#v", input[0])
	}

	reasoningJSON := marshalInputItemToString(t, input[1])
	if !strings.Contains(reasoningJSON, `"type":"reasoning"`) ||
		!strings.Contains(reasoningJSON, `"encrypted_content":"abc"`) {
		t.Fatalf("input[1] should be reasoning replay item: %s", reasoningJSON)
	}

	assistantJSON := marshalInputItemToString(t, input[2])
	for _, want := range []string{
		`"type":"message"`,
		`"role":"assistant"`,
		`"phase":"commentary"`,
		`"text":"calling tool"`,
	} {
		if !strings.Contains(assistantJSON, want) {
			t.Fatalf("input[2] missing %q: %s", want, assistantJSON)
		}
	}

	if got := input[3].OfFunctionCall; got == nil || got.CallID != "call-1" {
		t.Fatalf("input[3] should be function call with call-1: %#v", input[3])
	}
	if got := input[4].OfFunctionCallOutput; got == nil || got.CallID != "call-1" {
		t.Fatalf("input[4] should be function call output with call-1: %#v", input[4])
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
	_, instructions, err := mapMessages([]conversation.Message{
		conversation.SystemMessage("base"),
		conversation.UserMessage("hello"),
		conversation.SystemMessage("steering"),
	}, nil)
	if err != nil {
		t.Fatalf("mapMessages() error = %v", err)
	}
	if instructions != "base\n\nsteering" {
		t.Fatalf("instructions = %q, want %q", instructions, "base\n\nsteering")
	}
}

func TestMapMessagesPreservesAssistantDispositionOnReplay(t *testing.T) {
	input, _, err := mapMessages([]conversation.Message{
		conversation.AssistantMessage(
			conversation.Text("resumed assistant message", conversation.TextDispositionCommentary),
		),
	}, nil)
	if err != nil {
		t.Fatalf("mapMessages() error = %v", err)
	}
	if len(input) != 1 {
		t.Fatalf("input len = %d, want 1", len(input))
	}

	body := marshalInputItemToString(t, input[0])
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

func TestParseResponseContentToolCallsReasoningAndPhase(t *testing.T) {
	var resp responses.Response
	if err := json.Unmarshal([]byte(`{
		"status": "completed",
		"output": [
			{
				"type": "reasoning",
				"id": "rs_123",
				"encrypted_content": "abc",
				"summary": [{"type": "summary_text", "text": "portable summary"}]
			},
			{
				"type": "message",
				"role": "assistant",
				"phase": "commentary",
				"content": [{"type": "output_text", "text": "hello"}]
			},
			{
				"type": "function_call",
				"call_id": "call-1",
				"name": "shell",
				"arguments": "{\"cmd\":\"ls\"}"
			}
		]
	}`), &resp); err != nil {
		t.Fatalf("json.Unmarshal(response) error = %v", err)
	}

	got := parseResponse(&resp)
	if len(got.Messages) != 3 {
		t.Fatalf("messages len = %d, want 3", len(got.Messages))
	}
	if got.Messages[0].Parts[0].Type != conversation.ReasoningPartType ||
		got.Messages[0].Parts[0].Text != "portable summary" {
		t.Fatalf("reasoning message = %#v", got.Messages[0])
	}
	if string(got.Messages[0].Parts[0].Replay[openAIResponsesReplayKey]) == "" {
		t.Fatalf("reasoning replay missing: %#v", got.Messages[0].Parts[0])
	}
	if got.Messages[1].Parts[0].Type != conversation.TextPartType ||
		got.Messages[1].Parts[0].Disposition != conversation.TextDispositionCommentary {
		t.Fatalf("assistant text message = %#v", got.Messages[1])
	}
	if conversation.FinalAnswer(got.Messages) != "" {
		t.Fatalf(
			"FinalAnswer() = %q, want empty for commentary-only text",
			conversation.FinalAnswer(got.Messages),
		)
	}
	calls := conversation.ToolCalls(got.Messages)
	if len(calls) != 1 {
		t.Fatalf("tool calls len = %d, want 1", len(calls))
	}
	if calls[0].ID != "call-1" || calls[0].Name != "shell" {
		t.Fatalf("unexpected tool call: %#v", calls[0])
	}
	if got.FinishReason != "tool_calls" {
		t.Fatalf("finish reason = %q, want %q", got.FinishReason, "tool_calls")
	}
}

func TestMapMessagesBuildsMultipartUserInputForImageInput(t *testing.T) {
	store, ref, rawImage := mustStoreTestImage(t)

	input, _, err := mapMessages([]conversation.Message{
		conversation.UserMessageParts(
			conversation.Text("describe this screenshot", ""),
			conversation.Image(ref, ""),
		),
	}, store)
	if err != nil {
		t.Fatalf("mapMessages() error = %v", err)
	}
	if len(input) != 1 {
		t.Fatalf("input len = %d, want 1", len(input))
	}

	body := marshalInputItemToString(t, input[0])
	wantDataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(rawImage)
	for _, want := range []string{
		`"role":"user"`,
		`"type":"input_text"`,
		`"text":"describe this screenshot"`,
		`"type":"input_image"`,
		wantDataURL,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("serialized input missing %q: %s", want, body)
		}
	}
}

func TestMapMessagesRejectsInvalidImageInput(t *testing.T) {
	store, err := q15media.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	_, _, err = mapMessages([]conversation.Message{
		conversation.UserMessageParts(conversation.Image("media://sha256/not-real", "")),
	}, store)
	if err == nil || !strings.Contains(err.Error(), "resolve image input") {
		t.Fatalf("mapMessages() error = %v, want image resolution failure", err)
	}
}

func TestMapMessagesAddsVisionFollowupForToolProducedImage(t *testing.T) {
	store, ref, _ := mustStoreTestImage(t)

	input, _, err := mapMessages([]conversation.Message{
		{
			Role: conversation.ToolRole,
			Parts: []conversation.Part{
				conversation.ToolResult("call-1", "captured screenshot", false),
				conversation.Image(ref, ""),
			},
		},
	}, store)
	if err != nil {
		t.Fatalf("mapMessages() error = %v", err)
	}
	if len(input) != 2 {
		t.Fatalf("input len = %d, want 2", len(input))
	}

	toolJSON := marshalInputItemToString(t, input[0])
	if !strings.Contains(toolJSON, `"call_id":"call-1"`) {
		t.Fatalf("tool output item = %s", toolJSON)
	}

	followupJSON := marshalInputItemToString(t, input[1])
	for _, want := range []string{
		`"role":"user"`,
		toolImageFollowupText,
		`"type":"input_image"`,
	} {
		if !strings.Contains(followupJSON, want) {
			t.Fatalf("followup missing %q: %s", want, followupJSON)
		}
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
	if conversation.FinalAnswer(got.Messages) != "partial" {
		t.Fatalf("FinalAnswer() = %q, want %q", conversation.FinalAnswer(got.Messages), "partial")
	}
}

func TestMergeResultWithStreamSnapshotFillsMissingReasoningSummary(t *testing.T) {
	snapshot := newStreamSnapshot()

	var evt responses.ResponseStreamEventUnion
	if err := json.Unmarshal([]byte(`{
		"type": "response.reasoning_summary_text.done",
		"item_id": "rs_123",
		"output_index": 0,
		"sequence_number": 1,
		"summary_index": 0,
		"text": "portable summary from stream"
	}`), &evt); err != nil {
		t.Fatalf("json.Unmarshal(event) error = %v", err)
	}
	snapshot.Record(evt)

	result := agent.ModelClientResult{
		Messages: []conversation.Message{
			conversation.AssistantMessage(
				conversation.Reasoning("", map[string]json.RawMessage{
					openAIResponsesReplayKey: json.RawMessage(
						`{"id":"rs_123","type":"reasoning","encrypted_content":"abc","summary":[]}`,
					),
				}),
			),
		},
	}

	got := mergeResultWithStreamSnapshot(result, snapshot)
	if len(got.Messages) != 1 || len(got.Messages[0].Parts) != 1 {
		t.Fatalf("messages = %#v", got.Messages)
	}
	if got.Messages[0].Parts[0].Text != "portable summary from stream" {
		t.Fatalf(
			"reasoning text = %q, want %q",
			got.Messages[0].Parts[0].Text,
			"portable summary from stream",
		)
	}
}

func TestBuildRequestParamsSetsToolChoiceParallelCallsAndReasoningConfig(t *testing.T) {
	params, err := buildRequestParams(
		"gpt-5-codex",
		[]conversation.Message{
			conversation.SystemMessage("sys"),
			conversation.UserMessage("hello"),
		},
		[]agent.ToolDefinition{
			{
				Name: "shell",
				Parameters: map[string]any{
					"type": "object",
				},
			},
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
	include, ok := body["include"].([]any)
	if !ok || len(include) != 1 || include[0] != "reasoning.encrypted_content" {
		t.Fatalf("include = %#v, want reasoning.encrypted_content", body["include"])
	}
	reasoning, ok := body["reasoning"].(map[string]any)
	if !ok {
		t.Fatalf("reasoning = %#v, want object", body["reasoning"])
	}
	if got := reasoning["summary"]; got != "auto" {
		t.Fatalf("reasoning.summary = %#v, want %q", got, "auto")
	}
}

func TestBuildRequestParamsUsesDefaultInstructionsWhenMissingSystemMessage(t *testing.T) {
	params, err := buildRequestParams(
		"gpt-5-codex",
		[]conversation.Message{
			conversation.UserMessage("hello"),
		},
		nil,
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
	snapshot.Record(responses.ResponseStreamEventUnion{
		Type:   "response.output_text.done",
		ItemID: "msg-1",
		Text:   "Hello ",
	})
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
			Name:      "exec",
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
	if snapshot.toolCalls[0].ID != "call-1" || snapshot.toolCalls[0].Name != "exec" {
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
			Name:      "exec",
			Arguments: `{"command":"pwd"}`,
		},
	})

	result := mergeResultWithStreamSnapshot(agent.ModelClientResult{}, snapshot)
	if conversation.FinalAnswer(result.Messages) != "Recovered text" {
		t.Fatalf(
			"FinalAnswer() = %q, want %q",
			conversation.FinalAnswer(result.Messages),
			"Recovered text",
		)
	}
	if len(conversation.ToolCalls(result.Messages)) != 1 {
		t.Fatalf("tool calls len = %d, want 1", len(conversation.ToolCalls(result.Messages)))
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
		Messages: []conversation.Message{
			conversation.AssistantMessage(conversation.Text("parsed", "")),
		},
		FinishReason: "stop",
	}, snapshot)
	if conversation.FinalAnswer(result.Messages) != "parsed" {
		t.Fatalf("FinalAnswer() = %q, want %q", conversation.FinalAnswer(result.Messages), "parsed")
	}
	if len(conversation.ToolCalls(result.Messages)) != 0 {
		t.Fatalf("tool calls len = %d, want 0", len(conversation.ToolCalls(result.Messages)))
	}
}

func marshalInputItemToString(t *testing.T, item responses.ResponseInputItemUnionParam) string {
	t.Helper()

	data, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("json.Marshal(item) error = %v", err)
	}
	return string(data)
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

func mustStoreTestImage(t *testing.T) (*q15media.FileStore, string, []byte) {
	t.Helper()

	store, err := q15media.NewFileStore(filepath.Join(t.TempDir(), "media"))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	rawImage := []byte{
		0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n',
		0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R',
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
		0x89, 0x00, 0x00, 0x00, 0x0d, 'I', 'D', 'A', 'T',
		0x78, 0x9c, 0x63, 0xf8, 0xcf, 0xc0, 0x00, 0x00,
		0x03, 0x01, 0x01, 0x00, 0xc9, 0xfe, 0x92, 0xef,
		0x00, 0x00, 0x00, 0x00, 'I', 'E', 'N', 'D',
		0xae, 0x42, 0x60, 0x82,
	}
	imagePath := filepath.Join(t.TempDir(), "image.png")
	if err := os.WriteFile(imagePath, rawImage, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	ref, err := store.Store(imagePath, q15media.Meta{ContentType: "image/png"}, "test")
	if err != nil {
		t.Fatalf("Store() error = %v", err)
	}
	return store, ref, rawImage
}
