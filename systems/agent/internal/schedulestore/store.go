// Package schedulestore persists agent-created scheduled jobs and their run
// records in an agent-owned filesystem root.
package schedulestore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/q15co/q15/systems/agent/internal/atomicfile"
	"github.com/q15co/q15/systems/agent/internal/schedule"
)

const (
	jobsDir          = "jobs"
	runsDir          = "runs"
	maxRunQueryLimit = 100
)

// Store persists scheduled jobs beneath one standalone state root.
type Store struct {
	mu   sync.RWMutex
	root string
}

var _ schedule.Store = (*Store)(nil)

// New constructs scheduled-job persistence rooted at root.
func New(root string) *Store {
	root = strings.TrimSpace(root)
	if root != "" {
		root = filepath.Clean(root)
	}
	return &Store{root: root}
}

// Init creates the scheduled-job state directories without modifying existing
// files.
func (s *Store) Init(ctx context.Context) error {
	if err := s.validate(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := atomicfile.EnsureDirectory(s.root, 0o755); err != nil {
		return fmt.Errorf("create scheduled-job root %q: %w", s.root, err)
	}
	for _, relative := range []string{jobsDir, runsDir} {
		path := filepath.Join(s.root, relative)
		if err := atomicfile.EnsureDirectory(path, 0o755); err != nil {
			return fmt.Errorf("create scheduled-job directory %q: %w", path, err)
		}
	}
	return nil
}

// LoadJobs loads all persisted scheduled-job definitions in stable ID order.
func (s *Store) LoadJobs(ctx context.Context) ([]schedule.Job, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	dir := filepath.Join(s.root, jobsDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read scheduled jobs dir %q: %w", dir, err)
	}

	jobs := make([]schedule.Job, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read scheduled job %q: %w", path, err)
		}

		var job schedule.Job
		if err := json.Unmarshal(data, &job); err != nil {
			return nil, fmt.Errorf("decode scheduled job %q: %w", path, err)
		}
		if err := validateJobID(job.ID); err != nil {
			return nil, fmt.Errorf("decode scheduled job %q: %w", path, err)
		}
		if want := job.ID + ".json"; entry.Name() != want {
			return nil, fmt.Errorf(
				"scheduled job file %q does not match stored id %q",
				entry.Name(),
				job.ID,
			)
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}

// StoreJob atomically creates or replaces one scheduled-job definition.
func (s *Store) StoreJob(ctx context.Context, job schedule.Job) error {
	if err := s.validate(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}

	path, err := s.jobPath(job.ID)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal scheduled job %q: %w", job.ID, err)
	}
	data = append(data, '\n')

	snapshot, err := captureFile(path)
	if err != nil {
		return fmt.Errorf("read scheduled job %q: %w", path, err)
	}
	if snapshot.exists && bytes.Equal(snapshot.data, data) {
		return nil
	}
	if err := atomicfile.WriteBytes(path, data); err != nil {
		return restoreFile(
			path,
			snapshot,
			fmt.Errorf("write scheduled job %q: %w", path, err),
		)
	}
	return nil
}

// DeleteJob removes one active scheduled-job definition. Missing jobs are an
// idempotent success so deleting an already-fired one-shot remains safe.
func (s *Store) DeleteJob(ctx context.Context, jobID string) error {
	if err := s.validate(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}

	path, err := s.jobPath(jobID)
	if err != nil {
		return err
	}
	snapshot, err := captureFile(path)
	if err != nil {
		return fmt.Errorf("read scheduled job %q: %w", path, err)
	}
	if !snapshot.exists {
		return nil
	}
	if err := atomicfile.Remove(path); err != nil {
		return restoreFile(
			path,
			snapshot,
			fmt.Errorf("delete scheduled job %q: %w", path, err),
		)
	}
	return nil
}

// AppendRunRecord persists one dated scheduled-job run provenance record.
func (s *Store) AppendRunRecord(ctx context.Context, record schedule.RunRecord) error {
	if err := s.validate(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := validateJobID(record.JobID); err != nil {
		return err
	}
	if err := validateRunID(record.ID); err != nil {
		return err
	}
	startedAt := record.StartedAt.UTC()
	if startedAt.IsZero() {
		return fmt.Errorf("scheduled run started_at is required")
	}
	record.ScheduledFor = record.ScheduledFor.UTC()
	record.StartedAt = startedAt
	record.FinishedAt = record.FinishedAt.UTC()

	path := filepath.Join(
		s.root,
		runsDir,
		startedAt.Format("2006"),
		startedAt.Format("01"),
		startedAt.Format("02"),
		fmt.Sprintf("%s-%s.json", startedAt.Format("150405.000000000"), record.ID),
	)
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal scheduled run record %q: %w", record.JobID, err)
	}
	data = append(data, '\n')

	existing, err := os.ReadFile(path)
	if err == nil {
		if bytes.Equal(existing, data) {
			return nil
		}
		return fmt.Errorf("scheduled run record %q already exists with different contents", path)
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("read scheduled run record %q: %w", path, err)
	}
	if err := atomicfile.WriteBytes(path, data); err != nil {
		if removeErr := atomicfile.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
			err = errors.Join(err, fmt.Errorf("remove partial run record %q: %w", path, removeErr))
		}
		return fmt.Errorf("write scheduled run record %q: %w", path, err)
	}
	return nil
}

// QueryRuns returns exact owner-scoped aggregates and a bounded newest-first
// detail window over archived run records.
func (s *Store) QueryRuns(
	ctx context.Context,
	query schedule.RunQuery,
) (schedule.RunHistory, error) {
	if err := s.validate(); err != nil {
		return schedule.RunHistory{}, err
	}
	if err := ctx.Err(); err != nil {
		return schedule.RunHistory{}, err
	}
	query, err := normalizeRunQuery(query)
	if err != nil {
		return schedule.RunHistory{}, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return schedule.RunHistory{}, err
	}

	history := schedule.RunHistory{
		StatusCounts: make(map[schedule.Status]int),
		Runs:         make([]schedule.RunRecord, 0, query.Filter.Limit),
	}
	root := filepath.Join(s.root, runsDir)
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".json") {
			return nil
		}

		record, err := readRunRecord(path)
		if err != nil {
			return err
		}
		if !runMatches(record, query) {
			return nil
		}
		history.Matched++
		history.StatusCounts[record.Status]++
		history.Runs = insertNewestRun(history.Runs, record, query.Filter.Limit)
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return history, nil
		}
		return schedule.RunHistory{}, fmt.Errorf("query scheduled runs in %q: %w", root, err)
	}
	return history, nil
}

func normalizeRunQuery(query schedule.RunQuery) (schedule.RunQuery, error) {
	query.Owner = schedule.Owner{
		Channel: strings.TrimSpace(query.Owner.Channel),
		ChatID:  strings.TrimSpace(query.Owner.ChatID),
		UserID:  strings.TrimSpace(query.Owner.UserID),
	}
	if query.Owner.Channel == "" || query.Owner.ChatID == "" || query.Owner.UserID == "" {
		return schedule.RunQuery{}, fmt.Errorf("scheduled run query owner is required")
	}
	query.Filter.JobID = strings.TrimSpace(query.Filter.JobID)
	query.Filter.JobName = strings.TrimSpace(query.Filter.JobName)
	query.Filter.Since = query.Filter.Since.UTC()
	query.Filter.Before = query.Filter.Before.UTC()
	if query.Filter.Limit <= 0 {
		return schedule.RunQuery{}, fmt.Errorf("scheduled run query limit must be positive")
	}
	if query.Filter.Limit > maxRunQueryLimit {
		return schedule.RunQuery{}, fmt.Errorf(
			"scheduled run query limit must not exceed %d",
			maxRunQueryLimit,
		)
	}
	for _, status := range query.Filter.Statuses {
		switch status {
		case schedule.StatusSucceeded,
			schedule.StatusFailed,
			schedule.StatusInterrupted,
			schedule.StatusModelUnavailable:
		default:
			return schedule.RunQuery{}, fmt.Errorf(
				"invalid archived scheduled run status %q",
				status,
			)
		}
	}
	if !query.Filter.Since.IsZero() &&
		!query.Filter.Before.IsZero() &&
		!query.Filter.Since.Before(query.Filter.Before) {
		return schedule.RunQuery{}, fmt.Errorf("scheduled run query since must be before before")
	}
	return query, nil
}

func readRunRecord(path string) (schedule.RunRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return schedule.RunRecord{}, fmt.Errorf("read scheduled run %q: %w", path, err)
	}
	var record schedule.RunRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return schedule.RunRecord{}, fmt.Errorf("decode scheduled run %q: %w", path, err)
	}
	if record.SchemaVersion != schedule.SchemaVersion {
		return schedule.RunRecord{}, fmt.Errorf(
			"decode scheduled run %q: unsupported schema version %d",
			path,
			record.SchemaVersion,
		)
	}
	if err := validateJobID(record.JobID); err != nil {
		return schedule.RunRecord{}, fmt.Errorf("decode scheduled run %q: %w", path, err)
	}
	if err := validateRunID(record.ID); err != nil {
		return schedule.RunRecord{}, fmt.Errorf("decode scheduled run %q: %w", path, err)
	}
	if record.StartedAt.IsZero() {
		return schedule.RunRecord{}, fmt.Errorf(
			"decode scheduled run %q: started_at is required",
			path,
		)
	}
	switch record.Status {
	case schedule.StatusSucceeded,
		schedule.StatusFailed,
		schedule.StatusInterrupted,
		schedule.StatusModelUnavailable:
	default:
		return schedule.RunRecord{}, fmt.Errorf(
			"decode scheduled run %q: invalid archived status %q",
			path,
			record.Status,
		)
	}
	record.ScheduledFor = record.ScheduledFor.UTC()
	record.StartedAt = record.StartedAt.UTC()
	record.FinishedAt = record.FinishedAt.UTC()
	return record, nil
}

func runMatches(record schedule.RunRecord, query schedule.RunQuery) bool {
	recordOwner := schedule.Owner{
		Channel: strings.TrimSpace(record.Owner.Channel),
		ChatID:  strings.TrimSpace(record.Owner.ChatID),
		UserID:  strings.TrimSpace(record.Owner.UserID),
	}
	if recordOwner != query.Owner {
		return false
	}
	filter := query.Filter
	if filter.JobID != "" && record.JobID != filter.JobID {
		return false
	}
	if filter.JobName != "" && record.JobName != filter.JobName {
		return false
	}
	if len(filter.Statuses) > 0 && !containsStatus(filter.Statuses, record.Status) {
		return false
	}
	if !filter.Since.IsZero() && record.StartedAt.Before(filter.Since) {
		return false
	}
	return filter.Before.IsZero() || record.StartedAt.Before(filter.Before)
}

func containsStatus(statuses []schedule.Status, candidate schedule.Status) bool {
	for _, status := range statuses {
		if status == candidate {
			return true
		}
	}
	return false
}

func insertNewestRun(
	runs []schedule.RunRecord,
	record schedule.RunRecord,
	limit int,
) []schedule.RunRecord {
	index := sort.Search(len(runs), func(i int) bool {
		if runs[i].StartedAt.Equal(record.StartedAt) {
			return runs[i].ID < record.ID
		}
		return runs[i].StartedAt.Before(record.StartedAt)
	})
	if index >= limit {
		return runs
	}
	runs = append(runs, schedule.RunRecord{})
	copy(runs[index+1:], runs[index:])
	runs[index] = record
	if len(runs) > limit {
		runs = runs[:limit]
	}
	return runs
}

func (s *Store) validate() error {
	if s == nil || strings.TrimSpace(s.root) == "" {
		return fmt.Errorf("scheduled-job state root is required")
	}
	if !filepath.IsAbs(s.root) {
		return fmt.Errorf("scheduled-job state root must be absolute")
	}
	return nil
}

func (s *Store) jobPath(jobID string) (string, error) {
	if err := validateJobID(jobID); err != nil {
		return "", err
	}
	return filepath.Join(s.root, jobsDir, jobID+".json"), nil
}

func validateJobID(jobID string) error {
	if jobID == "" {
		return fmt.Errorf("scheduled job id is required")
	}
	if jobID != strings.TrimSpace(jobID) ||
		jobID == "." ||
		jobID == ".." ||
		strings.ContainsAny(jobID, `/\`) {
		return fmt.Errorf("scheduled job id %q is invalid", jobID)
	}
	return nil
}

func validateRunID(runID string) error {
	if runID == "" {
		return fmt.Errorf("scheduled run id is required")
	}
	if runID != strings.TrimSpace(runID) ||
		runID == "." ||
		runID == ".." ||
		strings.ContainsAny(runID, `/\`) {
		return fmt.Errorf("scheduled run id %q is invalid", runID)
	}
	return nil
}

type fileSnapshot struct {
	exists bool
	data   []byte
	mode   os.FileMode
}

func captureFile(path string) (fileSnapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fileSnapshot{}, nil
		}
		return fileSnapshot{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return fileSnapshot{}, err
	}
	return fileSnapshot{
		exists: true,
		data:   data,
		mode:   info.Mode().Perm(),
	}, nil
}

func restoreFile(path string, snapshot fileSnapshot, cause error) error {
	var restoreErr error
	if snapshot.exists {
		restoreErr = atomicfile.WriteBytes(path, snapshot.data)
		if restoreErr == nil {
			restoreErr = os.Chmod(path, snapshot.mode)
		}
	} else {
		restoreErr = atomicfile.Remove(path)
		if os.IsNotExist(restoreErr) {
			restoreErr = nil
		}
	}
	if restoreErr != nil {
		restoreErr = fmt.Errorf("restore scheduled state %q: %w", path, restoreErr)
	}
	return errors.Join(cause, restoreErr)
}
