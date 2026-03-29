// Package conversation defines the canonical transcript model used at runtime
// and for persistence.
package conversation

import (
	"encoding/json"
	"strings"
)

// SchemaVersion is the current persisted transcript schema version.
const SchemaVersion = 2

// PortableReasoningUnavailableText is an explicit placeholder used when a
// provider only exposed opaque replay state and no portable reasoning text.
const PortableReasoningUnavailableText = "Portable reasoning summary unavailable; only provider-specific replay state was preserved."

// Role identifies the message author role.
type Role string

// Canonical message roles.
const (
	SystemRole    Role = "system"
	UserRole      Role = "user"
	AssistantRole Role = "assistant"
	ToolRole      Role = "tool"
)

// PartType identifies one canonical message part variant.
type PartType string

// Canonical message part types.
const (
	TextPartType       PartType = "text"
	ReasoningPartType  PartType = "reasoning"
	ToolCallPartType   PartType = "tool_call"
	ToolResultPartType PartType = "tool_result"
)

// TextDisposition classifies assistant text when the source provider exposes a
// commentary-vs-final distinction.
type TextDisposition string

// Canonical assistant text dispositions.
const (
	TextDispositionCommentary TextDisposition = "commentary"
	TextDispositionFinal      TextDisposition = "final"
)

// Message is one canonical conversation message.
type Message struct {
	Role  Role   `json:"role"`
	Parts []Part `json:"parts,omitempty"`
}

// Part is one canonical transcript part. Only fields relevant to the current
// Type are populated.
type Part struct {
	Type PartType `json:"type"`

	// Text and Reasoning parts.
	Text        string                     `json:"text,omitempty"`
	Disposition TextDisposition            `json:"disposition,omitempty"`
	Replay      map[string]json.RawMessage `json:"replay,omitempty"`

	// Tool call parts.
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`

	// Tool result parts.
	ToolCallID string `json:"tool_call_id,omitempty"`
	Content    string `json:"content,omitempty"`
	IsError    bool   `json:"is_error,omitempty"`
}

// Text creates one text part.
func Text(text string, disposition TextDisposition) Part {
	return Part{
		Type:        TextPartType,
		Text:        text,
		Disposition: normalizeDisposition(disposition),
	}
}

// Reasoning creates one reasoning part.
func Reasoning(text string, replay map[string]json.RawMessage) Part {
	return Part{
		Type:   ReasoningPartType,
		Text:   text,
		Replay: cloneReplayMap(replay),
	}
}

// ToolCall creates one tool-call part.
func ToolCall(id, name, arguments string) Part {
	return Part{
		Type:      ToolCallPartType,
		ID:        strings.TrimSpace(id),
		Name:      strings.TrimSpace(name),
		Arguments: normalizeArguments(arguments),
	}
}

// ToolResult creates one tool-result part.
func ToolResult(toolCallID, content string, isError bool) Part {
	return Part{
		Type:       ToolResultPartType,
		ToolCallID: strings.TrimSpace(toolCallID),
		Content:    content,
		IsError:    isError,
	}
}

// SystemMessage creates a system message with one text part.
func SystemMessage(text string) Message {
	return Message{Role: SystemRole, Parts: []Part{Text(text, "")}}
}

// UserMessage creates a user message with one text part.
func UserMessage(text string) Message {
	return Message{Role: UserRole, Parts: []Part{Text(text, "")}}
}

// AssistantMessage creates an assistant message from ordered parts.
func AssistantMessage(parts ...Part) Message {
	return Message{Role: AssistantRole, Parts: CloneParts(parts)}
}

// ToolResultMessage creates a tool-role message with one tool-result part.
func ToolResultMessage(toolCallID, content string, isError bool) Message {
	return Message{Role: ToolRole, Parts: []Part{ToolResult(toolCallID, content, isError)}}
}

func normalizeDisposition(disposition TextDisposition) TextDisposition {
	switch disposition {
	case TextDispositionCommentary, TextDispositionFinal:
		return disposition
	default:
		return ""
	}
}

func normalizeArguments(arguments string) string {
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" {
		return "{}"
	}
	return trimmed
}
