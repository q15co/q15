package conversation

import (
	"encoding/json"
	"testing"
)

func TestNormalizeMessagesDropsEmptyAndNormalizesFields(t *testing.T) {
	input := []Message{
		{
			Role: AssistantRole,
			Parts: []Part{
				{Type: TextPartType, Text: "hello", Disposition: "final"},
				{Type: ToolCallPartType, ID: " call-1 ", Name: " echo ", Arguments: ""},
				{
					Type: ReasoningPartType,
					Replay: map[string]json.RawMessage{
						"":                 json.RawMessage(`"ignored"`),
						"openai_responses": json.RawMessage(`{"id":"r1"}`),
					},
				},
				{Type: ToolResultPartType},
				{Type: "unknown"},
			},
		},
	}

	got := NormalizeMessages(input)
	if len(got) != 1 {
		t.Fatalf("NormalizeMessages len = %d, want 1", len(got))
	}
	if len(got[0].Parts) != 3 {
		t.Fatalf("NormalizeMessages parts len = %d, want 3", len(got[0].Parts))
	}
	if got[0].Parts[0].Disposition != TextDispositionFinal {
		t.Fatalf(
			"text disposition = %q, want %q",
			got[0].Parts[0].Disposition,
			TextDispositionFinal,
		)
	}
	if got[0].Parts[1].ID != "call-1" || got[0].Parts[1].Name != "echo" ||
		got[0].Parts[1].Arguments != "{}" {
		t.Fatalf("tool call normalization = %#v", got[0].Parts[1])
	}
	if len(got[0].Parts[2].Replay) != 1 {
		t.Fatalf("reasoning replay len = %d, want 1", len(got[0].Parts[2].Replay))
	}
}

func TestCloneMessagesDeepCopiesReasoningReplay(t *testing.T) {
	input := []Message{
		AssistantMessage(Reasoning("summary", map[string]json.RawMessage{
			"openai_responses": json.RawMessage(`{"id":"r1"}`),
		})),
	}

	cloned := CloneMessages(input)
	cloned[0].Parts[0].Replay["openai_responses"][0] = '['

	if string(input[0].Parts[0].Replay["openai_responses"]) != `{"id":"r1"}` {
		t.Fatalf("original replay mutated = %s", input[0].Parts[0].Replay["openai_responses"])
	}
}

func TestToolCallsReturnsOrderedToolCallParts(t *testing.T) {
	messages := []Message{
		AssistantMessage(
			Text("thinking", TextDispositionCommentary),
			ToolCall("call-1", "shell", `{"cmd":"pwd"}`),
		),
		AssistantMessage(
			Reasoning("summary", nil),
			ToolCall("call-2", "read_file", `{"path":"README.md"}`),
		),
	}

	got := ToolCalls(messages)
	if len(got) != 2 {
		t.Fatalf("ToolCalls len = %d, want 2", len(got))
	}
	if got[0].ID != "call-1" || got[1].ID != "call-2" {
		t.Fatalf("ToolCalls order = %#v", got)
	}
}

func TestFinalAnswerPrefersFinalThenPlainOverCommentary(t *testing.T) {
	t.Run("prefers final disposition", func(t *testing.T) {
		got := FinalAnswer([]Message{
			AssistantMessage(Text("thinking", TextDispositionCommentary)),
			AssistantMessage(Text("done", TextDispositionFinal)),
		})
		if got != "done" {
			t.Fatalf("FinalAnswer() = %q, want %q", got, "done")
		}
	})

	t.Run("falls back to plain text", func(t *testing.T) {
		got := FinalAnswer([]Message{
			AssistantMessage(Text("thinking", TextDispositionCommentary)),
			AssistantMessage(Text("plain", "")),
		})
		if got != "plain" {
			t.Fatalf("FinalAnswer() = %q, want %q", got, "plain")
		}
	})
}

func TestMessageJSONRoundTripPreservesReasoningReplay(t *testing.T) {
	input := Message{
		Role: AssistantRole,
		Parts: []Part{
			Reasoning("summary", map[string]json.RawMessage{
				"openai_responses": json.RawMessage(`{"id":"rs_123","encrypted_content":"abc"}`),
			}),
			Text("final answer", TextDispositionFinal),
		},
	}

	data, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var got Message
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if got.Role != AssistantRole {
		t.Fatalf("role = %q, want assistant", got.Role)
	}
	if got.Parts[0].Type != ReasoningPartType ||
		string(
			got.Parts[0].Replay["openai_responses"],
		) != `{"id":"rs_123","encrypted_content":"abc"}` {
		t.Fatalf("reasoning replay = %#v", got.Parts[0])
	}
	if got.Parts[1].Disposition != TextDispositionFinal {
		t.Fatalf("text disposition = %q, want %q", got.Parts[1].Disposition, TextDispositionFinal)
	}
}
