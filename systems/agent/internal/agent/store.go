package agent

import "context"

// ConversationStore persists assistant/user/tool messages between replies.
type ConversationStore interface {
	LoadRecentMessages(ctx context.Context, turns int) ([]Message, error)
	AppendTurn(ctx context.Context, messages []Message) error
}

// CoreMemory holds small, high-signal identity/profile notes that should stay
// in-context for every model call.
type CoreMemory struct {
	Files []CoreMemoryFile
}

// CoreMemoryFile is one markdown file loaded from persistent core memory.
type CoreMemoryFile struct {
	RelativePath string
	Description  string
	Limit        int
	Content      string
}

// CoreMemoryStore can provide persistent core memory for system prompt
// injection.
type CoreMemoryStore interface {
	LoadCoreMemory(ctx context.Context) (CoreMemory, error)
}
