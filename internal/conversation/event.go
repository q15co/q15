package conversation

type Kind string

const (
	UserMessageKind      Kind = "user_message"
	AssistantMessageKind Kind = "assistant_message"
	ActionRequestKind    Kind = "action_request"
	ActionResultKind     Kind = "action_result"
	SystemMessageKind    Kind = "system_message"
)

type Event interface {
	Kind() Kind
}

type UserMessage struct{ Text string }

func (UserMessage) Kind() Kind { return UserMessageKind }

type AssistantMessage struct{ Text string } // can be empty
func (AssistantMessage) Kind() Kind         { return AssistantMessageKind }

type ActionRequest struct {
	CallID string
	Name   string
	Args   []byte // json
}

func (ActionRequest) Kind() Kind { return ActionRequestKind }

type ActionResult struct {
	CallID  string
	Output  string
	IsError bool
}

func (ActionResult) Kind() Kind { return ActionResultKind }

type SystemMessage struct{ Text string }

func (SystemMessage) Kind() Kind { return SystemMessageKind }
