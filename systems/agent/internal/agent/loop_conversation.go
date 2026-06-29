package agent

import (
	"fmt"
	"strings"
	"time"

	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/turnreply"
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
		case conversation.TextPartType, conversation.MediaPartType:
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
	attachments := toolResultAttachments(result)
	parts := make([]conversation.Part, 0, 1+len(attachments))
	parts = append(parts, conversation.ToolResult(toolCallID, result.Output, isError))
	parts = append(parts, attachments...)
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

func finalReply(
	messages []conversation.Message,
	extractor *turnreply.Extractor,
) ReplyResult {
	selection := extractor.Extract(messages)
	return ReplyResult{
		Text:        selection.Text,
		Attachments: conversation.CloneParts(selection.Attachments),
		MediaRefs:   mediaRefsFromAttachments(selection.Attachments),
	}
}

// deliverToolNames derives the set of tool names whose attachments are
// candidates for user-facing delivery, built fresh from the registry snapshot.
// Tool identity — not tool-call position — decides what reaches the user.
func deliverToolNames(reg ToolRegistry) map[string]struct{} {
	set := make(map[string]struct{})
	if reg == nil {
		return set
	}
	for _, def := range reg.Definitions() {
		if !def.DeliversAttachments {
			continue
		}
		name := strings.TrimSpace(def.Name)
		if name != "" {
			set[name] = struct{}{}
		}
	}
	return set
}

func toolResultAttachments(result ToolResult) []conversation.Part {
	if len(result.Attachments) > 0 {
		return conversation.CloneParts(conversation.NormalizeParts(result.Attachments))
	}
	// NOTE: MediaRefs are intentionally not converted to Image parts here.
	// Converting them causes image parts to be embedded in tool result messages,
	// which then get stripped by Canonicalize()/compactMessages(), leaving
	// orphaned tool calls that providers like Kimi reject with 400 errors.
	// See https://github.com/q15co/q15/issues/84
	return nil
}

func mediaRefsFromAttachments(parts []conversation.Part) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0, len(parts))
	for _, part := range conversation.NormalizeParts(parts) {
		if !part.IsMedia(conversation.MediaKindImage) {
			continue
		}
		ref := strings.TrimSpace(part.MediaRef)
		if ref == "" {
			continue
		}
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		out = append(out, ref)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func withUserTemporalMetadata(
	msg conversation.Message,
	now time.Time,
	lastUserTimestamp time.Time,
	hasLastUserTimestamp bool,
) conversation.Message {
	msg = conversation.NormalizeMessage(msg)
	if msg.Role != conversation.UserRole {
		return msg
	}

	timestamp := now.In(time.Local)
	if stored, ok := conversation.UserMessageTimeLocal(msg); ok {
		timestamp = stored
	}

	meta := &conversation.UserTemporalMetadata{
		TimeLocal: timestamp,
	}
	if hasLastUserTimestamp {
		gap := timestamp.Sub(lastUserTimestamp)
		if gap < 0 {
			gap = 0
		}
		meta.SincePrevUserMessage = conversation.NewDuration(gap)
	}
	msg.UserTemporal = meta
	return conversation.NormalizeMessage(msg)
}
