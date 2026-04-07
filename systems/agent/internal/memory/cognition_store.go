package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/q15co/q15/systems/agent/internal/cognition"
)

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
