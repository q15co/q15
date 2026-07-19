package schedule

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/q15co/q15/systems/agent/internal/bus"
	"github.com/q15co/q15/systems/agent/internal/cronexpr"
)

const (
	defaultMaxJobs               = 64
	defaultMaxTurns              = 16
	defaultRunTimeout            = 5 * time.Minute
	defaultUnavailableRetryDelay = 5 * time.Minute
	minUnavailableRetryDelay     = 30 * time.Second
	maxUnavailableRetryDelay     = time.Hour
	maxNameRunes                 = 120
	maxPromptRunes               = 20000
	maxOutputRunes               = 20000
	maxNotifyRunes               = 3500
	defaultRunHistoryLimit       = 20
	maxRunHistoryLimit           = 100
)

// Manager owns scheduled-job CRUD, due-time selection, and serial execution.
type Manager struct {
	mu      sync.Mutex
	jobs    map[string]Job
	running map[string]struct{}
	wake    chan struct{}

	store     Store
	executor  Executor
	publisher Publisher
	recorder  DeliveryRecorder

	maxJobs               int
	maxTurns              int
	runTimeout            time.Duration
	unavailableRetryDelay time.Duration
	allowedUserIDs        map[string]struct{}
	defaultModel          func() ModelTarget
	modelExists           func(ModelTarget) bool
	toolExists            func(string) bool
	now                   func() time.Time
	newID                 func() (string, error)
}

// NewManager loads persisted jobs and constructs a scheduled-job manager.
func NewManager(ctx context.Context, cfg Config) (*Manager, error) {
	if cfg.Store == nil {
		return nil, fmt.Errorf("schedule store is required")
	}
	if cfg.Executor == nil {
		return nil, fmt.Errorf("schedule executor is required")
	}
	if cfg.Publisher == nil {
		return nil, fmt.Errorf("schedule publisher is required")
	}
	if cfg.DeliveryRecorder == nil {
		return nil, fmt.Errorf("schedule delivery recorder is required")
	}
	if cfg.MaxJobs <= 0 {
		cfg.MaxJobs = defaultMaxJobs
	}
	if cfg.MaxTurns <= 0 {
		cfg.MaxTurns = defaultMaxTurns
	}
	if cfg.RunTimeout <= 0 {
		cfg.RunTimeout = defaultRunTimeout
	}
	if cfg.UnavailableRetryDelay == 0 {
		cfg.UnavailableRetryDelay = defaultUnavailableRetryDelay
	}
	if cfg.UnavailableRetryDelay < minUnavailableRetryDelay ||
		cfg.UnavailableRetryDelay > maxUnavailableRetryDelay {
		return nil, fmt.Errorf(
			"model unavailable retry delay must be between %s and %s",
			minUnavailableRetryDelay,
			maxUnavailableRetryDelay,
		)
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	if cfg.NewID == nil {
		cfg.NewID = func() (string, error) {
			id, err := uuid.NewV7()
			if err != nil {
				return "", err
			}
			return "job-" + id.String(), nil
		}
	}
	if cfg.DefaultModel == nil {
		cfg.DefaultModel = func() ModelTarget { return ModelTarget{} }
	}
	if cfg.ModelExists == nil {
		cfg.ModelExists = func(ModelTarget) bool { return true }
	}
	if cfg.ToolExists == nil {
		return nil, fmt.Errorf("schedule tool catalog is required")
	}

	allowedUserIDs := make(map[string]struct{}, len(cfg.AllowedUserIDs))
	for _, id := range cfg.AllowedUserIDs {
		if id > 0 {
			allowedUserIDs[strconv.FormatInt(id, 10)] = struct{}{}
		}
	}

	m := &Manager{
		jobs:                  make(map[string]Job),
		running:               make(map[string]struct{}),
		wake:                  make(chan struct{}, 1),
		store:                 cfg.Store,
		executor:              cfg.Executor,
		publisher:             cfg.Publisher,
		recorder:              cfg.DeliveryRecorder,
		maxJobs:               cfg.MaxJobs,
		maxTurns:              cfg.MaxTurns,
		runTimeout:            cfg.RunTimeout,
		unavailableRetryDelay: cfg.UnavailableRetryDelay,
		allowedUserIDs:        allowedUserIDs,
		defaultModel:          cfg.DefaultModel,
		modelExists:           cfg.ModelExists,
		toolExists:            cfg.ToolExists,
		now:                   cfg.Now,
		newID:                 cfg.NewID,
	}

	jobs, err := cfg.Store.LoadJobs(ctx)
	if err != nil {
		return nil, fmt.Errorf("load scheduled jobs: %w", err)
	}
	now := m.nowUTC()
	for _, job := range jobs {
		normalizedToolLists := normalizeStoredToolLists(&job)
		if err := m.validateStoredJob(job); err != nil {
			return nil, fmt.Errorf("load scheduled job %q: %w", job.ID, err)
		}
		if normalizedToolLists {
			if err := m.store.StoreJob(ctx, job); err != nil {
				return nil, fmt.Errorf(
					"normalize scheduled job %q tool policy: %w",
					job.ID,
					err,
				)
			}
		}
		for _, name := range job.AllowedTools {
			if !m.toolExists(name) {
				log.Printf(
					"q15: scheduler event=job_tool_unavailable job_id=%q name=%q tool=%q",
					job.ID,
					job.Name,
					name,
				)
			}
		}
		switch {
		case job.PendingRun != nil && job.PendingNotification != nil:
			m.jobs[job.ID] = job
			m.running[job.ID] = struct{}{}
		case job.PendingRun != nil:
			if err := m.finishPendingRun(ctx, &job); err != nil {
				return nil, fmt.Errorf("recover scheduled job %q: %w", job.ID, err)
			}
			if !job.Done {
				m.jobs[job.ID] = job
			}
		case job.Done:
			if job.LastStatus == StatusRunning {
				m.markInterrupted(&job, now)
				job.PendingRun = runRecordPtr(interruptedRecord(job, now))
				if err := m.store.StoreJob(ctx, job); err != nil {
					return nil, fmt.Errorf("store interrupted scheduled job %q: %w", job.ID, err)
				}
				if err := m.finishPendingRun(ctx, &job); err != nil {
					return nil, fmt.Errorf("record interrupted scheduled job %q: %w", job.ID, err)
				}
			} else {
				return nil, fmt.Errorf(
					"load scheduled job %q: completed one-shot is missing pending run provenance",
					job.ID,
				)
			}
		case job.LastStatus == StatusRunning:
			m.markInterrupted(&job, now)
			job.PendingRun = runRecordPtr(interruptedRecord(job, now))
			if err := m.store.StoreJob(ctx, job); err != nil {
				return nil, fmt.Errorf("reconcile interrupted scheduled job %q: %w", job.ID, err)
			}
			if err := m.finishPendingRun(ctx, &job); err != nil {
				return nil, fmt.Errorf("record interrupted scheduled job %q: %w", job.ID, err)
			}
			m.jobs[job.ID] = job
		default:
			m.jobs[job.ID] = job
		}
	}
	if len(m.jobs) > m.maxJobs {
		return nil, fmt.Errorf(
			"persisted scheduled jobs exceed configured maximum: %d > %d",
			len(m.jobs),
			m.maxJobs,
		)
	}
	return m, nil
}

// Create validates, persists, and activates one owner-scoped scheduled job.
func (m *Manager) Create(
	ctx context.Context,
	owner Owner,
	req CreateRequest,
) (Job, error) {
	if m == nil {
		return Job{}, fmt.Errorf("schedule manager is not configured")
	}
	if err := m.authorize(owner); err != nil {
		return Job{}, err
	}

	now := m.nowUTC()
	job := Job{
		SchemaVersion:  SchemaVersion,
		Name:           strings.TrimSpace(req.Name),
		Prompt:         strings.TrimSpace(req.Prompt),
		ContextProfile: normalizeContextProfile(req.ContextProfile),
		Kind:           req.Kind,
		RunAt:          req.RunAt.UTC(),
		Cron:           strings.TrimSpace(req.Cron),
		Notify:         req.Notify,
		Model:          normalizeModelTarget(req.Model),
		MaxTurns:       req.MaxTurns,
		AllowedTools:   normalizeToolNames(req.AllowedTools),
		Owner:          normalizeOwner(owner),
		CreatedAt:      now,
		UpdatedAt:      now,
		LastStatus:     StatusPending,
	}
	if job.MaxTurns == 0 {
		job.MaxTurns = m.maxTurns
	}
	if job.ContextProfile == "" {
		job.ContextProfile = ContextProfileMinimal
	}
	if job.Model == (ModelTarget{}) {
		job.Model = normalizeModelTarget(m.defaultModel())
	}
	if err := m.prepareSchedule(&job, now, true); err != nil {
		return Job{}, err
	}
	if err := m.validateMutableJob(job); err != nil {
		return Job{}, err
	}
	if err := m.validateAvailableModel(job.Model); err != nil {
		return Job{}, err
	}
	if err := m.validateAvailableTools(job.AllowedTools); err != nil {
		return Job{}, err
	}

	id, err := m.newID()
	if err != nil {
		return Job{}, fmt.Errorf("generate scheduled job id: %w", err)
	}
	job.ID = strings.TrimSpace(id)
	if job.ID == "" {
		return Job{}, fmt.Errorf("generated scheduled job id is empty")
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.jobs) >= m.maxJobs {
		return Job{}, ErrMaxJobsReached
	}
	if _, exists := m.jobs[job.ID]; exists {
		return Job{}, fmt.Errorf("generated duplicate scheduled job id %q", job.ID)
	}
	if err := m.store.StoreJob(ctx, job); err != nil {
		return Job{}, fmt.Errorf("store scheduled job %q: %w", job.ID, err)
	}
	m.jobs[job.ID] = job
	m.notifyWake()
	log.Printf(
		"q15: scheduler event=job_created job_id=%q name=%q kind=%q next_fire=%q provider=%q model=%q context_profile=%q allowed_tools=%q notify=%t",
		job.ID,
		job.Name,
		job.Kind,
		job.NextFire.Format(time.RFC3339Nano),
		job.Model.Provider,
		job.Model.Ref,
		job.ContextProfile,
		job.AllowedTools,
		job.Notify,
	)
	return cloneJob(job), nil
}

// List returns the caller's scheduled jobs in deterministic next-fire order.
func (m *Manager) List(ctx context.Context, owner Owner) ([]Job, error) {
	_ = ctx
	if m == nil {
		return nil, fmt.Errorf("schedule manager is not configured")
	}
	if err := m.authorize(owner); err != nil {
		return nil, err
	}
	owner = normalizeOwner(owner)

	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Job, 0, len(m.jobs))
	for _, job := range m.jobs {
		if sameOwner(job.Owner, owner) {
			out = append(out, cloneJob(job))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].NextFire.Equal(out[j].NextFire) {
			return out[i].ID < out[j].ID
		}
		if out[i].NextFire.IsZero() {
			return false
		}
		if out[j].NextFire.IsZero() {
			return true
		}
		return out[i].NextFire.Before(out[j].NextFire)
	})
	return out, nil
}

// RunHistory returns canonical archived run aggregates and bounded details for
// the exact owner. It includes deleted jobs because run provenance is retained
// independently of active job definitions.
func (m *Manager) RunHistory(
	ctx context.Context,
	owner Owner,
	filter RunFilter,
) (RunHistory, error) {
	if m == nil {
		return RunHistory{}, fmt.Errorf("schedule manager is not configured")
	}
	if err := m.authorize(owner); err != nil {
		return RunHistory{}, err
	}
	owner = normalizeOwner(owner)
	filter.JobID = strings.TrimSpace(filter.JobID)
	filter.JobName = strings.TrimSpace(filter.JobName)
	filter.Since = filter.Since.UTC()
	filter.Before = filter.Before.UTC()
	if filter.Limit == 0 {
		filter.Limit = defaultRunHistoryLimit
	}
	if filter.Limit < 1 || filter.Limit > maxRunHistoryLimit {
		return RunHistory{}, fmt.Errorf(
			"scheduled run history limit must be between 1 and %d",
			maxRunHistoryLimit,
		)
	}
	if !filter.Since.IsZero() &&
		!filter.Before.IsZero() &&
		!filter.Since.Before(filter.Before) {
		return RunHistory{}, fmt.Errorf("since must be before before")
	}

	statuses := make([]Status, 0, len(filter.Statuses))
	seenStatuses := make(map[Status]struct{}, len(filter.Statuses))
	for _, status := range filter.Statuses {
		status = Status(strings.TrimSpace(string(status)))
		if !isArchivedRunStatus(status) {
			return RunHistory{}, fmt.Errorf("invalid archived scheduled run status %q", status)
		}
		if _, exists := seenStatuses[status]; exists {
			continue
		}
		seenStatuses[status] = struct{}{}
		statuses = append(statuses, status)
	}
	filter.Statuses = statuses

	history, err := m.store.QueryRuns(ctx, RunQuery{Owner: owner, Filter: filter})
	if err != nil {
		return RunHistory{}, fmt.Errorf("query scheduled run history: %w", err)
	}
	return cloneRunHistory(history), nil
}

// Update atomically patches one owned scheduled job while preserving identity,
// ownership, and execution history.
func (m *Manager) Update(
	ctx context.Context,
	owner Owner,
	id string,
	req UpdateRequest,
) (Job, error) {
	if m == nil {
		return Job{}, fmt.Errorf("schedule manager is not configured")
	}
	if err := m.authorize(owner); err != nil {
		return Job{}, err
	}
	id = strings.TrimSpace(id)
	owner = normalizeOwner(owner)

	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[id]
	if !ok || !sameOwner(job.Owner, owner) {
		return Job{}, ErrNotFound
	}
	if _, running := m.running[id]; running {
		return Job{}, fmt.Errorf("scheduled job %q is currently running", id)
	}

	updated := cloneJob(job)
	scheduleChanged := false
	if req.Name != nil {
		updated.Name = strings.TrimSpace(*req.Name)
	}
	if req.Prompt != nil {
		updated.Prompt = strings.TrimSpace(*req.Prompt)
	}
	if req.ContextProfile != nil {
		updated.ContextProfile = normalizeContextProfile(*req.ContextProfile)
	}
	if req.Kind != nil {
		updated.Kind = *req.Kind
		scheduleChanged = true
	}
	if req.RunAt != nil {
		updated.RunAt = req.RunAt.UTC()
		scheduleChanged = true
	}
	if req.Cron != nil {
		updated.Cron = strings.TrimSpace(*req.Cron)
		scheduleChanged = true
	}
	if req.Notify != nil {
		updated.Notify = *req.Notify
	}
	if req.Model != nil {
		updated.Model = normalizeModelTarget(*req.Model)
		if updated.Model == (ModelTarget{}) {
			updated.Model = normalizeModelTarget(m.defaultModel())
		}
		updated.ModelUnavailableNotified = false
	}
	if req.MaxTurns != nil {
		updated.MaxTurns = *req.MaxTurns
	}
	if req.AllowedTools != nil {
		updated.AllowedTools = normalizeToolNames(*req.AllowedTools)
	}
	if req.RunAt != nil && updated.Kind != KindOneShot {
		return Job{}, fmt.Errorf("run_at can only be set on oneshot jobs")
	}
	if req.Cron != nil && updated.Kind != KindRecurring {
		return Job{}, fmt.Errorf("cron can only be set on recurring jobs")
	}

	now := m.nowUTC()
	if scheduleChanged {
		switch updated.Kind {
		case KindOneShot:
			updated.Cron = ""
		case KindRecurring:
			updated.RunAt = time.Time{}
		}
		if err := m.prepareSchedule(&updated, now, true); err != nil {
			return Job{}, err
		}
		updated.Done = false
		updated.LastStatus = StatusPending
		updated.LastError = ""
	}
	if err := m.validateMutableJob(updated); err != nil {
		return Job{}, err
	}
	if req.Model != nil {
		if err := m.validateAvailableModel(updated.Model); err != nil {
			return Job{}, err
		}
	}
	if req.AllowedTools != nil {
		if err := m.validateAvailableTools(updated.AllowedTools); err != nil {
			return Job{}, err
		}
	}
	updated.UpdatedAt = now
	if err := m.store.StoreJob(ctx, updated); err != nil {
		return Job{}, fmt.Errorf("store scheduled job %q: %w", id, err)
	}
	m.jobs[id] = updated
	m.notifyWake()
	log.Printf(
		"q15: scheduler event=job_updated job_id=%q name=%q kind=%q next_fire=%q provider=%q model=%q context_profile=%q allowed_tools=%q notify=%t",
		updated.ID,
		updated.Name,
		updated.Kind,
		updated.NextFire.Format(time.RFC3339Nano),
		updated.Model.Provider,
		updated.Model.Ref,
		updated.ContextProfile,
		updated.AllowedTools,
		updated.Notify,
	)
	return cloneJob(updated), nil
}

// Delete idempotently removes one owned job without deleting its run history.
func (m *Manager) Delete(ctx context.Context, owner Owner, id string) error {
	if m == nil {
		return fmt.Errorf("schedule manager is not configured")
	}
	if err := m.authorize(owner); err != nil {
		return err
	}
	id = strings.TrimSpace(id)
	owner = normalizeOwner(owner)

	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[id]
	if !ok {
		return nil
	}
	if !sameOwner(job.Owner, owner) {
		return ErrNotFound
	}
	if _, running := m.running[id]; running {
		return fmt.Errorf("scheduled job %q is currently running", id)
	}
	if err := m.store.DeleteJob(ctx, id); err != nil {
		return fmt.Errorf("delete scheduled job %q: %w", id, err)
	}
	delete(m.jobs, id)
	m.notifyWake()
	log.Printf(
		"q15: scheduler event=job_deleted job_id=%q name=%q kind=%q",
		job.ID,
		job.Name,
		job.Kind,
	)
	return nil
}

// Run blocks, executing due jobs serially until ctx is canceled.
func (m *Manager) Run(ctx context.Context) error {
	if m == nil {
		return fmt.Errorf("schedule manager is not configured")
	}

	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		if jobID, ok := m.nextPendingNotification(); ok {
			if err := m.finalizePendingRun(ctx, jobID); err != nil {
				return err
			}
			continue
		}
		job, scheduledFor, ok, err := m.claimNextDue(ctx)
		if err != nil {
			return err
		}
		if ok {
			if err := m.executeClaimed(ctx, job, scheduledFor); err != nil {
				return err
			}
			continue
		}

		next := m.nextFire()
		resetTimer(timer, next, m.nowUTC())
		select {
		case <-ctx.Done():
			return nil
		case <-m.wake:
		case <-timer.C:
		}
	}
}

func (m *Manager) claimNextDue(
	ctx context.Context,
) (Job, time.Time, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.nowUTC()
	var selected Job
	for _, job := range m.jobs {
		if job.Done || job.NextFire.IsZero() || job.NextFire.After(now) {
			continue
		}
		if _, running := m.running[job.ID]; running {
			continue
		}
		if selected.ID == "" ||
			job.NextFire.Before(selected.NextFire) ||
			(job.NextFire.Equal(selected.NextFire) && job.ID < selected.ID) {
			selected = job
		}
	}
	if selected.ID == "" {
		return Job{}, time.Time{}, false, nil
	}

	scheduledFor := selected.NextFire.UTC()
	if selected.Kind == KindRecurring {
		expr, err := cronexpr.Parse(selected.Cron)
		if err != nil {
			return Job{}, time.Time{}, false, fmt.Errorf(
				"parse cron for scheduled job %q: %w",
				selected.ID,
				err,
			)
		}
		if latest, ok := expr.Prev(now.Add(time.Nanosecond)); ok &&
			!latest.Before(scheduledFor) {
			scheduledFor = latest
		}
		next, ok := expr.Next(now)
		if !ok {
			return Job{}, time.Time{}, false, fmt.Errorf(
				"scheduled job %q cron has no future occurrence",
				selected.ID,
			)
		}
		selected.NextFire = next
	} else {
		selected.NextFire = time.Time{}
		selected.Done = true
	}
	selected.LastRunAt = now
	selected.LastScheduledFor = scheduledFor
	selected.LastStatus = StatusRunning
	selected.LastError = ""
	selected.UpdatedAt = now

	if err := m.store.StoreJob(ctx, selected); err != nil {
		return Job{}, time.Time{}, false, fmt.Errorf(
			"claim scheduled job %q: %w",
			selected.ID,
			err,
		)
	}
	m.jobs[selected.ID] = selected
	m.running[selected.ID] = struct{}{}
	log.Printf(
		"q15: scheduler event=run_claimed job_id=%q run_id=%q name=%q scheduled_for=%q started_at=%q start_lag_ms=%d",
		selected.ID,
		runID(selected.ID, selected.LastRunAt),
		selected.Name,
		scheduledFor.Format(time.RFC3339Nano),
		selected.LastRunAt.Format(time.RFC3339Nano),
		selected.LastRunAt.Sub(scheduledFor).Milliseconds(),
	)
	return cloneJob(selected), scheduledFor, true, nil
}

func (m *Manager) executeClaimed(
	ctx context.Context,
	job Job,
	scheduledFor time.Time,
) error {
	currentTime := m.nowUTC()
	request := RunRequest{
		Job:            cloneJob(job),
		RunID:          runID(job.ID, job.LastRunAt),
		ScheduledFor:   scheduledFor.UTC(),
		StartedAt:      job.LastRunAt.UTC(),
		CurrentTimeUTC: currentTime,
	}
	log.Printf(
		"q15: scheduler event=run_started job_id=%q run_id=%q name=%q scheduled_for=%q started_at=%q current_time_utc=%q provider=%q model=%q context_profile=%q allowed_tools=%q",
		job.ID,
		request.RunID,
		job.Name,
		request.ScheduledFor.Format(time.RFC3339Nano),
		request.StartedAt.Format(time.RFC3339Nano),
		request.CurrentTimeUTC.Format(time.RFC3339Nano),
		job.Model.Provider,
		job.Model.Ref,
		job.ContextProfile,
		job.AllowedTools,
	)
	runCtx, cancel := context.WithTimeout(ctx, m.runTimeout)
	result, runErr := m.executor.Execute(runCtx, request)
	finishedAt := m.nowUTC()
	cancel()

	executedModel := normalizeModelTarget(result.Model)
	if executedModel == (ModelTarget{}) {
		executedModel = job.Model
	}
	if runErr == nil && executedModel != job.Model {
		runErr = fmt.Errorf(
			"scheduled executor used model %q instead of exact target %q",
			executedModel,
			job.Model,
		)
	}

	status := StatusSucceeded
	modelUnavailable := false
	if runErr != nil {
		status = StatusFailed
		if ctx.Err() != nil {
			status = StatusInterrupted
		} else if errors.Is(runErr, ErrModelUnavailable) {
			status = StatusModelUnavailable
			modelUnavailable = true
		} else if errors.Is(runErr, context.DeadlineExceeded) {
			runErr = fmt.Errorf("scheduled run timed out after %s: %w", m.runTimeout, runErr)
		}
	}
	record := RunRecord{
		SchemaVersion: SchemaVersion,
		ID:            request.RunID,
		JobID:         job.ID,
		JobName:       job.Name,
		Owner:         job.Owner,
		Status:        status,
		Model:         executedModel,
		AllowedTools:  cloneToolNames(job.AllowedTools),
		ScheduledFor:  scheduledFor.UTC(),
		StartedAt:     job.LastRunAt.UTC(),
		FinishedAt:    finishedAt,
		Output:        truncateRunes(strings.TrimSpace(result.Text), maxOutputRunes),
	}
	if runErr != nil {
		record.Error = runErr.Error()
	}
	if runErr != nil {
		log.Printf(
			"q15: scheduler event=run_failed job_id=%q run_id=%q name=%q status=%q finished_at=%q duration_ms=%d provider=%q model=%q error=%q",
			job.ID,
			record.ID,
			job.Name,
			status,
			finishedAt.Format(time.RFC3339Nano),
			finishedAt.Sub(job.LastRunAt).Milliseconds(),
			executedModel.Provider,
			executedModel.Ref,
			runErr,
		)
	} else {
		log.Printf(
			"q15: scheduler event=run_completed job_id=%q run_id=%q name=%q status=%q finished_at=%q duration_ms=%d provider=%q model=%q",
			job.ID,
			record.ID,
			job.Name,
			status,
			finishedAt.Format(time.RFC3339Nano),
			finishedAt.Sub(job.LastRunAt).Milliseconds(),
			executedModel.Provider,
			executedModel.Ref,
		)
	}

	modelUnavailableNotified := job.ModelUnavailableNotified
	if !modelUnavailable {
		modelUnavailableNotified = false
	}
	shouldNotify := job.Notify && ctx.Err() == nil &&
		(!modelUnavailable || !job.ModelUnavailableNotified)
	var pendingNotification *Notification
	if shouldNotify {
		pendingNotification = &Notification{
			Channel: job.Owner.Channel,
			ChatID:  job.Owner.ChatID,
		}
	}

	persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer persistCancel()

	m.mu.Lock()
	current, stillExists := m.jobs[job.ID]
	if stillExists {
		current.LastRunAt = job.LastRunAt
		current.LastScheduledFor = scheduledFor.UTC()
		current.UpdatedAt = finishedAt
		current.LastStatus = status
		current.ModelUnavailableNotified = modelUnavailableNotified
		if modelUnavailable && current.Kind == KindOneShot {
			current.Done = false
			current.NextFire = finishedAt.Add(m.unavailableRetryDelay)
		}
		if current.Kind == KindRecurring &&
			(current.NextFire.IsZero() || !current.NextFire.After(finishedAt)) {
			expr, err := cronexpr.Parse(current.Cron)
			if err != nil {
				m.mu.Unlock()
				return fmt.Errorf("parse cron for scheduled job %q: %w", job.ID, err)
			}
			next, ok := expr.Next(finishedAt)
			if !ok {
				m.mu.Unlock()
				return fmt.Errorf("scheduled job %q cron has no future occurrence", job.ID)
			}
			current.NextFire = next
		}
		if runErr != nil {
			current.LastFailureAt = finishedAt
			current.ConsecutiveFailures++
			current.LastError = runErr.Error()
		} else {
			current.LastSuccessAt = finishedAt
			current.ConsecutiveFailures = 0
			current.LastError = ""
		}
		if pendingNotification != nil {
			pendingNotification.Text = notificationText(current, record)
		}
		current.PendingRun = runRecordPtr(record)
		current.PendingNotification = pendingNotification
		if err := m.store.StoreJob(persistCtx, current); err != nil {
			m.mu.Unlock()
			return fmt.Errorf("finish scheduled job %q: %w", job.ID, err)
		}
		m.jobs[job.ID] = current
	}
	m.mu.Unlock()

	if stillExists {
		if err := m.finalizePendingRun(persistCtx, job.ID); err != nil {
			return fmt.Errorf("archive scheduled job %q run: %w", job.ID, err)
		}
		return nil
	}

	m.mu.Lock()
	delete(m.running, job.ID)
	m.mu.Unlock()
	if err := m.store.AppendRunRecord(persistCtx, record); err != nil {
		return fmt.Errorf("record removed scheduled job %q: %w", job.ID, err)
	}
	return nil
}

func (m *Manager) nextPendingNotification() (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	selected := ""
	for id, job := range m.jobs {
		if job.PendingRun != nil && job.PendingNotification != nil &&
			(selected == "" || id < selected) {
			selected = id
		}
	}
	return selected, selected != ""
}

func (m *Manager) finalizePendingRun(ctx context.Context, jobID string) error {
	m.mu.Lock()
	job, ok := m.jobs[jobID]
	m.mu.Unlock()
	if !ok || job.PendingRun == nil {
		m.mu.Lock()
		delete(m.running, jobID)
		m.mu.Unlock()
		return nil
	}

	var deliveryErr error
	if job.PendingNotification != nil {
		if job.PendingNotification.DeliveredAt.IsZero() {
			notifyCtx, notifyCancel := context.WithTimeout(ctx, 10*time.Second)
			deliveryErr = m.publisher.PublishOutboundAndWaitForDelivery(
				notifyCtx,
				bus.OutboundMessage{
					Channel: job.PendingNotification.Channel,
					ChatID:  job.PendingNotification.ChatID,
					Text:    job.PendingNotification.Text,
				},
			)
			notifyCancel()
			if deliveryErr != nil {
				log.Printf(
					"q15: scheduler event=notification_delivery_failed job_id=%q run_id=%q name=%q channel=%q error=%q",
					job.ID,
					job.PendingRun.ID,
					job.Name,
					job.PendingNotification.Channel,
					deliveryErr,
				)
			}
			if deliveryErr != nil && ctx.Err() != nil {
				return nil
			}
			if deliveryErr == nil {
				deliveredAt := m.nowUTC()
				var err error
				job, err = m.markNotificationDelivered(ctx, jobID, deliveredAt)
				if err != nil {
					return err
				}
				log.Printf(
					"q15: scheduler event=notification_delivered job_id=%q run_id=%q name=%q channel=%q delivered_at=%q",
					job.ID,
					job.PendingRun.ID,
					job.Name,
					job.PendingNotification.Channel,
					deliveredAt.Format(time.RFC3339Nano),
				)
			}
		}
		if deliveryErr == nil {
			notification := job.PendingNotification
			if notification == nil || notification.DeliveredAt.IsZero() {
				return fmt.Errorf("scheduled notification delivery timestamp is missing")
			}
			err := m.recorder.RecordDeliveredNotification(ctx, DeliveredNotification{
				JobID:       job.ID,
				RunID:       job.PendingRun.ID,
				Channel:     notification.Channel,
				ChatID:      notification.ChatID,
				Text:        notification.Text,
				DeliveredAt: notification.DeliveredAt,
			})
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				log.Printf(
					"q15: scheduler event=transcript_record_failed job_id=%q run_id=%q name=%q error=%q",
					job.ID,
					job.PendingRun.ID,
					job.Name,
					err,
				)
				return fmt.Errorf("record delivered scheduled notification: %w", err)
			}
			log.Printf(
				"q15: scheduler event=transcript_recorded job_id=%q run_id=%q name=%q delivered_at=%q",
				job.ID,
				job.PendingRun.ID,
				job.Name,
				notification.DeliveredAt.Format(time.RFC3339Nano),
			)
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.jobs[jobID]
	if !ok || current.PendingRun == nil {
		delete(m.running, jobID)
		return nil
	}
	current = cloneJob(current)
	archivedRun := *current.PendingRun
	if current.PendingNotification != nil {
		if deliveryErr != nil {
			current.PendingRun.NotifyError = deliveryErr.Error()
		} else if current.PendingRun.Status == StatusModelUnavailable {
			current.ModelUnavailableNotified = true
		}
		current.PendingNotification = nil
		if err := m.store.StoreJob(ctx, current); err != nil {
			return fmt.Errorf("record scheduled notification result: %w", err)
		}
	}
	if err := m.finishPendingRun(ctx, &current); err != nil {
		return err
	}
	delete(m.running, jobID)
	if current.Done {
		delete(m.jobs, jobID)
	} else {
		m.jobs[jobID] = current
	}
	log.Printf(
		"q15: scheduler event=run_archived job_id=%q run_id=%q name=%q status=%q",
		current.ID,
		archivedRun.ID,
		current.Name,
		archivedRun.Status,
	)
	return nil
}

func (m *Manager) markNotificationDelivered(
	ctx context.Context,
	jobID string,
	deliveredAt time.Time,
) (Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	current, ok := m.jobs[jobID]
	if !ok || current.PendingRun == nil || current.PendingNotification == nil {
		return Job{}, fmt.Errorf(
			"scheduled notification intent disappeared before recording delivery",
		)
	}
	current = cloneJob(current)
	if current.PendingNotification.DeliveredAt.IsZero() {
		current.PendingNotification.DeliveredAt = deliveredAt.UTC()
		if err := m.store.StoreJob(ctx, current); err != nil {
			return Job{}, fmt.Errorf("persist scheduled notification delivery: %w", err)
		}
		m.jobs[jobID] = current
	}
	return cloneJob(current), nil
}

func (m *Manager) finishPendingRun(ctx context.Context, job *Job) error {
	if job == nil || job.PendingRun == nil {
		return nil
	}
	if job.PendingNotification != nil {
		return fmt.Errorf("scheduled notification is still pending")
	}
	if err := m.store.AppendRunRecord(ctx, *job.PendingRun); err != nil {
		return err
	}
	if job.Done {
		return m.store.DeleteJob(ctx, job.ID)
	}
	job.PendingRun = nil
	return m.store.StoreJob(ctx, *job)
}

func (m *Manager) markInterrupted(job *Job, now time.Time) {
	job.LastStatus = StatusInterrupted
	job.LastError = "runtime stopped before the scheduled run completed"
	job.LastFailureAt = now
	job.ConsecutiveFailures++
	job.ModelUnavailableNotified = false
	job.UpdatedAt = now
}

func (m *Manager) nextFire() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	var next time.Time
	for _, job := range m.jobs {
		if job.Done || job.NextFire.IsZero() {
			continue
		}
		if next.IsZero() || job.NextFire.Before(next) {
			next = job.NextFire
		}
	}
	return next
}

func (m *Manager) prepareSchedule(job *Job, now time.Time, requireFuture bool) error {
	if job == nil {
		return fmt.Errorf("scheduled job is required")
	}
	switch job.Kind {
	case KindOneShot:
		job.Cron = ""
		job.RunAt = job.RunAt.UTC()
		if job.RunAt.IsZero() {
			return fmt.Errorf("run_at is required for oneshot jobs")
		}
		if requireFuture && !job.RunAt.After(now) {
			return fmt.Errorf("run_at must be in the future")
		}
		job.NextFire = job.RunAt
	case KindRecurring:
		job.RunAt = time.Time{}
		job.Cron = strings.TrimSpace(job.Cron)
		expr, err := cronexpr.Parse(job.Cron)
		if err != nil {
			return fmt.Errorf("invalid cron: %w", err)
		}
		next, ok := expr.Next(now)
		if !ok {
			return fmt.Errorf("cron has no future occurrence")
		}
		job.NextFire = next
	default:
		return fmt.Errorf("kind must be %q or %q", KindOneShot, KindRecurring)
	}
	return nil
}

func (m *Manager) validateMutableJob(job Job) error {
	if job.Name == "" {
		return fmt.Errorf("name is required")
	}
	if utf8.RuneCountInString(job.Name) > maxNameRunes {
		return fmt.Errorf("name must be at most %d characters", maxNameRunes)
	}
	if job.Prompt == "" {
		return fmt.Errorf("prompt is required")
	}
	if utf8.RuneCountInString(job.Prompt) > maxPromptRunes {
		return fmt.Errorf("prompt must be at most %d characters", maxPromptRunes)
	}
	if err := validateContextProfile(job.ContextProfile); err != nil {
		return err
	}
	if job.MaxTurns <= 0 || job.MaxTurns > m.maxTurns {
		return fmt.Errorf("max_turns must be between 1 and %d", m.maxTurns)
	}
	if err := validateModelTarget(job.Model); err != nil {
		return err
	}
	if err := validateCanonicalToolNames(job.AllowedTools); err != nil {
		return err
	}
	return nil
}

func (m *Manager) validateAvailableModel(model ModelTarget) error {
	if !m.modelExists(model) {
		return fmt.Errorf("unknown scheduled job model %q", model)
	}
	return nil
}

func (m *Manager) validateAvailableTools(names []string) error {
	for _, name := range names {
		if !m.toolExists(name) {
			return fmt.Errorf("unknown scheduled job tool %q", name)
		}
	}
	return nil
}

func (m *Manager) validateStoredJob(job Job) error {
	if job.SchemaVersion != SchemaVersion {
		return fmt.Errorf(
			"unsupported schema version %d (want %d)",
			job.SchemaVersion,
			SchemaVersion,
		)
	}
	if strings.TrimSpace(job.ID) == "" {
		return fmt.Errorf("id is required")
	}
	if err := validateOwner(job.Owner); err != nil {
		return err
	}
	if job.Name == "" {
		return fmt.Errorf("name is required")
	}
	if job.Prompt == "" {
		return fmt.Errorf("prompt is required")
	}
	if err := validateContextProfile(job.ContextProfile); err != nil {
		return err
	}
	if job.MaxTurns <= 0 || job.MaxTurns > m.maxTurns {
		return fmt.Errorf("max_turns must be between 1 and %d", m.maxTurns)
	}
	if err := validateModelTarget(job.Model); err != nil {
		return err
	}
	if err := validateCanonicalToolNames(job.AllowedTools); err != nil {
		return err
	}
	if job.PendingRun != nil {
		if job.PendingRun.SchemaVersion != SchemaVersion {
			return fmt.Errorf(
				"pending run has unsupported schema version %d",
				job.PendingRun.SchemaVersion,
			)
		}
		if job.PendingRun.JobID != job.ID {
			return fmt.Errorf("pending run job id %q does not match job id", job.PendingRun.JobID)
		}
		if strings.TrimSpace(job.PendingRun.ID) == "" {
			return fmt.Errorf("pending run id is required")
		}
		if job.PendingRun.StartedAt.IsZero() {
			return fmt.Errorf("pending run started_at is required")
		}
		if err := validateCanonicalToolNames(job.PendingRun.AllowedTools); err != nil {
			return fmt.Errorf("pending run: %w", err)
		}
		if want := runID(job.ID, job.PendingRun.StartedAt); job.PendingRun.ID != want {
			return fmt.Errorf(
				"pending run id %q does not match job/start identity %q",
				job.PendingRun.ID,
				want,
			)
		}
	}
	if job.PendingNotification != nil {
		if job.PendingRun == nil {
			return fmt.Errorf("pending notification requires a pending run")
		}
		if strings.TrimSpace(job.PendingNotification.Channel) == "" {
			return fmt.Errorf("pending notification channel is required")
		}
		if strings.TrimSpace(job.PendingNotification.ChatID) == "" {
			return fmt.Errorf("pending notification chat id is required")
		}
		if strings.TrimSpace(job.PendingNotification.Text) == "" {
			return fmt.Errorf("pending notification text is required")
		}
	}
	if job.Kind == KindOneShot {
		if job.RunAt.IsZero() {
			return fmt.Errorf("run_at is required for oneshot jobs")
		}
		if !job.Done && job.NextFire.IsZero() {
			return fmt.Errorf("next_fire is required for an active oneshot job")
		}
	} else if job.Kind == KindRecurring {
		if _, err := cronexpr.Parse(job.Cron); err != nil {
			return fmt.Errorf("invalid cron: %w", err)
		}
		if job.NextFire.IsZero() {
			return fmt.Errorf("next_fire is required for a recurring job")
		}
	} else {
		return fmt.Errorf("invalid kind %q", job.Kind)
	}
	return nil
}

func (m *Manager) authorize(owner Owner) error {
	owner = normalizeOwner(owner)
	if err := validateOwner(owner); err != nil {
		return err
	}
	if len(m.allowedUserIDs) > 0 {
		if _, ok := m.allowedUserIDs[owner.UserID]; !ok {
			return fmt.Errorf("user %q is not allowed to manage scheduled jobs", owner.UserID)
		}
	}
	return nil
}

func (m *Manager) nowUTC() time.Time {
	return m.now().UTC()
}

func (m *Manager) notifyWake() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func normalizeOwner(owner Owner) Owner {
	return Owner{
		Channel: strings.TrimSpace(owner.Channel),
		ChatID:  strings.TrimSpace(owner.ChatID),
		UserID:  strings.TrimSpace(owner.UserID),
	}
}

func normalizeModelTarget(model ModelTarget) ModelTarget {
	return ModelTarget{
		Provider: strings.TrimSpace(model.Provider),
		Ref:      strings.TrimSpace(model.Ref),
	}
}

func normalizeContextProfile(profile ContextProfile) ContextProfile {
	return ContextProfile(strings.TrimSpace(string(profile)))
}

func normalizeToolNames(names []string) []string {
	if len(names) == 0 {
		return []string{}
	}
	normalized := make([]string, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		normalized = append(normalized, name)
	}
	return normalized
}

func normalizeStoredToolLists(job *Job) bool {
	if job == nil {
		return false
	}
	normalized := false
	if job.AllowedTools == nil {
		job.AllowedTools = []string{}
		normalized = true
	}
	if job.PendingRun != nil && job.PendingRun.AllowedTools == nil {
		job.PendingRun.AllowedTools = []string{}
		normalized = true
	}
	return normalized
}

func validateCanonicalToolNames(names []string) error {
	normalized := normalizeToolNames(names)
	if len(normalized) != len(names) {
		return fmt.Errorf("allowed_tools must contain unique, non-empty tool names")
	}
	for index := range names {
		if names[index] != normalized[index] {
			return fmt.Errorf("allowed_tools entries must not have surrounding whitespace")
		}
	}
	return nil
}

func validateContextProfile(profile ContextProfile) error {
	switch normalizeContextProfile(profile) {
	case ContextProfileMinimal, ContextProfileAgent:
		return nil
	default:
		return fmt.Errorf(
			"context_profile must be %q or %q",
			ContextProfileMinimal,
			ContextProfileAgent,
		)
	}
}

func validateModelTarget(model ModelTarget) error {
	model = normalizeModelTarget(model)
	if model.Provider == "" {
		return fmt.Errorf("model provider is required")
	}
	if model.Ref == "" {
		return fmt.Errorf("model ref is required")
	}
	return nil
}

func validateOwner(owner Owner) error {
	owner = normalizeOwner(owner)
	if owner.Channel == "" {
		return fmt.Errorf("owner channel is required")
	}
	if owner.ChatID == "" {
		return fmt.Errorf("owner chat id is required")
	}
	if owner.UserID == "" {
		return fmt.Errorf("owner user id is required")
	}
	return nil
}

func sameOwner(left, right Owner) bool {
	left = normalizeOwner(left)
	right = normalizeOwner(right)
	return left == right
}

func isArchivedRunStatus(status Status) bool {
	switch status {
	case StatusSucceeded, StatusFailed, StatusInterrupted, StatusModelUnavailable:
		return true
	default:
		return false
	}
}

func cloneJob(job Job) Job {
	job.AllowedTools = cloneToolNames(job.AllowedTools)
	if job.PendingRun != nil {
		job.PendingRun = runRecordPtr(*job.PendingRun)
	}
	if job.PendingNotification != nil {
		notification := *job.PendingNotification
		job.PendingNotification = &notification
	}
	return job
}

func cloneRunHistory(history RunHistory) RunHistory {
	cloned := RunHistory{
		Matched:      history.Matched,
		StatusCounts: make(map[Status]int, len(history.StatusCounts)),
		Runs:         make([]RunRecord, len(history.Runs)),
	}
	for index, record := range history.Runs {
		cloned.Runs[index] = cloneRunRecord(record)
	}
	for status, count := range history.StatusCounts {
		cloned.StatusCounts[status] = count
	}
	return cloned
}

func runRecordPtr(record RunRecord) *RunRecord {
	record = cloneRunRecord(record)
	return &record
}

func cloneRunRecord(record RunRecord) RunRecord {
	record.AllowedTools = cloneToolNames(record.AllowedTools)
	return record
}

func cloneToolNames(names []string) []string {
	if names == nil {
		return nil
	}
	return append([]string{}, names...)
}

func interruptedRecord(job Job, finishedAt time.Time) RunRecord {
	startedAt := job.LastRunAt.UTC()
	if startedAt.IsZero() {
		startedAt = finishedAt
	}
	return RunRecord{
		SchemaVersion: SchemaVersion,
		ID:            runID(job.ID, startedAt),
		JobID:         job.ID,
		JobName:       job.Name,
		Owner:         job.Owner,
		Status:        StatusInterrupted,
		Model:         job.Model,
		AllowedTools:  cloneToolNames(job.AllowedTools),
		ScheduledFor:  job.LastScheduledFor.UTC(),
		StartedAt:     startedAt,
		FinishedAt:    finishedAt,
		Error:         "runtime stopped before the scheduled run completed",
	}
}

func runID(jobID string, startedAt time.Time) string {
	identity := strings.TrimSpace(jobID) + "\x00" +
		startedAt.UTC().Format(time.RFC3339Nano)
	return "run-" + uuid.NewSHA1(uuid.NameSpaceOID, []byte(identity)).String()
}

func notificationText(job Job, record RunRecord) string {
	name := notificationDisplayName(job.Name)
	header := "⏰ " + name
	body := strings.TrimSpace(record.Output)
	if record.Error != "" {
		header = "⚠️ " + name + " failed"
		body = strings.TrimSpace(record.Error)
	} else if body == "" {
		body = "Completed without text output."
	}

	footer := "Scheduled for " + formatNotificationTime(record.ScheduledFor)
	if job.Kind == KindRecurring && !job.NextFire.IsZero() {
		footer += "\nNext run " + formatNotificationTime(job.NextFire)
	}
	return notificationWithFooter(header, body, footer)
}

func notificationDisplayName(name string) string {
	fields := strings.Fields(name)
	if len(fields) == 0 {
		return "Scheduled job"
	}
	if len(fields) > 1 {
		return strings.Join(fields, " ")
	}
	parts := strings.FieldsFunc(fields[0], func(r rune) bool {
		return r == '-' || r == '_'
	})
	if len(parts) < 2 {
		return fields[0]
	}
	display := strings.Join(parts, " ")
	runes := []rune(display)
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}

func formatNotificationTime(value time.Time) string {
	return value.UTC().Format("02 Jan 2006, 15:04 UTC")
}

func notificationWithFooter(header, body, footer string) string {
	const separators = "\n\n\n\n"
	bodyLimit := maxNotifyRunes -
		utf8.RuneCountInString(header) -
		utf8.RuneCountInString(footer) -
		utf8.RuneCountInString(separators)
	if bodyLimit <= 0 {
		return truncateRunes(header+"\n\n"+footer, maxNotifyRunes)
	}
	return header + "\n\n" + truncateRunes(body, bodyLimit) + "\n\n" + footer
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 || utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	if limit == 1 {
		return "…"
	}
	return string(runes[:limit-1]) + "…"
}

func resetTimer(timer *time.Timer, next, now time.Time) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	wait := time.Hour
	if !next.IsZero() {
		wait = next.Sub(now)
		if wait < 0 {
			wait = 0
		}
	}
	timer.Reset(wait)
}
