package agent

import (
	"encoding/json"
	"fmt"

	"github.com/q15co/q15/systems/agent/internal/conversation"
)

// RequestBudgetError rejects a canonical request before any provider attempt.
// Bytes measures JSON-encoded messages and tool definitions, not token usage.
type RequestBudgetError struct {
	Bytes               int
	Limit               int
	PromptBytes         int
	InputBytes          int
	PriorTurnBytes      int
	EvidenceBytes       int
	ToolDefinitionBytes int
	FramingBytes        int
}

func (e *RequestBudgetError) Error() string {
	return fmt.Sprintf(
		"model request exceeds byte budget (%d>%d): prompt_bytes=%d input_bytes=%d prior_turn_bytes=%d evidence_bytes=%d tool_definition_bytes=%d framing_bytes=%d",
		e.Bytes,
		e.Limit,
		e.PromptBytes,
		e.InputBytes,
		e.PriorTurnBytes,
		e.EvidenceBytes,
		e.ToolDefinitionBytes,
		e.FramingBytes,
	)
}

type requestSize struct {
	messages, tools, total                  int
	prompt, input, prior, evidence, framing int
}

func measureRequest(messages []conversation.Message, tools []ToolDefinition) (requestSize, error) {
	messageJSON, err := json.Marshal(messages)
	if err != nil {
		return requestSize{}, fmt.Errorf("measure model messages: %w", err)
	}
	toolJSON, err := json.Marshal(tools)
	if err != nil {
		return requestSize{}, fmt.Errorf("measure model tools: %w", err)
	}
	size := requestSize{
		messages: len(messageJSON),
		tools:    len(toolJSON),
		total:    len(messageJSON) + len(toolJSON) + len(`{"messages":,"tools":}`),
	}
	for _, message := range messages {
		encoded, err := json.Marshal(message)
		if err != nil {
			return requestSize{}, err
		}
		switch message.Role {
		case conversation.SystemRole:
			size.prompt += len(encoded)
		case conversation.ToolRole:
			size.evidence += len(encoded)
		case conversation.AssistantRole:
			size.prior += len(encoded)
		default:
			size.input += len(encoded)
		}
	}
	size.framing = size.total - size.prompt - size.input - size.prior - size.evidence - size.tools
	return size, nil
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
		return size, &RequestBudgetError{
			Bytes: size.total, Limit: limit,
			PromptBytes: size.prompt, InputBytes: size.input, PriorTurnBytes: size.prior,
			EvidenceBytes: size.evidence, ToolDefinitionBytes: size.tools, FramingBytes: size.framing,
		}
	}
	return size, nil
}

const omittedToolEvidence = "tool executed; result omitted because it exceeds the request evidence byte budget; use a narrower query or page, or finish from the supplied evidence; do not repeat a mutation merely because its output was omitted"

// fitRequestBudget changes only the provider's copy of optional evidence. Source
// messages, tool-call arguments, replay state and the recorded run stay intact.
func fitRequestBudget(
	messages []conversation.Message,
	tools []ToolDefinition,
	limit, start int,
) (requestSize, error) {
	size, err := checkRequestBudget(messages, tools, limit)
	if err == nil || size.total == 0 {
		return size, err
	}
	for i := start; i < len(messages); i++ {
		for j := range messages[i].Parts {
			part := &messages[i].Parts[j]
			if part.Type != conversation.ToolResultPartType ||
				len(part.Content) <= len(omittedToolEvidence) {
				continue
			}
			part.Content = omittedToolEvidence
			part.IsError = true
			size, err = checkRequestBudget(messages, tools, limit)
			if err == nil {
				return size, nil
			}
		}
	}
	return size, err
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
	// evidence allowance is exhausted.
	allowance := limit / 8
	if len(encoded) <= min(32*1024, allowance) && size.total <= limit-allowance {
		return result, runErr
	}
	err = fmt.Errorf("%s", omittedToolEvidence)
	return ToolResult{Output: "tool error: " + err.Error()}, err
}
