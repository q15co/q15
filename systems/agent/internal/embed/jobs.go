package embed

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Sync job statuses reported in SyncJob.Status.
const (
	SyncJobStatusRunning   = "running"
	SyncJobStatusCompleted = "completed"
	SyncJobStatusFailed    = "failed"
	SyncJobStatusCancelled = "cancelled"
)

// maxSyncJobHistory bounds how many terminal jobs the manager retains. The
// active job is always retained regardless of this bound.
const maxSyncJobHistory = 10

// syncJobIDPrefix prefixes every managed sync job id.
const syncJobIDPrefix = "embed-sync-"

// SyncJob is a point-in-time snapshot of one managed sync.
type SyncJob struct {
	ID         string       `json:"id"`
	Status     string       `json:"status"` // running|completed|failed|cancelled
	StartedAt  time.Time    `json:"started_at"`
	FinishedAt *time.Time   `json:"finished_at,omitempty"`
	Progress   SyncProgress `json:"progress"`
	Err        string       `json:"error,omitempty"`
	// Result carries the final SyncResult reported by the sync once the job
	// completes successfully. It stays nil while the job runs and on failure.
	Result *SyncResult `json:"result,omitempty"`
}

// SyncJobManager runs embedding syncs as cancellable background jobs: one
// active sync at a time, started on a context detached from the caller's run
// so it survives turn cancellation, with bounded terminal history and
// by-value snapshots for pollers. Job tracking is in-memory; durable
// per-batch progress lives in the sync-state file, so interrupted syncs
// resume from their last checkpoint on the next run.
type SyncJobManager struct {
	mu      sync.Mutex
	service *Service
	nextID  int
	active  string
	order   []string
	jobs    map[string]*managedSyncJob
}

// managedSyncJob is the manager's mutable record for one sync job. Snapshots
// handed to callers are always copies of job.
type managedSyncJob struct {
	job             SyncJob
	cancel          context.CancelFunc
	cancelRequested bool
}

// snapshotJob returns a copy of one job snapshot with pointer fields
// deep-copied so callers can never mutate manager state.
func snapshotJob(job SyncJob) SyncJob {
	if job.Result != nil {
		result := *job.Result
		job.Result = &result
	}
	if job.FinishedAt != nil {
		finished := *job.FinishedAt
		job.FinishedAt = &finished
	}
	return job
}

// NewSyncJobManager constructs a sync job manager around one embedding
// service.
func NewSyncJobManager(service *Service) *SyncJobManager {
	return &SyncJobManager{
		service: service,
		jobs:    make(map[string]*managedSyncJob),
	}
}

// Start launches one background sync job. The job runs on a context derived
// from ctx via context.WithoutCancel plus a dedicated cancel func, so it
// survives cancellation of ctx while remaining independently cancellable via
// Cancel. Only one sync runs at a time across all callers.
func (m *SyncJobManager) Start(ctx context.Context, opts SyncOptions) (SyncJob, error) {
	if m == nil || m.service == nil {
		return SyncJob{}, fmt.Errorf("embed service is not configured")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active != "" {
		return SyncJob{}, fmt.Errorf("sync already running (job %s)", m.active)
	}
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	if m.jobs == nil {
		m.jobs = make(map[string]*managedSyncJob)
	}
	m.nextID++
	rec := &managedSyncJob{
		job: SyncJob{
			ID:        fmt.Sprintf("%s%d", syncJobIDPrefix, m.nextID),
			Status:    SyncJobStatusRunning,
			StartedAt: time.Now(),
		},
		cancel: cancel,
	}
	m.jobs[rec.job.ID] = rec
	m.order = append(m.order, rec.job.ID)
	m.active = rec.job.ID
	callerProgress := opts.Progress
	opts.Progress = func(p SyncProgress) {
		if callerProgress != nil {
			callerProgress(p)
		}
		m.mu.Lock()
		rec.job.Progress = p
		m.mu.Unlock()
	}
	go m.run(runCtx, rec, opts, cancel)
	return rec.job, nil
}

// Get returns a snapshot of one tracked job by id.
func (m *SyncJobManager) Get(id string) (SyncJob, bool) {
	if m == nil {
		return SyncJob{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.jobs[id]
	if !ok {
		return SyncJob{}, false
	}
	return snapshotJob(rec.job), true
}

// Active returns a snapshot of the running job, if any.
func (m *SyncJobManager) Active() (SyncJob, bool) {
	if m == nil {
		return SyncJob{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active == "" {
		return SyncJob{}, false
	}
	rec, ok := m.jobs[m.active]
	if !ok {
		return SyncJob{}, false
	}
	return snapshotJob(rec.job), true
}

// Cancel requests cancellation of one running job and returns the snapshot as
// of the request. It never sets a terminal status itself: the job goroutine's
// return path resolves the job to cancelled once the underlying sync stops.
func (m *SyncJobManager) Cancel(id string) (SyncJob, error) {
	if m == nil {
		return SyncJob{}, fmt.Errorf("embed sync jobs are not configured")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.jobs[id]
	if !ok {
		return SyncJob{}, fmt.Errorf("sync job %q not found", id)
	}
	if rec.job.Status != SyncJobStatusRunning {
		return SyncJob{}, fmt.Errorf(
			"sync job %s is not running (status %s)",
			id,
			rec.job.Status,
		)
	}
	rec.cancelRequested = true
	rec.cancel()
	return snapshotJob(rec.job), nil
}

// run executes one sync to completion on runCtx and records the terminal
// job state. cancel is the job's dedicated cancel func and is always
// invoked to release context resources.
func (m *SyncJobManager) run(
	runCtx context.Context,
	rec *managedSyncJob,
	opts SyncOptions,
	cancel context.CancelFunc,
) {
	defer cancel()
	result, err := m.service.Sync(runCtx, opts)
	m.mu.Lock()
	defer m.mu.Unlock()
	finished := time.Now()
	rec.job.FinishedAt = &finished
	switch {
	case err == nil:
		rec.job.Status = SyncJobStatusCompleted
		rec.job.Result = &result
	case errors.Is(err, context.Canceled) || rec.cancelRequested:
		rec.job.Status = SyncJobStatusCancelled
		rec.job.Err = err.Error()
	default:
		rec.job.Status = SyncJobStatusFailed
		rec.job.Err = err.Error()
	}
	if m.active == rec.job.ID {
		m.active = ""
	}
	m.pruneHistoryLocked()
}

// pruneHistoryLocked drops the oldest terminal jobs beyond the most recent
// maxSyncJobHistory. The active job is always retained.
func (m *SyncJobManager) pruneHistoryLocked() {
	terminal := 0
	for _, id := range m.order {
		if rec, ok := m.jobs[id]; ok && rec.job.Status != SyncJobStatusRunning {
			terminal++
		}
	}
	excess := terminal - maxSyncJobHistory
	if excess <= 0 {
		return
	}
	kept := make([]string, 0, len(m.order))
	for _, id := range m.order {
		rec, ok := m.jobs[id]
		if ok && excess > 0 && rec.job.Status != SyncJobStatusRunning {
			delete(m.jobs, id)
			excess--
			continue
		}
		kept = append(kept, id)
	}
	m.order = kept
}
