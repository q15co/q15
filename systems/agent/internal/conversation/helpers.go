package conversation

import (
	"encoding/json"
	"strings"
)

// CloneMessages deep-copies canonical messages.
func CloneMessages(in []Message) []Message {
	if len(in) == 0 {
		return nil
	}

	out := make([]Message, len(in))
	for i, msg := range in {
		out[i] = Message{
			Role:  msg.Role,
			Parts: CloneParts(msg.Parts),
		}
	}
	return out
}

// CloneParts deep-copies canonical parts.
func CloneParts(in []Part) []Part {
	if len(in) == 0 {
		return nil
	}

	out := make([]Part, len(in))
	for i, part := range in {
		out[i] = part
		out[i].Replay = cloneReplayMap(part.Replay)
	}
	return out
}

func cloneReplayMap(in map[string]json.RawMessage) map[string]json.RawMessage {
	if len(in) == 0 {
		return nil
	}

	out := make(map[string]json.RawMessage, len(in))
	for key, raw := range in {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		out[key] = append(json.RawMessage(nil), raw...)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// NormalizeMessages normalizes message parts and drops empty replay map entries.
func NormalizeMessages(in []Message) []Message {
	if len(in) == 0 {
		return nil
	}

	out := make([]Message, len(in))
	for i, msg := range in {
		out[i] = NormalizeMessage(msg)
	}
	return out
}

// NormalizeMessage normalizes one message in-place.
func NormalizeMessage(msg Message) Message {
	msg.Parts = NormalizeParts(msg.Parts)
	return msg
}

// NormalizeParts normalizes one part slice.
func NormalizeParts(in []Part) []Part {
	if len(in) == 0 {
		return nil
	}

	out := make([]Part, 0, len(in))
	for _, part := range in {
		part = NormalizePart(part)
		if shouldKeepPart(part) {
			out = append(out, part)
		}
	}
	return out
}

// NormalizePart normalizes one part.
func NormalizePart(part Part) Part {
	part.Replay = cloneReplayMap(part.Replay)
	switch part.Type {
	case TextPartType:
		part.Disposition = normalizeDisposition(part.Disposition)
	case ImagePartType:
		part.MediaRef = strings.TrimSpace(part.MediaRef)
		part.DataURL = strings.TrimSpace(part.DataURL)
	case ReasoningPartType:
	case ToolCallPartType:
		part.ID = strings.TrimSpace(part.ID)
		part.Name = strings.TrimSpace(part.Name)
		part.Arguments = normalizeArguments(part.Arguments)
	case ToolResultPartType:
		part.ToolCallID = strings.TrimSpace(part.ToolCallID)
	default:
	}
	return part
}

func shouldKeepPart(part Part) bool {
	switch part.Type {
	case TextPartType:
		return strings.TrimSpace(part.Text) != ""
	case ImagePartType:
		return strings.TrimSpace(part.MediaRef) != "" || strings.TrimSpace(part.DataURL) != ""
	case ReasoningPartType:
		return strings.TrimSpace(part.Text) != "" || len(part.Replay) > 0
	case ToolCallPartType:
		return strings.TrimSpace(part.Name) != ""
	case ToolResultPartType:
		return strings.TrimSpace(part.ToolCallID) != ""
	default:
		return false
	}
}

// TextValue returns the first text value from a message for text-only roles.
func TextValue(msg Message) string {
	for _, part := range msg.Parts {
		if part.Type == TextPartType {
			return part.Text
		}
	}
	return ""
}

// ToolCalls returns the ordered tool-call parts from the provided messages.
func ToolCalls(messages []Message) []Part {
	var out []Part
	for _, msg := range messages {
		for _, part := range msg.Parts {
			if part.Type == ToolCallPartType {
				out = append(out, NormalizePart(part))
			}
		}
	}
	return out
}

// FinalAnswer returns the best assistant-facing final text from the provided
// transcript slice.
func FinalAnswer(messages []Message) string {
	finalText := ""
	plainText := ""
	for _, msg := range messages {
		if msg.Role != AssistantRole {
			continue
		}
		for _, part := range msg.Parts {
			if part.Type != TextPartType {
				continue
			}
			text := strings.TrimSpace(part.Text)
			if text == "" {
				continue
			}
			switch normalizeDisposition(part.Disposition) {
			case TextDispositionFinal:
				finalText = text
			case "":
				plainText = text
			}
		}
	}
	if finalText != "" {
		return finalText
	}
	return plainText
}

// HasImageParts reports whether any message contains one or more image parts.
func HasImageParts(messages []Message) bool {
	for _, msg := range messages {
		for _, part := range msg.Parts {
			if NormalizePart(part).Type == ImagePartType {
				return true
			}
		}
	}
	return false
}
