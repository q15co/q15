// Package conversation defines the normalized event stream for agent turns.
package conversation

// Kind identifies one conversation event variant.
type Kind string

const (
	// UserMessageKind marks a user-authored message event.
	UserMessageKind Kind = "user_message"
	// AssistantMessageKind marks an assistant-authored message event.
	AssistantMessageKind Kind = "assistant_message"
	// ActionRequestKind marks a tool/action request event.
	ActionRequestKind Kind = "action_request"
	// ActionResultKind marks a tool/action result event.
	ActionResultKind Kind = "action_result"
	// SystemMessageKind marks a system-authored message event.
	SystemMessageKind Kind = "system_message"
)

// Event is the common interface implemented by all conversation events.
type Event interface {
	Kind() Kind
}

// UserMessage stores one user-authored message.
type UserMessage struct{ Text string }

// Kind returns the event kind.
func (UserMessage) Kind() Kind { return UserMessageKind }

// AssistantMessage stores one assistant-authored message.
type AssistantMessage struct{ Text string } // can be empty

// Kind returns the event kind.
func (AssistantMessage) Kind() Kind { return AssistantMessageKind }

// ActionRequest stores one requested tool or action invocation.
type ActionRequest struct {
	CallID string
	Name   string
	Args   []byte // json
}

// Kind returns the event kind.
func (ActionRequest) Kind() Kind { return ActionRequestKind }

// ActionResult stores one completed tool or action result.
type ActionResult struct {
	CallID  string
	Output  string
	IsError bool
}

// Kind returns the event kind.
func (ActionResult) Kind() Kind { return ActionResultKind }

// SystemMessage stores one system-authored message.
type SystemMessage struct{ Text string }

// Kind returns the event kind.
func (SystemMessage) Kind() Kind { return SystemMessageKind }
