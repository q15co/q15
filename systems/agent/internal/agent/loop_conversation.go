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

func finalReply(messages []conversation.Message) ReplyResult {
	selection := turnreply.Extract(messages)
	return ReplyResult{
		Text:        selection.Text,
		Attachments: conversation.CloneParts(selection.Attachments),
		MediaRefs:   mediaRefsFromAttachments(selection.Attachments),
	}
}

func toolResultAttachments(result ToolResult) []conversation.Part {
	if len(result.Attachments) > 0 {
		return conversation.CloneParts(conversation.NormalizeParts(result.Attachments))
	}
	return imagePartsFromMediaRefs(result.MediaRefs)
}

func imagePartsFromMediaRefs(refs []string) []conversation.Part {
	if len(refs) == 0 {
		return nil
	}
	out := make([]conversation.Part, 0, len(refs))
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		out = append(out, conversation.Image(ref, ""))
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func mediaRefsFromAttachments(parts []conversation.Part) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0, len(parts))
	for _, part := range conversation.NormalizeParts(parts) {
		switch part.Type {
		case conversation.ImagePartType:
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
