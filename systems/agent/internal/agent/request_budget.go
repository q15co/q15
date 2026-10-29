package agent

import (
	"encoding/json"
	"fmt"

	"github.com/q15co/q15/systems/agent/internal/conversation"
)

// RequestEncodedBytes measures canonical messages and tools for request diagnostics.
// The value is an encoded byte count, not a provider tokenizer's token count.
func RequestEncodedBytes(messages []conversation.Message, tools []ToolDefinition) (int, error) {
	messageJSON, err := json.Marshal(messages)
	if err != nil {
		return 0, fmt.Errorf("measure model messages: %w", err)
	}
	toolJSON, err := json.Marshal(tools)
	if err != nil {
		return 0, fmt.Errorf("measure model tools: %w", err)
	}
	return len(messageJSON) + len(toolJSON) + len(`{"messages":,"tools":}`), nil
}
