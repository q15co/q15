package agent

import (
	"encoding/json"
	"fmt"

	"github.com/q15co/q15/systems/agent/internal/conversation"
)

// RequestBudgetError rejects a canonical request before any provider attempt.
// Bytes measures JSON-encoded messages and tool definitions, not token usage.
type RequestBudgetError struct {
	Bytes int
	Limit int
}

func (e *RequestBudgetError) Error() string {
	return fmt.Sprintf("model request exceeds byte budget (%d>%d)", e.Bytes, e.Limit)
}

// RequestEncodedBytes measures the canonical envelope used by the context fitter.
// The value is an encoded-byte allowance, not a provider tokenizer's token count.
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
