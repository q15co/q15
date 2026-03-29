package openaicompatible

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/openai/openai-go/v3"
	"github.com/q15co/q15/systems/agent/internal/conversation"
)

func TestMapMessagesBuildsAssistantReplayWithReasoningAndTools(t *testing.T) {
	messages, err := mapMessages([]conversation.Message{
		conversation.AssistantMessage(
			conversation.Reasoning("portable summary", map[string]json.RawMessage{
				openAICompatibleReplayKey: json.RawMessage(`{"reasoning_opaque":"opaque-token"}`),
			}),
			conversation.Text("hello", ""),
			conversation.ToolCall("call-1", "shell", `{"cmd":"pwd"}`),
		),
	})
	if err != nil {
		t.Fatalf("mapMessages error = %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(messages))
	}

	data, err := json.Marshal(messages[0])
	if err != nil {
		t.Fatalf("json.Marshal(messages[0]) error = %v", err)
	}
	body := string(data)
	for _, want := range []string{
		`"role":"assistant"`,
		`"content":"hello"`,
		`"reasoning_content":"portable summary"`,
		`"reasoning_opaque":"opaque-token"`,
		`"tool_calls":[`,
		`"name":"shell"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("serialized replay missing %q: %s", want, body)
		}
	}
}

func TestMapMessagesUsesPortableReasoningTextAcrossProviderFallback(t *testing.T) {
	messages, err := mapMessages([]conversation.Message{
		conversation.AssistantMessage(
			conversation.Reasoning("portable summary", map[string]json.RawMessage{
				"openai_responses": json.RawMessage(
					`{"type":"reasoning","encrypted_content":"abc"}`,
				),
			}),
		),
	})
	if err != nil {
		t.Fatalf("mapMessages error = %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(messages))
	}

	data, err := json.Marshal(messages[0])
	if err != nil {
		t.Fatalf("json.Marshal(messages[0]) error = %v", err)
	}
	body := string(data)
	if !strings.Contains(body, `"reasoning_content":"portable summary"`) {
		t.Fatalf("serialized replay missing portable reasoning: %s", body)
	}
	if strings.Contains(body, "encrypted_content") {
		t.Fatalf("serialized replay should not include unmatched opaque replay: %s", body)
	}
}

func TestMapMessagesCoalescesContiguousAssistantMessagesForToolReplay(t *testing.T) {
	messages, err := mapMessages([]conversation.Message{
		conversation.AssistantMessage(
			conversation.Reasoning("portable summary", map[string]json.RawMessage{
				"openai_responses": json.RawMessage(
					`{"type":"reasoning","encrypted_content":"abc"}`,
				),
			}),
		),
		conversation.AssistantMessage(
			conversation.ToolCall("call-1", "shell", `{"cmd":"pwd"}`),
		),
	})
	if err != nil {
		t.Fatalf("mapMessages error = %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(messages))
	}

	data, err := json.Marshal(messages[0])
	if err != nil {
		t.Fatalf("json.Marshal(messages[0]) error = %v", err)
	}
	body := string(data)
	for _, want := range []string{
		`"role":"assistant"`,
		`"reasoning_content":"portable summary"`,
		`"tool_calls":[`,
		`"name":"shell"`,
		`"arguments":"{\"cmd\":\"pwd\"}"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("serialized replay missing %q: %s", want, body)
		}
	}
}

func TestMapMessagesSynthesizesReasoningContentForOpaqueToolReplay(t *testing.T) {
	messages, err := mapMessages([]conversation.Message{
		conversation.AssistantMessage(
			conversation.Reasoning("", map[string]json.RawMessage{
				"openai_responses": json.RawMessage(
					`{"id":"rs_123","type":"reasoning","encrypted_content":"abc","summary":[]}`,
				),
			}),
		),
		conversation.AssistantMessage(
			conversation.ToolCall("call-1", "shell", `{"cmd":"pwd"}`),
		),
	})
	if err != nil {
		t.Fatalf("mapMessages error = %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(messages))
	}

	data, err := json.Marshal(messages[0])
	if err != nil {
		t.Fatalf("json.Marshal(messages[0]) error = %v", err)
	}
	body := string(data)
	for _, want := range []string{
		`"role":"assistant"`,
		`"reasoning_content":"` + synthesizedReasoningContent + `"`,
		`"tool_calls":[`,
		`"name":"shell"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("serialized replay missing %q: %s", want, body)
		}
	}
}

func TestMapMessagesSynthesizesReasoningContentForToolReplayWithoutReasoningPart(t *testing.T) {
	messages, err := mapMessages([]conversation.Message{
		conversation.AssistantMessage(
			conversation.ToolCall("call-1", "shell", `{"cmd":"pwd"}`),
		),
	})
	if err != nil {
		t.Fatalf("mapMessages error = %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(messages))
	}

	data, err := json.Marshal(messages[0])
	if err != nil {
		t.Fatalf("json.Marshal(messages[0]) error = %v", err)
	}
	body := string(data)
	for _, want := range []string{
		`"role":"assistant"`,
		`"reasoning_content":"` + synthesizedReasoningContent + `"`,
		`"tool_calls":[`,
		`"name":"shell"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("serialized replay missing %q: %s", want, body)
		}
	}
}

func TestParseAssistantMessageExtractsReasoningAndTools(t *testing.T) {
	raw := json.RawMessage(`{
		"role": "assistant",
		"content": "hello",
		"reasoning_content": "portable summary",
		"reasoning_opaque": "opaque-token"
	}`)

	var toolCalls []openai.ChatCompletionMessageToolCallUnion
	if err := json.Unmarshal([]byte(`[
		{
			"id": "call-1",
			"type": "function",
			"function": {
				"name": "shell",
				"arguments": "{\"cmd\":\"pwd\"}"
			}
		}
	]`), &toolCalls); err != nil {
		t.Fatalf("json.Unmarshal(toolCalls) error = %v", err)
	}

	got, err := parseAssistantMessage(raw, toolCalls)
	if err != nil {
		t.Fatalf("parseAssistantMessage() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("messages len = %d, want 1", len(got))
	}
	if got[0].Role != conversation.AssistantRole {
		t.Fatalf("role = %q, want assistant", got[0].Role)
	}
	if len(got[0].Parts) != 3 {
		t.Fatalf("parts len = %d, want 3", len(got[0].Parts))
	}
	if got[0].Parts[0].Type != conversation.ReasoningPartType ||
		got[0].Parts[0].Text != "portable summary" {
		t.Fatalf("reasoning part = %#v", got[0].Parts[0])
	}
	if string(
		got[0].Parts[0].Replay[openAICompatibleReplayKey],
	) != `{"reasoning_opaque":"opaque-token"}` {
		t.Fatalf("reasoning replay = %s", got[0].Parts[0].Replay[openAICompatibleReplayKey])
	}
	if got[0].Parts[1].Type != conversation.TextPartType || got[0].Parts[1].Text != "hello" {
		t.Fatalf("text part = %#v", got[0].Parts[1])
	}
	if got[0].Parts[2].Type != conversation.ToolCallPartType || got[0].Parts[2].Name != "shell" {
		t.Fatalf("tool call part = %#v", got[0].Parts[2])
	}
}

func TestWithPromptProfileAppendsOpenAICompatibleProfile(t *testing.T) {
	base := []conversation.Message{
		conversation.SystemMessage("base"),
		conversation.UserMessage("hello"),
	}

	tuned := withPromptProfile(base)
	if len(tuned) != len(base)+1 {
		t.Fatalf("tuned len = %d, want %d", len(tuned), len(base)+1)
	}
	last := tuned[len(tuned)-1]
	if last.Role != conversation.SystemRole {
		t.Fatalf("last role = %q, want system", last.Role)
	}
	lastText := conversation.TextValue(last)
	for _, want := range []string{
		`provider="openai-compatible"`,
		"does not preserve assistant commentary disposition metadata",
	} {
		if !strings.Contains(lastText, want) {
			t.Fatalf("profile missing %q:\n%s", want, lastText)
		}
	}
	if strings.Contains(lastText, `model="`) {
		t.Fatalf("profile should not depend on model name:\n%s", lastText)
	}
}
