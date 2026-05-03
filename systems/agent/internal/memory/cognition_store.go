package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/q15co/q15/systems/agent/internal/cognition"
)

// LoadCognitionArtifact loads one job-owned cognition artifact under
// /memory/cognition/.
func (s *Store) LoadCognitionArtifact(
	ctx context.Context,
	relativePath string,
) (cognition.Artifact, error) {
	_ = ctx

	s.mu.Lock()
	defer s.mu.Unlock()

	normalized, path, err := s.cognitionArtifactPath(relativePath)
	if err != nil {
		return cognition.Artifact{}, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cognition.Artifact{}, nil
		}
		return cognition.Artifact{}, fmt.Errorf(
			"read cognition artifact %q: %w",
			path,
			err,
		)
	}

	return cognition.Artifact{
		RelativePath: normalized,
		Content:      string(data),
	}, nil
}

// StoreCognitionArtifact persists one job-owned cognition artifact under
// /memory/cognition/.
func (s *Store) StoreCognitionArtifact(
	ctx context.Context,
	artifact cognition.Artifact,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	normalized, path, err := s.cognitionArtifactPath(artifact.RelativePath)
	if err != nil {
		return err
	}

	data := []byte(artifact.Content)
	existing, err := os.ReadFile(path)
	if err == nil && bytes.Equal(existing, data) {
		return nil
	}
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read cognition artifact %q: %w", path, err)
	}

	if err := writeBytesFileAtomic(path, data); err != nil {
		return fmt.Errorf("write cognition artifact %q: %w", path, err)
	}
	if _, err := s.committer.CommitAll(
		ctx,
		s.rootDir,
		fmt.Sprintf("memory: update cognition artifact %s", normalized),
	); err != nil {
		return fmt.Errorf("commit cognition artifact %q: %w", normalized, err)
	}
	return nil
}

// LoadHead returns the current persisted transcript head.
func (s *Store) LoadHead(ctx context.Context) (int64, time.Time, error) {
	_ = ctx

	s.mu.Lock()
	defer s.mu.Unlock()

	head, err := s.readHeadState()
	if err != nil {
		return 0, time.Time{}, err
	}
	return head.LastSeq, head.UpdatedAt, nil
}

// LoadConsolidationCheckpoint returns the current persisted working-memory
// consolidation checkpoint.
func (s *Store) LoadConsolidationCheckpoint(
	ctx context.Context,
) (cognition.ConsolidationCheckpoint, error) {
	_ = ctx

	s.mu.Lock()
	defer s.mu.Unlock()

	checkpoint, err := s.readConsolidationCheckpoint()
	if err != nil {
		return cognition.ConsolidationCheckpoint{}, err
	}
	return consolidationCheckpointStateToCognition(checkpoint), nil
}

// LoadSemanticExtractionCheckpoint returns the current persisted semantic-memory
// extraction checkpoint.
func (s *Store) LoadSemanticExtractionCheckpoint(
	ctx context.Context,
) (cognition.SemanticExtractionCheckpoint, error) {
	_ = ctx

	s.mu.Lock()
	defer s.mu.Unlock()

	checkpoint, err := s.readSemanticExtractionCheckpoint()
	if err != nil {
		return cognition.SemanticExtractionCheckpoint{}, err
	}
	return semanticExtractionCheckpointStateToCognition(checkpoint), nil
}

// StoreConsolidationCheckpoint persists the last successful working-memory
// consolidation boundary used by checkpoint-aware transcript replay.
func (s *Store) StoreConsolidationCheckpoint(
	ctx context.Context,
	checkpoint cognition.ConsolidationCheckpoint,
) (cognition.ConsolidationCheckpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state := consolidationCheckpointStateFromCognition(checkpoint)

	if state.LastConsolidatedSeq > 0 {
		turn, ok, err := s.findTurnBySeq(state.LastConsolidatedSeq)
		if err != nil {
			return cognition.ConsolidationCheckpoint{}, err
		}
		if !ok {
			return cognition.ConsolidationCheckpoint{}, fmt.Errorf(
				"consolidation checkpoint seq %d not found in turn history",
				state.LastConsolidatedSeq,
			)
		}
		state.LastConsolidatedTurnID = strings.TrimSpace(turn.ID)
		state.LastConsolidatedAt = turn.CreatedAt.UTC()
	} else {
		state.LastConsolidatedTurnID = ""
		state.LastConsolidatedAt = time.Time{}
	}

	existing, err := s.readConsolidationCheckpoint()
	if err != nil {
		return cognition.ConsolidationCheckpoint{}, err
	}
	if sameConsolidationCheckpoint(existing, state) {
		return consolidationCheckpointStateToCognition(existing), nil
	}

	state.UpdatedAt = time.Now().UTC()
	if err := writeJSONFileAtomic(s.consolidationCheckpointPath(), state); err != nil {
		return cognition.ConsolidationCheckpoint{}, fmt.Errorf(
			"write consolidation checkpoint: %w",
			err,
		)
	}
	if _, err := s.committer.CommitAll(ctx, s.rootDir, "memory: update consolidation checkpoint"); err != nil {
		return cognition.ConsolidationCheckpoint{}, fmt.Errorf(
			"commit consolidation checkpoint: %w",
			err,
		)
	}
	return consolidationCheckpointStateToCognition(state), nil
}

// StoreSemanticExtractionCheckpoint persists the last successful semantic
// extraction boundary used by semantic checkpoint-aware transcript replay.
func (s *Store) StoreSemanticExtractionCheckpoint(
	ctx context.Context,
	checkpoint cognition.SemanticExtractionCheckpoint,
) (cognition.SemanticExtractionCheckpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state := semanticExtractionCheckpointStateFromCognition(checkpoint)

	if state.LastExtractedSeq > 0 {
		turn, ok, err := s.findTurnBySeq(state.LastExtractedSeq)
		if err != nil {
			return cognition.SemanticExtractionCheckpoint{}, err
		}
		if !ok {
			return cognition.SemanticExtractionCheckpoint{}, fmt.Errorf(
				"semantic extraction checkpoint seq %d not found in turn history",
				state.LastExtractedSeq,
			)
		}
		state.LastExtractedTurnID = strings.TrimSpace(turn.ID)
		state.LastExtractedAt = turn.CreatedAt.UTC()
	} else {
		state.LastExtractedTurnID = ""
		state.LastExtractedAt = time.Time{}
	}

	existing, err := s.readSemanticExtractionCheckpoint()
	if err != nil {
		return cognition.SemanticExtractionCheckpoint{}, err
	}
	if sameSemanticExtractionCheckpoint(existing, state) {
		return semanticExtractionCheckpointStateToCognition(existing), nil
	}

	state.UpdatedAt = time.Now().UTC()
	if err := writeJSONFileAtomic(s.semanticExtractionCheckpointPath(), state); err != nil {
		return cognition.SemanticExtractionCheckpoint{}, fmt.Errorf(
			"write semantic extraction checkpoint: %w",
			err,
		)
	}
	if _, err := s.committer.CommitAll(
		ctx,
		s.rootDir,
		"memory: update semantic extraction checkpoint",
	); err != nil {
		return cognition.SemanticExtractionCheckpoint{}, fmt.Errorf(
			"commit semantic extraction checkpoint: %w",
			err,
		)
	}
	return semanticExtractionCheckpointStateToCognition(state), nil
}

// LoadJobState loads persisted cognition trigger state for one job type.
func (s *Store) LoadJobState(ctx context.Context, jobType string) (cognition.JobState, error) {
	_ = ctx

	s.mu.Lock()
	defer s.mu.Unlock()

	path := s.jobStatePath(jobType)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cognition.JobState{}, nil
		}
		return cognition.JobState{}, fmt.Errorf("read cognition job state %q: %w", path, err)
	}

	var state cognition.JobState
	if err := json.Unmarshal(data, &state); err != nil {
		return cognition.JobState{}, fmt.Errorf("decode cognition job state %q: %w", path, err)
	}
	state.LastScheduledFor = normalizeScheduleMap(state.LastScheduledFor)
	return state, nil
}

// StoreJobState persists cognition trigger state for one job type.
func (s *Store) StoreJobState(
	ctx context.Context,
	jobType string,
	state cognition.JobState,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := s.jobStatePath(jobType)
	state.LastScheduledFor = normalizeScheduleMap(state.LastScheduledFor)

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal cognition job state %q: %w", jobType, err)
	}
	data = append(data, '\n')

	existing, err := os.ReadFile(path)
	if err == nil && bytes.Equal(existing, data) {
		return nil
	}
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read cognition job state %q: %w", path, err)
	}

	if err := writeBytesFileAtomic(path, data); err != nil {
		return fmt.Errorf("write cognition job state %q: %w", path, err)
	}
	if _, err := s.committer.CommitAll(
		ctx,
		s.rootDir,
		fmt.Sprintf("memory: update cognition trigger state %s", sanitizeCognitionName(jobType)),
	); err != nil {
		return fmt.Errorf("commit cognition job state %q: %w", jobType, err)
	}
	return nil
}

// AppendRunRecord appends one persisted cognition run record.
func (s *Store) AppendRunRecord(ctx context.Context, record cognition.RunRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	jobType := strings.TrimSpace(record.Type)
	if jobType == "" {
		return fmt.Errorf("cognition run record type is required")
	}
	startedAt := record.StartedAt.UTC()
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
		record.StartedAt = startedAt
	}
	record.FinishedAt = record.FinishedAt.UTC()
	record.Cause.Kind = strings.TrimSpace(record.Cause.Kind)
	record.Cause.RuleID = strings.TrimSpace(record.Cause.RuleID)
	record.Cause.Reason = strings.TrimSpace(record.Cause.Reason)

	path := s.runRecordPath(record.Type, startedAt)
	if err := writeJSONFileAtomic(path, record); err != nil {
		return fmt.Errorf("write cognition run record %q: %w", path, err)
	}
	if _, err := s.committer.CommitAll(
		ctx,
		s.rootDir,
		fmt.Sprintf("memory: record cognition run %s", sanitizeCognitionName(jobType)),
	); err != nil {
		return fmt.Errorf("commit cognition run record %q: %w", jobType, err)
	}
	return nil
}

func (s *Store) jobStatePath(jobType string) string {
	return filepath.Join(
		s.rootDir,
		cognitionJobsPath,
		sanitizeCognitionName(jobType)+".json",
	)
}

func (s *Store) runRecordPath(jobType string, startedAt time.Time) string {
	return filepath.Join(
		s.rootDir,
		cognitionRunsPath,
		startedAt.Format("2006"),
		startedAt.Format("01"),
		startedAt.Format("02"),
		fmt.Sprintf(
			"%s-%s.json",
			startedAt.Format("150405.000000000"),
			sanitizeCognitionName(jobType),
		),
	)
}

func normalizeScheduleMap(in map[string]time.Time) map[string]time.Time {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]time.Time, len(in))
	for key, value := range in {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		out[key] = value.UTC()
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func sameConsolidationCheckpoint(
	left consolidationCheckpointState,
	right consolidationCheckpointState,
) bool {
	return left.LastConsolidatedSeq == right.LastConsolidatedSeq &&
		strings.TrimSpace(
			left.LastConsolidatedTurnID,
		) == strings.TrimSpace(
			right.LastConsolidatedTurnID,
		) &&
		left.LastConsolidatedAt.UTC().Equal(right.LastConsolidatedAt.UTC())
}

func sameSemanticExtractionCheckpoint(
	left semanticExtractionCheckpointState,
	right semanticExtractionCheckpointState,
) bool {
	return left.LastExtractedSeq == right.LastExtractedSeq &&
		strings.TrimSpace(
			left.LastExtractedTurnID,
		) == strings.TrimSpace(
			right.LastExtractedTurnID,
		) &&
		left.LastExtractedAt.UTC().Equal(right.LastExtractedAt.UTC())
}

func (s *Store) findTurnBySeq(seq int64) (turnRecord, bool, error) {
	entries, err := s.listTurnEntries()
	if err != nil {
		return turnRecord{}, false, err
	}
	index := sort.Search(len(entries), func(i int) bool {
		return entries[i].Seq >= seq
	})
	if index == len(entries) || entries[index].Seq != seq {
		return turnRecord{}, false, nil
	}
	record, err := s.readTurn(entries[index].Path)
	if err != nil {
		return turnRecord{}, false, err
	}
	return record, true, nil
}

func consolidationCheckpointStateFromCognition(
	checkpoint cognition.ConsolidationCheckpoint,
) consolidationCheckpointState {
	return consolidationCheckpointState{
		LastConsolidatedTurnID: strings.TrimSpace(checkpoint.LastConsolidatedTurnID),
		LastConsolidatedSeq:    max(0, checkpoint.LastConsolidatedSeq),
		LastConsolidatedAt:     checkpoint.LastConsolidatedAt.UTC(),
		UpdatedAt:              checkpoint.UpdatedAt.UTC(),
	}
}

func consolidationCheckpointStateToCognition(
	checkpoint consolidationCheckpointState,
) cognition.ConsolidationCheckpoint {
	return cognition.ConsolidationCheckpoint{
		LastConsolidatedTurnID: strings.TrimSpace(checkpoint.LastConsolidatedTurnID),
		LastConsolidatedSeq:    max(0, checkpoint.LastConsolidatedSeq),
		LastConsolidatedAt:     checkpoint.LastConsolidatedAt.UTC(),
		UpdatedAt:              checkpoint.UpdatedAt.UTC(),
	}
}

func semanticExtractionCheckpointStateFromCognition(
	checkpoint cognition.SemanticExtractionCheckpoint,
) semanticExtractionCheckpointState {
	return semanticExtractionCheckpointState{
		LastExtractedTurnID: strings.TrimSpace(checkpoint.LastExtractedTurnID),
		LastExtractedSeq:    max(0, checkpoint.LastExtractedSeq),
		LastExtractedAt:     checkpoint.LastExtractedAt.UTC(),
		UpdatedAt:           checkpoint.UpdatedAt.UTC(),
	}
}

func semanticExtractionCheckpointStateToCognition(
	checkpoint semanticExtractionCheckpointState,
) cognition.SemanticExtractionCheckpoint {
	return cognition.SemanticExtractionCheckpoint{
		LastExtractedTurnID: strings.TrimSpace(checkpoint.LastExtractedTurnID),
		LastExtractedSeq:    max(0, checkpoint.LastExtractedSeq),
		LastExtractedAt:     checkpoint.LastExtractedAt.UTC(),
		UpdatedAt:           checkpoint.UpdatedAt.UTC(),
	}
}

func sanitizeCognitionName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "job"
	}

	var b strings.Builder
	for _, r := range name {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
			b.WriteRune(unicode.ToLower(r))
		case r == '.', r == '-', r == '_':
			b.WriteRune('_')
		default:
			b.WriteRune('_')
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "job"
	}
	return out
}

func (s *Store) cognitionArtifactPath(relativePath string) (string, string, error) {
	normalized, err := normalizeCognitionArtifactPath(relativePath)
	if err != nil {
		return "", "", err
	}
	return normalized, filepath.Join(
		s.rootDir,
		cognitionDirPath,
		filepath.FromSlash(normalized),
	), nil
}

func normalizeCognitionArtifactPath(relativePath string) (string, error) {
	relativePath = strings.TrimSpace(relativePath)
	if relativePath == "" {
		return "", fmt.Errorf("cognition artifact path is required")
	}
	if filepath.IsAbs(relativePath) {
		return "", fmt.Errorf("cognition artifact path must be relative: %q", relativePath)
	}

	clean := filepath.Clean(filepath.FromSlash(relativePath))
	if clean == "." {
		return "", fmt.Errorf("cognition artifact path is required")
	}

	normalized := filepath.ToSlash(clean)
	if normalized == ".." || strings.HasPrefix(normalized, "../") {
		return "", fmt.Errorf(
			"cognition artifact path must stay under cognition/: %q",
			relativePath,
		)
	}
	return normalized, nil
}
