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

// RequestEncodedBytes measures the same canonical envelope as the engine's guard.
// The value is an encoded-byte allowance, not a provider tokenizer's token count.
func RequestEncodedBytes(messages []conversation.Message, tools []ToolDefinition) (int, error) {
	size, err := measureRequest(messages, tools)
	return size.total, err
}

// turnReplyReserveDivisor reserves one eighth of the request budget for the
// upcoming assistant reply and fresh evidence shells. boundToolResult enforces
// the same eighth when it keeps a fresh tool result.
const turnReplyReserveDivisor = 8

// requestTurnLimit is the per-turn preflight ceiling: the hard request budget
// minus the reply reserve. A turn that cannot leave room for its own reply is
// rejected before the provider call instead of after an expensive round trip.
func requestTurnLimit(limit int) int {
	if limit <= 0 {
		return limit
	}
	return limit - limit/turnReplyReserveDivisor
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

const omittedPriorTurnReasoning = "reasoning omitted because earlier turns accumulated past the request byte budget; the recorded run retains the original reasoning"

const omittedPriorTurnText = "assistant text omitted because earlier turns accumulated past the request byte budget; the recorded run retains the original text"

// omittedToolCallArguments stays a JSON object so providers that decode
// tool-call arguments into a map keep accepting the call.
const omittedToolCallArguments = `{"error":"tool call arguments omitted because earlier turns accumulated past the request byte budget; the recorded run retains the original arguments; do not repeat a mutation merely because its arguments were omitted"}`

// fitRequestBudget changes only the provider's copy of replay content. Source
// messages, tool-call arguments, replay state and the recorded run stay intact.
// Tool-result evidence is omitted first, oldest result first; when that alone
// cannot fit the request, prior assistant turns are compacted the same way,
// oldest turn first, and only then is the request rejected.
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
	for i := start; i < len(messages); i++ {
		if messages[i].Role != conversation.AssistantRole {
			continue
		}
		for j := range messages[i].Parts {
			part := &messages[i].Parts[j]
			switch part.Type {
			case conversation.ReasoningPartType:
				if len(part.Text) <= len(omittedPriorTurnReasoning) &&
					len(part.Replay) == 0 {
					continue
				}
				part.Text = omittedPriorTurnReasoning
				part.Replay = nil
			case conversation.ToolCallPartType:
				if len(part.Arguments) <= len(omittedToolCallArguments) {
					continue
				}
				part.Arguments = omittedToolCallArguments
			case conversation.TextPartType:
				if len(part.Text) <= len(omittedPriorTurnText) {
					continue
				}
				part.Text = omittedPriorTurnText
			default:
				continue
			}
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
	allowance := limit / turnReplyReserveDivisor
	if len(encoded) <= min(32*1024, allowance) && size.total <= limit-allowance {
		return result, runErr
	}
	err = fmt.Errorf("%s", omittedToolEvidence)
	return ToolResult{Output: "tool error: " + err.Error()}, err
}
