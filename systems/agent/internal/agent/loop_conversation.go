package agent

import (
	"fmt"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/conversation"
)

func systemMessage(text string) conversation.Message {
	return conversation.SystemMessage(text)
}

func normalizeUserMessage(msg conversation.Message) (conversation.Message, error) {
	msg = conversation.NormalizeMessage(msg)
	if msg.Role != conversation.UserRole {
		return conversation.Message{}, fmt.Errorf(
			"user message role must be %q",
			conversation.UserRole,
		)
	}

	hasInput := false
	for _, part := range msg.Parts {
		switch part.Type {
		case conversation.TextPartType:
			hasInput = true
		case conversation.ImagePartType:
			hasInput = true
		default:
			return conversation.Message{}, fmt.Errorf(
				"unsupported user message part type %q",
				part.Type,
			)
		}
	}
	if !hasInput {
		return conversation.Message{}, fmt.Errorf("empty user input")
	}
	return msg, nil
}

func assistantTextMessage(
	text string,
	disposition conversation.TextDisposition,
) conversation.Message {
	if strings.TrimSpace(text) == "" {
		return conversation.AssistantMessage()
	}
	return conversation.AssistantMessage(conversation.Text(text, disposition))
}

func toolResultMessage(toolCallID string, result ToolResult, isError bool) conversation.Message {
	parts := make([]conversation.Part, 0, 1+len(result.MediaRefs))
	parts = append(parts, conversation.ToolResult(toolCallID, result.Output, isError))
	for _, ref := range result.MediaRefs {
		parts = append(parts, conversation.Image(ref, ""))
	}
	return conversation.Message{
		Role:  conversation.ToolRole,
		Parts: conversation.NormalizeParts(parts),
	}
}

func resultToolCalls(messages []conversation.Message) []ToolCall {
	parts := conversation.ToolCalls(messages)
	if len(parts) == 0 {
		return nil
	}

	out := make([]ToolCall, 0, len(parts))
	for _, part := range parts {
		out = append(out, ToolCall{
			ID:        part.ID,
			Name:      part.Name,
			Arguments: part.Arguments,
		})
	}
	return out
}

func finalAnswer(messages []conversation.Message) string {
	return conversation.FinalAnswer(messages)
}
