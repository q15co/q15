package app

import (
	"context"
	"sync"
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/cognition"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/memory"
	q15skills "github.com/q15co/q15/systems/agent/internal/skills"
)

type runtimeStore struct {
	memory *memory.Store
	skills *q15skills.Manager

	mu              sync.Mutex
	appendObservers []func()
}

var _ agent.ConversationStore = (*runtimeStore)(nil)
var _ agent.CoreMemoryStore = (*runtimeStore)(nil)
var _ agent.WorkingMemoryStore = (*runtimeStore)(nil)
var _ agent.SkillCatalogStore = (*runtimeStore)(nil)
var _ cognition.ContextLoader = (*runtimeStore)(nil)
var _ cognition.ControllerStore = (*runtimeStore)(nil)

func (s *runtimeStore) LoadRecentMessages(
	ctx context.Context,
	turns int,
) ([]conversation.Message, error) {
	return s.memory.LoadRecentMessages(ctx, turns)
}

func (s *runtimeStore) LoadLastUserTimestamp(
	ctx context.Context,
) (time.Time, bool, error) {
	return s.memory.LoadLastUserTimestamp(ctx)
}

func (s *runtimeStore) AppendTurn(ctx context.Context, messages []conversation.Message) error {
	if err := s.memory.AppendTurn(ctx, messages); err != nil {
		return err
	}
	s.mu.Lock()
	observers := append([]func(){}, s.appendObservers...)
	s.mu.Unlock()
	for _, observer := range observers {
		if observer == nil {
			continue
		}
		observer()
	}
	return nil
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

func (s *runtimeStore) LoadCognitionArtifact(
	ctx context.Context,
	relativePath string,
) (cognition.Artifact, error) {
	return s.memory.LoadCognitionArtifact(ctx, relativePath)
}

func (s *runtimeStore) StoreCognitionArtifact(
	ctx context.Context,
	artifact cognition.Artifact,
) error {
	return s.memory.StoreCognitionArtifact(ctx, artifact)
}

func (s *runtimeStore) LoadHead(ctx context.Context) (int64, time.Time, error) {
	return s.memory.LoadHead(ctx)
}

func (s *runtimeStore) LoadConsolidationCheckpoint(
	ctx context.Context,
) (cognition.ConsolidationCheckpoint, error) {
	return s.memory.LoadConsolidationCheckpoint(ctx)
}

func (s *runtimeStore) LoadJobState(
	ctx context.Context,
	jobType string,
) (cognition.JobState, error) {
	return s.memory.LoadJobState(ctx, jobType)
}

func (s *runtimeStore) StoreJobState(
	ctx context.Context,
	jobType string,
	state cognition.JobState,
) error {
	return s.memory.StoreJobState(ctx, jobType, state)
}

func (s *runtimeStore) StoreConsolidationCheckpoint(
	ctx context.Context,
	checkpoint cognition.ConsolidationCheckpoint,
) (cognition.ConsolidationCheckpoint, error) {
	return s.memory.StoreConsolidationCheckpoint(ctx, checkpoint)
}

func (s *runtimeStore) AppendRunRecord(ctx context.Context, record cognition.RunRecord) error {
	return s.memory.AppendRunRecord(ctx, record)
}

func (s *runtimeStore) AddAppendObserver(observer func()) {
	if observer == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appendObservers = append(s.appendObservers, observer)
}
