package memory

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/q15co/q15/systems/agent/internal/cognition"
)

// #379 layer 2: a failing cognition job in a retry storm must not commit one
// git commit per run record. Failure records stay on disk for forensics, but
// only the first record of a consecutive failure streak (ConsecutiveCount 1)
// is committed; successes always commit.
func TestAppendRunRecordCommitHygiene(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory")
	committer := &fakeCommitter{}
	store := newTestStore(root, committer)
	ctx := context.Background()

	if err := store.Init(ctx); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	baseline := committer.commitCalls

	at := time.Date(2026, time.October, 20, 12, 0, 0, 0, time.UTC)
	appendFailure := func(nth int, count int) string {
		record := cognition.RunRecord{
			Type:             "semantic_memory.extract",
			StartedAt:        at.Add(time.Duration(nth) * time.Second),
			FinishedAt:       at.Add(time.Duration(nth)*time.Second + time.Millisecond),
			Succeeded:        false,
			Error:            `apply cognition result for job "semantic_memory.extract": boom`,
			ConsecutiveCount: count,
		}
		if err := store.AppendRunRecord(ctx, record); err != nil {
			t.Fatalf("AppendRunRecord(%d) error = %v", nth, err)
		}
		path := store.runRecordPath(record.Type, at.Add(time.Duration(nth)*time.Second))
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("run record %d missing on disk: %v", nth, err)
		}
		return path
	}

	// First failure of a streak (count 0 from uninstrumented producers, and an
	// explicit 1) both commit: provenance of a NEW failure class is kept.
	appendFailure(0, 0)
	appendFailure(1, 1)
	if got, want := committer.commitCalls-baseline, 2; got != want {
		t.Fatalf("commits after first-of-streak failures = %d, want %d", got, want)
	}

	// Streak repeats are written without a commit.
	appendFailure(2, 2)
	appendFailure(3, 25)
	if got, want := committer.commitCalls-baseline, 2; got != want {
		t.Fatalf("commits after streak repeats = %d, want %d (repeats must not commit)", got, want)
	}

	// Successes always commit, one per run.
	for i := 4; i < 6; i++ {
		record := cognition.RunRecord{
			Type:       "semantic_memory.extract",
			StartedAt:  at.Add(time.Duration(i) * time.Second),
			FinishedAt: at.Add(time.Duration(i)*time.Second + time.Millisecond),
			Succeeded:  true,
			Summary:    "ok",
		}
		if err := store.AppendRunRecord(ctx, record); err != nil {
			t.Fatalf("AppendRunRecord(success %d) error = %v", i, err)
		}
	}
	if got, want := committer.commitCalls-baseline, 4; got != want {
		t.Fatalf("commits after two successes = %d, want %d", got, want)
	}
}
