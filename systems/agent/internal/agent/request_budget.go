package agent

import (
	"encoding/json"
	"fmt"

	"github.com/q15co/q15/systems/agent/internal/conversation"
)

// RequestBudgetError rejects a canonical request before any provider attempt.
// Bytes measures JSON-encoded messages and tool definitions, not token usage.
type RequestBudgetError struct{ Bytes, Limit int }

func (e *RequestBudgetError) Error() string {
	return fmt.Sprintf("model request exceeds byte budget (%d>%d)", e.Bytes, e.Limit)
}

type requestSize struct{ messages, tools, total int }

func measureRequest(messages []conversation.Message, tools []ToolDefinition) (requestSize, error) {
	messageJSON, err := json.Marshal(messages)
	if err != nil {
		return requestSize{}, fmt.Errorf("measure model messages: %w", err)
	}
	toolJSON, err := json.Marshal(tools)
	if err != nil {
		return requestSize{}, fmt.Errorf("measure model tools: %w", err)
	}
	return requestSize{
		messages: len(messageJSON),
		tools:    len(toolJSON),
		total:    len(messageJSON) + len(toolJSON) + len(`{"messages":,"tools":}`),
	}, nil
}

func checkRequestBudget(
	messages []conversation.Message,
	tools []ToolDefinition,
	limit int,
) (requestSize, error) {
	if limit <= 0 {
		return requestSize{}, nil
	}
	size, err := measureRequest(messages, tools)
	if err != nil {
		return size, err
	}
	if size.total > limit {
		return size, &RequestBudgetError{Bytes: size.total, Limit: limit}
	}
	return size, nil
}

func boundToolResult(
	messages []conversation.Message,
	tools []ToolDefinition,
	call ToolCall,
	result ToolResult,
	runErr error,
	limit int,
) (ToolResult, error) {
	if limit <= 0 {
		return result, runErr
	}
	message := toolResultMessage(call.ID, result, runErr != nil)
	encoded, err := json.Marshal(message)
	if err != nil {
		return ToolResult{}, err
	}
	size, err := measureRequest(append(copyMessages(messages), message), tools)
	if err != nil {
		return ToolResult{}, err
	}
	// Reserve an eighth for assistant decisions and small error results after the
	// evidence allowance is exhausted. No previously observed evidence is removed.
	allowance := limit / 8
	if len(encoded) <= allowance && size.total <= limit-allowance {
		return result, runErr
	}
	err = fmt.Errorf(
		"tool executed; result omitted because it exceeds the request evidence byte budget; use a narrower query or page, or finish from the supplied evidence; do not repeat a mutation merely because its output was omitted",
	)
	return ToolResult{Output: "tool error: " + err.Error()}, err
}
