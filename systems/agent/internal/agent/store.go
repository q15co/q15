package agent

import (
	"context"

	"github.com/q15co/q15/systems/agent/internal/conversation"
)

// ConversationStore persists assistant/user/tool messages between replies.
type ConversationStore interface {
	LoadRecentMessages(ctx context.Context, turns int) ([]conversation.Message, error)
	AppendTurn(ctx context.Context, messages []conversation.Message) error
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

// SkillCatalog describes dynamically available skills that may be loaded by
// the model on demand.
type SkillCatalog struct {
	Entries  []SkillCatalogEntry
	Warnings []string
}

// SkillCatalogEntry is one prompt-visible skill.
type SkillCatalogEntry struct {
	Name          string
	Description   string
	Source        string
	SkillPath     string
	SkillFilePath string
}

// SkillCatalogStore can provide a fresh skills catalog for each reply.
type SkillCatalogStore interface {
	LoadSkillCatalog(ctx context.Context) (SkillCatalog, error)
}
