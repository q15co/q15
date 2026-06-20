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

// Extractor derives and canonicalizes the final assistant-facing reply for one
// completed turn, honoring explicit attachment-delivery intent.
//
// Delivery is opt-in: only attachments produced by a tool whose name is in the
// deliver set are promoted onto the terminal assistant reply. Tool identity —
// not tool-call position — decides what reaches the user, so an agent-internal
// vision tool (e.g. load_image) can never leak its media to the user, and a
// later unrelated tool call can never drop a deliberately attached one (issue
// #110). A nil/empty deliver set promotes nothing (leak-safe default).
type Extractor struct {
	deliverTools map[string]struct{}
}

// NewExtractor constructs an Extractor that promotes attachments only from the
// named deliver tools. A nil or empty map promotes nothing. The set is copied
// so later mutation of the caller's map cannot affect this extractor.
func NewExtractor(deliverTools map[string]struct{}) *Extractor {
	if len(deliverTools) == 0 {
		return &Extractor{}
	}
	out := make(map[string]struct{}, len(deliverTools))
	for name := range deliverTools {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		out[name] = struct{}{}
	}
	if len(out) == 0 {
		return &Extractor{}
	}
	return &Extractor{deliverTools: out}
}

// Extract returns the final assistant-facing reply from one completed turn.
// Assistant-owned attachments in the trailing assistant block take precedence.
// When no assistant-owned attachments exist there, attachments produced by any
// deliver-tool result anywhere in the turn are treated as the fallback delivery
// batch, deduplicated by media ref.
func (e *Extractor) Extract(messages []conversation.Message) Selection {
	normalized := conversation.NormalizeMessages(conversation.CloneMessages(messages))
	return Selection{
		Text:        conversation.FinalAnswer(normalized),
		Attachments: e.selectAttachments(normalized),
	}
}

// Canonicalize rewrites one completed turn so the terminal assistant reply owns
// its delivered attachments. Attachments produced by deliver tools anywhere in
// the turn (not only the trailing tool batch) are gathered, deduplicated by
// media ref, stripped from their tool messages, and appended onto the terminal
// assistant reply. Agent-internal (non-deliver) attachments remain on their
// tool messages so the model still sees them on the next turn.
func (e *Extractor) Canonicalize(messages []conversation.Message) []conversation.Message {
	normalized := conversation.NormalizeMessages(conversation.CloneMessages(messages))
	if len(normalized) == 0 {
		return nil
	}

	assistantStart := trailingRoleBlockStart(normalized, conversation.AssistantRole)
	assistantAttachments := collectReplyAttachments(normalized, assistantStart, len(normalized))
	if len(assistantAttachments) > 0 {
		return compactMessages(normalized)
	}

	toolAttachments := e.collectDeliveredAttachments(normalized)
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

	for i := range normalized {
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

func (e *Extractor) selectAttachments(messages []conversation.Message) []conversation.Part {
	if len(messages) == 0 {
		return nil
	}

	assistantStart := trailingRoleBlockStart(messages, conversation.AssistantRole)
	if attachments := collectReplyAttachments(messages, assistantStart, len(messages)); len(
		attachments,
	) > 0 {
		return partsFromAttachments(attachments)
	}

	return partsFromAttachments(e.collectDeliveredAttachments(messages))
}

// collectDeliveredAttachments scans every tool-role message in the turn for
// attachments whose originating tool call resolves to a deliver tool, deduping
// by media ref. An attachment part carries no call id of its own; the call id
// is taken from the tool_result part(s) in the same message. Tool messages
// whose call id cannot be resolved (orphaned results) or whose tool is not a
// deliver tool contribute nothing — the safe default that keeps
// vision/internal media off the user-facing reply.
func (e *Extractor) collectDeliveredAttachments(
	messages []conversation.Message,
) []replyAttachment {
	if len(e.deliverTools) == 0 {
		return nil
	}

	names := resolveToolCallNames(messages)
	seen := make(map[string]struct{})
	out := make([]replyAttachment, 0)
	for _, msg := range messages {
		if msg.Role != conversation.ToolRole {
			continue
		}
		parts := conversation.NormalizeParts(msg.Parts)
		if !messageDelivers(parts, names, e.deliverTools) {
			continue
		}
		for _, part := range parts {
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

// resolveToolCallNames maps each tool-call id to its tool name across all
// assistant messages. Call ids are unique per turn, so a global map correctly
// resolves a tool result back to the earlier assistant tool call that produced
// it (the basis for tool-identity-based delivery).
func resolveToolCallNames(messages []conversation.Message) map[string]string {
	names := make(map[string]string)
	for _, msg := range messages {
		if msg.Role != conversation.AssistantRole {
			continue
		}
		for _, part := range conversation.NormalizeParts(msg.Parts) {
			if part.Type != conversation.ToolCallPartType {
				continue
			}
			id := strings.TrimSpace(part.ID)
			name := strings.TrimSpace(part.Name)
			if id == "" || name == "" {
				continue
			}
			if _, exists := names[id]; !exists {
				names[id] = name
			}
		}
	}
	return names
}

// messageDelivers reports whether a tool message originated from a deliver
// tool. The tool name is resolved from the message's tool_result part(s); the
// attachment parts themselves carry no call id. A tool message with no
// resolvable tool_result is treated as non-delivering (orphan-safe).
func messageDelivers(
	parts []conversation.Part,
	names map[string]string,
	deliverTools map[string]struct{},
) bool {
	for _, part := range parts {
		if part.Type != conversation.ToolResultPartType {
			continue
		}
		name, ok := names[strings.TrimSpace(part.ToolCallID)]
		if !ok {
			continue
		}
		if _, delivers := deliverTools[name]; delivers {
			return true
		}
	}
	return false
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
	case conversation.AudioPartType:
		if strings.TrimSpace(part.MediaRef) == "" {
			return replyAttachment{}, false
		}
		normalized := conversation.Audio(part.MediaRef)
		return replyAttachment{
			key:  string(normalized.Type) + "\x00" + normalized.MediaRef,
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
		if !messageSurvivesCompaction(msg.Parts) {
			continue
		}
		out = append(out, msg)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// messageSurvivesCompaction reports whether a message must be retained after
// canonicalization. A message carrying any tool_result or tool_call part always
// survives: dropping it would orphan tool results from their calls (issue #84)
// and break the tool-call-id resolution that deliver-attachment promotion
// depends on. Otherwise the message survives iff it has any part after
// normalization.
func messageSurvivesCompaction(parts []conversation.Part) bool {
	for _, part := range parts {
		switch conversation.NormalizePart(part).Type {
		case conversation.ToolResultPartType, conversation.ToolCallPartType:
			return true
		}
	}
	return len(conversation.NormalizeParts(parts)) > 0
}
