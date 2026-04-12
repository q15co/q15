// Package conversation defines the canonical transcript model used at runtime
// and for persistence.
package conversation

import (
	"encoding/json"
	"strings"
	"time"
)

// SchemaVersion is the current persisted transcript schema version. New writes
// always use this version; older versions are accepted only during startup
// migration.
const SchemaVersion = 3

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
	ImagePartType      PartType = "image"
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

// Duration stores a canonical runtime duration while keeping JSON stable and
// human-inspectable in persisted transcript records.
type Duration time.Duration

// NewDuration returns a pointer wrapper for one duration.
func NewDuration(value time.Duration) *Duration {
	duration := Duration(value)
	return &duration
}

// Duration returns the wrapped time.Duration value.
func (d Duration) Duration() time.Duration {
	return time.Duration(d)
}

// String returns the wrapped duration using the standard Go duration format.
func (d Duration) String() string {
	return d.Duration().String()
}

// MarshalJSON stores the wrapped duration as a standard Go duration string.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.String())
}

// UnmarshalJSON accepts either a standard Go duration string or legacy integer
// nanoseconds.
func (d *Duration) UnmarshalJSON(data []byte) error {
	if strings.TrimSpace(string(data)) == "null" {
		return nil
	}

	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		parsed, err := time.ParseDuration(strings.TrimSpace(text))
		if err != nil {
			return err
		}
		*d = Duration(parsed)
		return nil
	}

	var nanos int64
	if err := json.Unmarshal(data, &nanos); err != nil {
		return err
	}
	*d = Duration(time.Duration(nanos))
	return nil
}

// UserTemporalMetadata stores canonical temporal context for one user message.
type UserTemporalMetadata struct {
	TimeLocal            time.Time `json:"time_local"`
	SincePrevUserMessage *Duration `json:"since_prev_user_message,omitempty"`
}

// Message is one canonical conversation message.
//
// This is the canonical persisted and replayable transcript shape. Providers,
// the loop, and stores should map to and from this model rather than
// reintroducing alternative message types as sources of truth.
type Message struct {
	Role         Role                  `json:"role"`
	Parts        []Part                `json:"parts,omitempty"`
	UserTemporal *UserTemporalMetadata `json:"user_temporal,omitempty"`
}

// Part is one canonical transcript part. Only fields relevant to the current
// Type are populated.
type Part struct {
	Type PartType `json:"type"`

	// Text and Reasoning parts.
	Text        string          `json:"text,omitempty"`
	Disposition TextDisposition `json:"disposition,omitempty"`
	// Image parts.
	MediaRef string `json:"media_ref,omitempty"`
	DataURL  string `json:"data_url,omitempty"`
	// Replay stores provider-specific reconstruction metadata for a reasoning
	// part. It is supplemental only: portable transcript fields remain the
	// canonical source of truth, and replay consumers should prefer Text when it
	// is available.
	Replay map[string]json.RawMessage `json:"replay,omitempty"`

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

// Image creates one image part backed by either a media ref or a data URL.
func Image(mediaRef, dataURL string) Part {
	return Part{
		Type:     ImagePartType,
		MediaRef: strings.TrimSpace(mediaRef),
		DataURL:  strings.TrimSpace(dataURL),
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

// UserMessageParts creates a user message from ordered parts.
func UserMessageParts(parts ...Part) Message {
	return Message{Role: UserRole, Parts: CloneParts(parts)}
}

// AssistantMessage creates an assistant message from ordered parts.
func AssistantMessage(parts ...Part) Message {
	return Message{Role: AssistantRole, Parts: CloneParts(parts)}
}

// ToolResultMessage creates a tool-role message with one tool-result part.
func ToolResultMessage(toolCallID, content string, isError bool) Message {
	return Message{Role: ToolRole, Parts: []Part{ToolResult(toolCallID, content, isError)}}
}

// UserMessageTimeLocal returns the stored local-time timestamp for one user
// message when available.
func UserMessageTimeLocal(msg Message) (time.Time, bool) {
	if msg.Role != UserRole || msg.UserTemporal == nil || msg.UserTemporal.TimeLocal.IsZero() {
		return time.Time{}, false
	}
	return msg.UserTemporal.TimeLocal, true
}

// SincePrevUserMessage returns the stored gap to the prior persisted user
// message when available.
func SincePrevUserMessage(msg Message) (time.Duration, bool) {
	if msg.Role != UserRole || msg.UserTemporal == nil ||
		msg.UserTemporal.SincePrevUserMessage == nil {
		return 0, false
	}
	return msg.UserTemporal.SincePrevUserMessage.Duration(), true
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
