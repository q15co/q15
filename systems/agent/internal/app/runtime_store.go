package app

import (
	"context"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/memory"
	q15skills "github.com/q15co/q15/systems/agent/internal/skills"
)

type runtimeStore struct {
	memory *memory.Store
	skills *q15skills.Manager
}

var _ agent.ConversationStore = (*runtimeStore)(nil)
var _ agent.CoreMemoryStore = (*runtimeStore)(nil)
var _ agent.WorkingMemoryStore = (*runtimeStore)(nil)
var _ agent.SkillCatalogStore = (*runtimeStore)(nil)

func (s *runtimeStore) LoadRecentMessages(
	ctx context.Context,
	turns int,
) ([]conversation.Message, error) {
	return s.memory.LoadRecentMessages(ctx, turns)
}

func (s *runtimeStore) AppendTurn(ctx context.Context, messages []conversation.Message) error {
	return s.memory.AppendTurn(ctx, messages)
}

func (s *runtimeStore) LoadCoreMemory(ctx context.Context) (agent.CoreMemory, error) {
	return s.memory.LoadCoreMemory(ctx)
}

func (s *runtimeStore) LoadWorkingMemory(ctx context.Context) (agent.WorkingMemory, error) {
	return s.memory.LoadWorkingMemory(ctx)
}

func (s *runtimeStore) LoadSkillCatalog(ctx context.Context) (agent.SkillCatalog, error) {
	_ = ctx
	if s.skills == nil {
		return agent.SkillCatalog{}, nil
	}
	catalog := s.skills.LoadCatalog()
	entries := make([]agent.SkillCatalogEntry, 0, len(catalog.Entries))
	for _, entry := range catalog.Entries {
		entries = append(entries, agent.SkillCatalogEntry{
			Name:          entry.Name,
			Description:   entry.Description,
			Source:        string(entry.Source),
			SkillPath:     entry.SkillPath,
			SkillFilePath: entry.SkillFilePath,
		})
	}
	return agent.SkillCatalog{
		Entries:  entries,
		Warnings: append([]string(nil), catalog.Warnings...),
	}, nil
}
