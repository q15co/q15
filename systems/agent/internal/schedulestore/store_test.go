package schedulestore

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/q15co/q15/systems/agent/internal/schedule"
)

func TestStoreInitCreatesStandaloneStateRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "schedule")
	store := New(root)
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	for _, path := range []string{
		filepath.Join(root, "jobs"),
		filepath.Join(root, "runs"),
	} {
		if info, err := os.Stat(path); err != nil {
			t.Fatalf("Stat(%q) error = %v", path, err)
		} else if !info.IsDir() {
			t.Fatalf("%q is not a directory", path)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); !os.IsNotExist(err) {
		t.Fatalf("standalone store unexpectedly created .git: %v", err)
	}
}

func TestStoreScheduledJobRoundTrip(t *testing.T) {
	root := filepath.Join(t.TempDir(), "schedule")
	store := newTestStore(t, root)
	job := testJob()

	if err := store.StoreJob(context.Background(), job); err != nil {
		t.Fatalf("StoreJob() error = %v", err)
	}
	path := filepath.Join(root, "jobs", "job-01.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Stat(job) error = %v", err)
	}
	jobs, err := store.LoadJobs(context.Background())
	if err != nil {
		t.Fatalf("LoadJobs() error = %v", err)
	}
	if !reflect.DeepEqual(jobs, []schedule.Job{job}) {
		t.Fatalf("LoadJobs() = %#v, want %#v", jobs, []schedule.Job{job})
	}

	if err := store.StoreJob(context.Background(), job); err != nil {
		t.Fatalf("StoreJob() no-op error = %v", err)
	}
	job.Prompt = "Review the latest overnight changes."
	if err := store.StoreJob(context.Background(), job); err != nil {
		t.Fatalf("StoreJob() update error = %v", err)
	}
	jobs, err = store.LoadJobs(context.Background())
	if err != nil {
		t.Fatalf("LoadJobs() after update error = %v", err)
	}
	if len(jobs) != 1 || jobs[0].Prompt != job.Prompt {
		t.Fatalf("LoadJobs() after update = %#v", jobs)
	}
}

func TestStoreDeleteScheduledJobIsIdempotent(t *testing.T) {
	root := filepath.Join(t.TempDir(), "schedule")
	store := newTestStore(t, root)
	job := testJob()
	if err := store.StoreJob(context.Background(), job); err != nil {
		t.Fatalf("StoreJob() error = %v", err)
	}

	if err := store.DeleteJob(context.Background(), job.ID); err != nil {
		t.Fatalf("DeleteJob() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "jobs", job.ID+".json")); !os.IsNotExist(err) {
		t.Fatalf("deleted job stat error = %v, want not exist", err)
	}
	if err := store.DeleteJob(context.Background(), job.ID); err != nil {
		t.Fatalf("DeleteJob() second error = %v", err)
	}
}

func TestStoreAppendScheduledRunRecord(t *testing.T) {
	root := filepath.Join(t.TempDir(), "schedule")
	store := newTestStore(t, root)
	record := testRunRecord()

	if err := store.AppendRunRecord(context.Background(), record); err != nil {
		t.Fatalf("AppendRunRecord() error = %v", err)
	}
	path := filepath.Join(
		root,
		"runs",
		"2026",
		"07",
		"18",
		"080001.234000000-run-01.json",
	)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(run record) error = %v", err)
	}
	var got schedule.RunRecord
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal(run record) error = %v", err)
	}
	if !reflect.DeepEqual(got, record) {
		t.Fatalf("run record = %#v, want %#v", got, record)
	}

	if err := store.AppendRunRecord(context.Background(), record); err != nil {
		t.Fatalf("AppendRunRecord() idempotent error = %v", err)
	}
	conflicting := record
	conflicting.Output = "different output"
	if err := store.AppendRunRecord(context.Background(), conflicting); err == nil {
		t.Fatal("AppendRunRecord() conflicting error = nil, want non-nil")
	}
}

func TestStoreQueryRunsReturnsExactOwnerScopedAggregatesAndBoundedDetails(t *testing.T) {
	store := newTestStore(t, filepath.Join(t.TempDir(), "schedule"))
	base := testRunRecord()
	records := []schedule.RunRecord{
		base,
		func() schedule.RunRecord {
			record := base
			record.ID = "run-02"
			record.Status = schedule.StatusFailed
			record.Error = "model request failed"
			record.Output = ""
			record.StartedAt = base.StartedAt.Add(time.Minute)
			record.FinishedAt = base.FinishedAt.Add(time.Minute)
			return record
		}(),
		func() schedule.RunRecord {
			record := base
			record.ID = "run-03"
			record.JobID = "job-02"
			record.JobName = "Other job"
			record.StartedAt = base.StartedAt.Add(2 * time.Minute)
			record.FinishedAt = base.FinishedAt.Add(2 * time.Minute)
			return record
		}(),
		func() schedule.RunRecord {
			record := base
			record.ID = "run-04"
			record.Owner.ChatID = "other-chat"
			record.StartedAt = base.StartedAt.Add(3 * time.Minute)
			record.FinishedAt = base.FinishedAt.Add(3 * time.Minute)
			return record
		}(),
	}
	for _, record := range records {
		if err := store.AppendRunRecord(context.Background(), record); err != nil {
			t.Fatalf("AppendRunRecord(%q) error = %v", record.ID, err)
		}
	}

	history, err := store.QueryRuns(context.Background(), schedule.RunQuery{
		Owner: base.Owner,
		Filter: schedule.RunFilter{
			JobName: "Morning review",
			Since:   base.StartedAt,
			Before:  base.StartedAt.Add(3 * time.Minute),
			Limit:   1,
		},
	})
	if err != nil {
		t.Fatalf("QueryRuns() error = %v", err)
	}
	if history.Matched != 2 {
		t.Fatalf("QueryRuns().Matched = %d, want 2", history.Matched)
	}
	if history.StatusCounts[schedule.StatusSucceeded] != 1 ||
		history.StatusCounts[schedule.StatusFailed] != 1 {
		t.Fatalf("QueryRuns().StatusCounts = %#v", history.StatusCounts)
	}
	if len(history.Runs) != 1 || history.Runs[0].ID != "run-02" {
		t.Fatalf("QueryRuns().Runs = %#v, want newest run-02 only", history.Runs)
	}

	failed, err := store.QueryRuns(context.Background(), schedule.RunQuery{
		Owner: base.Owner,
		Filter: schedule.RunFilter{
			JobID:    base.JobID,
			Statuses: []schedule.Status{schedule.StatusFailed},
			Limit:    10,
		},
	})
	if err != nil {
		t.Fatalf("QueryRuns(failed) error = %v", err)
	}
	if failed.Matched != 1 || len(failed.Runs) != 1 || failed.Runs[0].ID != "run-02" {
		t.Fatalf("QueryRuns(failed) = %#v", failed)
	}
}

func TestStoreQueryRunsRejectsUnsafeFilters(t *testing.T) {
	store := newTestStore(t, filepath.Join(t.TempDir(), "schedule"))
	owner := testRunRecord().Owner
	tests := []schedule.RunFilter{
		{Limit: 0},
		{Limit: maxRunQueryLimit + 1},
		{Limit: 1, Statuses: []schedule.Status{schedule.StatusRunning}},
		{Limit: 1, Statuses: []schedule.Status{"unknown"}},
		{Limit: 1, Since: time.Now(), Before: time.Now().Add(-time.Minute)},
	}
	for _, filter := range tests {
		if _, err := store.QueryRuns(context.Background(), schedule.RunQuery{
			Owner:  owner,
			Filter: filter,
		}); err == nil {
			t.Fatalf("QueryRuns(%#v) error = nil, want non-nil", filter)
		}
	}
}

func TestStoreSerializesConcurrentWrites(t *testing.T) {
	root := filepath.Join(t.TempDir(), "schedule")
	store := newTestStore(t, root)

	const jobs = 32
	errs := make(chan error, jobs)
	var wg sync.WaitGroup
	for i := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			job := testJob()
			job.ID = fmt.Sprintf("job-%02d", i)
			errs <- store.StoreJob(context.Background(), job)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("StoreJob() concurrent error = %v", err)
		}
	}

	got, err := store.LoadJobs(context.Background())
	if err != nil {
		t.Fatalf("LoadJobs() error = %v", err)
	}
	if len(got) != jobs {
		t.Fatalf("LoadJobs() len = %d, want %d", len(got), jobs)
	}
}

func TestStoreRejectsInvalidRootAndJobID(t *testing.T) {
	for _, root := range []string{"", "relative"} {
		store := New(root)
		if err := store.Init(context.Background()); err == nil {
			t.Fatalf("Init(%q) error = nil, want non-nil", root)
		}
	}

	store := newTestStore(t, filepath.Join(t.TempDir(), "schedule"))
	if err := store.StoreJob(context.Background(), schedule.Job{ID: "../escape"}); err == nil {
		t.Fatal("StoreJob() error = nil, want invalid id error")
	}
	if err := store.DeleteJob(context.Background(), "../escape"); err == nil {
		t.Fatal("DeleteJob() error = nil, want invalid id error")
	}
	if err := store.AppendRunRecord(context.Background(), schedule.RunRecord{
		ID:        "run-invalid-job",
		JobID:     "../escape",
		StartedAt: time.Now(),
	}); err == nil {
		t.Fatal("AppendRunRecord() error = nil, want invalid id error")
	}
	if err := store.AppendRunRecord(context.Background(), schedule.RunRecord{
		ID:        "../escape",
		JobID:     "job-valid",
		StartedAt: time.Now(),
	}); err == nil {
		t.Fatal("AppendRunRecord() error = nil, want invalid run id error")
	}
}

func TestStoreHonorsCanceledContextBeforeMutation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "schedule")
	store := newTestStore(t, root)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := store.StoreJob(ctx, testJob()); err == nil {
		t.Fatal("StoreJob() error = nil, want canceled context")
	}
	entries, err := os.ReadDir(filepath.Join(root, "jobs"))
	if err != nil {
		t.Fatalf("ReadDir(jobs) error = %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("jobs after canceled write = %d, want 0", len(entries))
	}
}

func newTestStore(t *testing.T, root string) *Store {
	t.Helper()
	store := New(root)
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	return store
}

func testJob() schedule.Job {
	return schedule.Job{
		SchemaVersion: schedule.SchemaVersion,
		ID:            "job-01",
		Name:          "Morning review",
		Prompt:        "Review the overnight changes.",
		Kind:          schedule.KindRecurring,
		Cron:          "0 8 * * *",
		Notify:        true,
		Model: schedule.ModelTarget{
			Provider: "provider",
			Ref:      "model",
		},
		MaxTurns: 16,
		Owner: schedule.Owner{
			Channel: "telegram",
			ChatID:  "123",
			UserID:  "456",
		},
		CreatedAt: time.Date(2026, time.July, 18, 8, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, time.July, 18, 8, 0, 0, 0, time.UTC),
		NextFire:  time.Date(2026, time.July, 19, 8, 0, 0, 0, time.UTC),
	}
}

func testRunRecord() schedule.RunRecord {
	return schedule.RunRecord{
		SchemaVersion: schedule.SchemaVersion,
		ID:            "run-01",
		JobID:         "job-01",
		JobName:       "Morning review",
		Owner: schedule.Owner{
			Channel: "telegram",
			ChatID:  "123",
			UserID:  "456",
		},
		Status: schedule.StatusSucceeded,
		Model: schedule.ModelTarget{
			Provider: "provider",
			Ref:      "model",
		},
		ScheduledFor: time.Date(2026, time.July, 18, 8, 0, 0, 0, time.UTC),
		StartedAt:    time.Date(2026, time.July, 18, 8, 0, 1, 234000000, time.UTC),
		FinishedAt:   time.Date(2026, time.July, 18, 8, 0, 4, 0, time.UTC),
		Output:       "No overnight issues found.",
	}
}
