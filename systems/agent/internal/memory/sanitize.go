package memory

import (
	"strings"

	"github.com/q15co/q15/systems/agent/internal/conversation"
)

func sanitizeStoredMessages(in []conversation.Message) []conversation.Message {
	normalized := conversation.NormalizeMessages(in)
	if len(normalized) == 0 {
		return nil
	}

	backfillPortableReasoningForToolReplay(normalized)

	out := make([]conversation.Message, 0, len(normalized))
	for _, msg := range normalized {
		if len(msg.Parts) == 0 {
			continue
		}
		out = append(out, msg)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func backfillPortableReasoningForToolReplay(messages []conversation.Message) {
	for start := 0; start < len(messages); {
		if messages[start].Role != conversation.AssistantRole {
			start++
			continue
		}

		end := start
		groupHasToolCalls := false
		for end < len(messages) && messages[end].Role == conversation.AssistantRole {
			if assistantMessageHasToolCall(messages[end]) {
				groupHasToolCalls = true
			}
			end++
		}
		if groupHasToolCalls {
			for i := start; i < end; i++ {
				backfillReplayOnlyReasoning(&messages[i])
			}
		}
		start = end
	}
}

func assistantMessageHasToolCall(msg conversation.Message) bool {
	for _, part := range msg.Parts {
		if part.Type == conversation.ToolCallPartType {
			return true
		}
	}
	return false
}

func backfillReplayOnlyReasoning(msg *conversation.Message) {
	if msg == nil {
		return
	}
	for i := range msg.Parts {
		part := &msg.Parts[i]
		if part.Type != conversation.ReasoningPartType {
			continue
		}
		if strings.TrimSpace(part.Text) != "" || len(part.Replay) == 0 {
			continue
		}
		part.Text = conversation.PortableReasoningUnavailableText
	}
}
