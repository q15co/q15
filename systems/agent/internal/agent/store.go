package agent

import "context"

// ConversationStore persists assistant/user/tool messages between replies.
type ConversationStore interface {
	LoadRecentMessages(ctx context.Context, turns int) ([]Message, error)
	AppendTurn(ctx context.Context, messages []Message) error
}
