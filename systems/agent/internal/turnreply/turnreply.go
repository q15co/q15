// Package turnreply derives and canonicalizes the final assistant-facing reply
// for one completed turn transcript slice.
package turnreply

import (
	"strings"

	"github.com/q15co/q15/systems/agent/internal/conversation"
)

// Selection is the final assistant-facing reply extracted from one completed
// turn transcript slice.
type Selection struct {
	Text        string
	Attachments []conversation.Part
}

// Extract returns the final assistant-facing reply from one completed turn.
// Assistant-owned attachments in the trailing assistant block take precedence.
// When no assistant-owned attachments exist there, the trailing tool block is
// treated as the fallback delivery batch.
func Extract(messages []conversation.Message) Selection {
	normalized := conversation.NormalizeMessages(conversation.CloneMessages(messages))
	return Selection{
		Text:        conversation.FinalAnswer(normalized),
		Attachments: selectAttachments(normalized),
	}
}

// Canonicalize rewrites one completed turn so the terminal assistant reply owns
// its delivered attachments. Older turns may have stored the delivered
// attachments on the trailing tool batch instead.
func Canonicalize(messages []conversation.Message) []conversation.Message {
	normalized := conversation.NormalizeMessages(conversation.CloneMessages(messages))
	if len(normalized) == 0 {
		return nil
	}

	assistantStart := trailingRoleBlockStart(normalized, conversation.AssistantRole)
	assistantAttachments := collectReplyAttachments(normalized, assistantStart, len(normalized))
	if len(assistantAttachments) > 0 {
		return compactMessages(normalized)
	}

	toolStart, toolEnd := trailingToolBlockBefore(normalized, assistantStart)
	toolAttachments := collectReplyAttachments(normalized, toolStart, toolEnd)
	if len(toolAttachments) == 0 {
		return compactMessages(normalized)
	}

	if assistantStart == len(normalized) {
		normalized = append(normalized, conversation.AssistantMessage())
	}

	attachmentKeys := make(map[string]struct{}, len(toolAttachments))
	for _, attachment := range toolAttachments {
		attachmentKeys[attachment.key] = struct{}{}
	}

	for i := toolStart; i < toolEnd; i++ {
		if normalized[i].Role != conversation.ToolRole {
			continue
		}
		normalized[i].Parts = stripReplyAttachments(normalized[i].Parts, attachmentKeys)
		normalized[i] = conversation.NormalizeMessage(normalized[i])
	}

	lastAssistant := len(normalized) - 1
	finalParts := conversation.CloneParts(normalized[lastAssistant].Parts)
	for _, attachment := range toolAttachments {
		finalParts = append(finalParts, attachment.part)
	}
	normalized[lastAssistant].Parts = conversation.NormalizeParts(finalParts)

	return compactMessages(normalized)
}

type replyAttachment struct {
	key  string
	part conversation.Part
}

func selectAttachments(messages []conversation.Message) []conversation.Part {
	if len(messages) == 0 {
		return nil
	}

	assistantStart := trailingRoleBlockStart(messages, conversation.AssistantRole)
	if attachments := collectReplyAttachments(messages, assistantStart, len(messages)); len(
		attachments,
	) > 0 {
		return partsFromAttachments(attachments)
	}

	toolStart, toolEnd := trailingToolBlockBefore(messages, assistantStart)
	return partsFromAttachments(collectReplyAttachments(messages, toolStart, toolEnd))
}

func collectReplyAttachments(
	messages []conversation.Message,
	start int,
	end int,
) []replyAttachment {
	if start < 0 {
		start = 0
	}
	if end > len(messages) {
		end = len(messages)
	}
	if start >= end {
		return nil
	}

	seen := make(map[string]struct{})
	out := make([]replyAttachment, 0, end-start)
	for _, msg := range messages[start:end] {
		for _, part := range conversation.NormalizeParts(msg.Parts) {
			attachment, ok := normalizeReplyAttachment(part)
			if !ok {
				continue
			}
			if _, dup := seen[attachment.key]; dup {
				continue
			}
			seen[attachment.key] = struct{}{}
			out = append(out, attachment)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeReplyAttachment(part conversation.Part) (replyAttachment, bool) {
	part = conversation.NormalizePart(part)
	switch part.Type {
	case conversation.ImagePartType:
		if strings.TrimSpace(part.MediaRef) == "" && strings.TrimSpace(part.DataURL) == "" {
			return replyAttachment{}, false
		}
		normalized := conversation.Image(part.MediaRef, part.DataURL)
		return replyAttachment{
			key: string(
				normalized.Type,
			) + "\x00" + normalized.MediaRef + "\x00" + normalized.DataURL,
			part: normalized,
		}, true
	default:
		return replyAttachment{}, false
	}
}

func partsFromAttachments(attachments []replyAttachment) []conversation.Part {
	if len(attachments) == 0 {
		return nil
	}

	out := make([]conversation.Part, 0, len(attachments))
	for _, attachment := range attachments {
		out = append(out, attachment.part)
	}
	return out
}

func trailingRoleBlockStart(messages []conversation.Message, role conversation.Role) int {
	if len(messages) == 0 || messages[len(messages)-1].Role != role {
		return len(messages)
	}
	start := len(messages) - 1
	for start > 0 && messages[start-1].Role == role {
		start--
	}
	return start
}

func trailingToolBlockBefore(messages []conversation.Message, end int) (int, int) {
	if end > len(messages) {
		end = len(messages)
	}
	start := end
	for start > 0 && messages[start-1].Role == conversation.ToolRole {
		start--
	}
	return start, end
}

func stripReplyAttachments(
	parts []conversation.Part,
	keys map[string]struct{},
) []conversation.Part {
	out := make([]conversation.Part, 0, len(parts))
	for _, part := range conversation.NormalizeParts(parts) {
		if attachment, ok := normalizeReplyAttachment(part); ok {
			if _, remove := keys[attachment.key]; remove {
				continue
			}
		}
		out = append(out, part)
	}
	return out
}

func compactMessages(messages []conversation.Message) []conversation.Message {
	out := make([]conversation.Message, 0, len(messages))
	for _, msg := range messages {
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
