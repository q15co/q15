package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/conversation"
)

func TestRequestEncodedBytesIncludesDefinitionsReplayAndEscaping(t *testing.T) {
	for _, tc := range []struct {
		name     string
		messages []conversation.Message
		tools    []ToolDefinition
	}{
		{name: "empty"},
		{name: "text", messages: []conversation.Message{conversation.UserMessage(strings.Repeat("x", 1024))}},
		{name: "escaping", messages: []conversation.Message{conversation.UserMessage(strings.Repeat("\x01", 256))}},
		{name: "tools", tools: []ToolDefinition{{Name: "test", Description: strings.Repeat("x", 1024)}}},
		{name: "replay", messages: []conversation.Message{{Role: conversation.AssistantRole, Parts: []conversation.Part{{Type: conversation.ReasoningPartType, Text: "synthetic", Replay: map[string]json.RawMessage{"synthetic": json.RawMessage(`"` + strings.Repeat("x", 1024) + `"`)}}}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := json.Marshal(struct {
				Messages []conversation.Message `json:"messages"`
				Tools    []ToolDefinition       `json:"tools"`
			}{Messages: tc.messages, Tools: tc.tools})
			if err != nil {
				t.Fatal(err)
			}
			measured, err := RequestEncodedBytes(tc.messages, tc.tools)
			if err != nil || measured != len(encoded) {
				t.Fatalf("request bytes=%d, encoded=%d, error=%v", measured, len(encoded), err)
			}
		})
	}
}

func TestRequestEncodedBytesRejectsInvalidEnvelope(t *testing.T) {
	invalidReplay := []conversation.Message{
		conversation.AssistantMessage(
			conversation.Reasoning(
				"",
				map[string]json.RawMessage{"provider": json.RawMessage(`invalid JSON`)},
			),
		),
	}
	if _, err := RequestEncodedBytes(invalidReplay, nil); err == nil {
		t.Fatal("invalid replay JSON was not rejected")
	}
	tools := []ToolDefinition{{Name: "test", Parameters: map[string]any{"invalid": make(chan int)}}}
	if _, err := RequestEncodedBytes(nil, tools); err == nil {
		t.Fatal("invalid tool schema was not rejected")
	}
}
