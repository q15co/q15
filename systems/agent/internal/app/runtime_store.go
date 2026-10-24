package app

import (
	"context"
	"fmt"
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/memory"
	q15skills "github.com/q15co/q15/systems/agent/internal/skills"
)

type transcriptStore interface {
	agent.ConversationStore
	RecordDeliveredAssistantEvent(context.Context, conversation.DeliveredAssistantEvent) error
}
type runtimeStore struct {
	memory         *memory.Store
	transcript     transcriptStore
	skills         *q15skills.Manager
	contextService agent.ContextAssembler
}

func (s *runtimeStore) AppendTurnAtSeq(
	ctx context.Context,
	seq int64,
	messages []conversation.Message,
) error {
	return s.transcript.AppendTurnAtSeq(ctx, seq, messages)
}

func (s *runtimeStore) RecordDeliveredAssistantEvent(
	ctx context.Context,
	event conversation.DeliveredAssistantEvent,
) error {
	return s.transcript.RecordDeliveredAssistantEvent(ctx, event)
}
func (s *runtimeStore) LoadRecentMessages(
	ctx context.Context,
	turns int,
) ([]conversation.Message, error) {
	return s.transcript.LoadRecentMessages(ctx, turns)
}

func (s *runtimeStore) LoadLastUserTimestamp(
	ctx context.Context,
) (time.Time, bool, error) {
	return s.transcript.LoadLastUserTimestamp(ctx)
}

func (s *runtimeStore) ReserveTurnSeq(ctx context.Context) (int64, error) {
	return s.transcript.ReserveTurnSeq(ctx)
}

func (s *runtimeStore) LoadCoreMemory(ctx context.Context) (agent.CoreMemory, error) {
	return s.memory.LoadCoreMemory(ctx)
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
			Tools:         append([]string(nil), entry.Tools...),
		})
	}
	return agent.SkillCatalog{
		Entries:  entries,
		Warnings: append([]string(nil), catalog.Warnings...),
	}, nil
}

func (s *runtimeStore) PrepareContext(
	ctx context.Context,
	system, input []conversation.Message,
) (agent.PreparedContext, error) {
	if s.contextService == nil {
		return agent.PreparedContext{}, fmt.Errorf("history context service is required")
	}
	return s.contextService.PrepareContext(ctx, system, input)
}
