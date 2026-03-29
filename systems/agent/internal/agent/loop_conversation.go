package agent

import (
	"strings"

	"github.com/q15co/q15/systems/agent/internal/conversation"
)

func systemMessage(text string) conversation.Message {
	return conversation.SystemMessage(text)
}

func userMessage(text string) conversation.Message {
	return conversation.UserMessage(text)
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

func toolResultMessage(toolCallID, content string, isError bool) conversation.Message {
	return conversation.ToolResultMessage(toolCallID, content, isError)
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
