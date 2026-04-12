package conversation

import (
	"encoding/json"
	"html"
	"strconv"
	"strings"
	"time"
)

// CloneMessages deep-copies canonical messages.
func CloneMessages(in []Message) []Message {
	if len(in) == 0 {
		return nil
	}

	out := make([]Message, len(in))
	for i, msg := range in {
		out[i] = Message{
			Role:         msg.Role,
			Parts:        CloneParts(msg.Parts),
			UserTemporal: cloneUserTemporalMetadata(msg.UserTemporal),
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
	msg.UserTemporal = normalizeUserTemporalMetadata(msg.Role, msg.UserTemporal)
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

// RenderUserMessageMetadataTag renders the canonical prompt-visible metadata
// tag for one user message when temporal metadata is available.
func RenderUserMessageMetadataTag(msg Message) string {
	msg = NormalizeMessage(msg)
	timeLocal, ok := UserMessageTimeLocal(msg)
	if !ok {
		return ""
	}

	var out strings.Builder
	out.WriteString(`<message_meta day_of_week_local="`)
	out.WriteString(html.EscapeString(timeLocal.Weekday().String()))
	out.WriteString(`" timestamp_local="`)
	out.WriteString(html.EscapeString(timeLocal.Format("20060102T150405-0700")))
	out.WriteString(`" since_prev_user_message="`)
	if gap, ok := SincePrevUserMessage(msg); ok {
		out.WriteString(html.EscapeString(formatCompactDuration(gap)))
	} else {
		out.WriteString("none")
	}
	out.WriteString(`"/>`)
	return out.String()
}

// PromptVisibleUserMessage returns a transient prompt-visible user message with
// the metadata tag injected as the leading text part.
func PromptVisibleUserMessage(msg Message) Message {
	msg = NormalizeMessage(msg)
	tag := RenderUserMessageMetadataTag(msg)
	if msg.Role != UserRole || tag == "" {
		return msg
	}

	parts := make([]Part, 0, len(msg.Parts)+1)
	prefix := tag
	if messageHasTextPart(msg) {
		prefix += "\n\n"
	}
	parts = append(parts, Text(prefix, ""))
	parts = append(parts, CloneParts(msg.Parts)...)
	msg.Parts = parts
	return msg
}

func cloneUserTemporalMetadata(
	in *UserTemporalMetadata,
) *UserTemporalMetadata {
	if in == nil {
		return nil
	}

	out := &UserTemporalMetadata{
		TimeLocal: normalizeMessageTimeLocal(in.TimeLocal),
	}
	if in.SincePrevUserMessage != nil {
		duration := *in.SincePrevUserMessage
		if duration < 0 {
			duration = 0
		}
		out.SincePrevUserMessage = &duration
	}
	if out.TimeLocal.IsZero() {
		return nil
	}
	return out
}

func normalizeUserTemporalMetadata(
	role Role,
	in *UserTemporalMetadata,
) *UserTemporalMetadata {
	if role != UserRole || in == nil {
		return nil
	}

	out := cloneUserTemporalMetadata(in)
	if out == nil {
		return nil
	}
	return out
}

func normalizeMessageTimeLocal(value time.Time) time.Time {
	if value.IsZero() {
		return time.Time{}
	}
	return time.Unix(value.Unix(), 0).In(value.Location())
}

func messageHasTextPart(msg Message) bool {
	for _, part := range msg.Parts {
		if NormalizePart(part).Type == TextPartType {
			return true
		}
	}
	return false
}

func formatCompactDuration(value time.Duration) string {
	if value < 0 {
		value = 0
	}

	totalSeconds := int64(value / time.Second)
	if totalSeconds <= 0 {
		return "0s"
	}

	type durationUnit struct {
		suffix  string
		seconds int64
	}

	units := []durationUnit{
		{suffix: "w", seconds: 7 * 24 * 60 * 60},
		{suffix: "d", seconds: 24 * 60 * 60},
		{suffix: "h", seconds: 60 * 60},
		{suffix: "m", seconds: 60},
		{suffix: "s", seconds: 1},
	}

	parts := make([]string, 0, 2)
	remaining := totalSeconds
	for _, unit := range units {
		if remaining < unit.seconds && len(parts) > 0 {
			continue
		}
		if remaining < unit.seconds {
			continue
		}

		count := remaining / unit.seconds
		remaining %= unit.seconds
		parts = append(parts, strconv.FormatInt(count, 10)+unit.suffix)
		if len(parts) == 2 {
			break
		}
	}

	if len(parts) == 0 {
		return "0s"
	}
	return strings.Join(parts, "")
}
