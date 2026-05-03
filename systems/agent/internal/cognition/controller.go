package cognition

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
)

// RunCause values identify the trigger class that launched a cognition job.
const (
	RunCauseStartup  = "startup"
	RunCauseSchedule = "schedule"
	RunCauseState    = "state"
)

// StateEvaluator decides whether a registered startup or state trigger should
// launch its job for the provided snapshot and persisted job state.
type StateEvaluator func(context.Context, Snapshot, JobState) (bool, string, error)

// JobRegistration binds one typed cognition job to its trigger policy.
type JobRegistration struct {
	NewJob func() JobDefinition
	Policy TriggerPolicy
}

// TriggerPolicy describes all supported trigger rules for one job.
type TriggerPolicy struct {
	Startup  []StartupRule
	Schedule []ScheduleRule
	State    []StateRule
}

// StartupRule evaluates once during controller startup.
type StartupRule struct {
	ID       string
	Evaluate StateEvaluator
}

// ScheduleRule launches on a parsed UTC cron schedule.
type ScheduleRule struct {
	ID   string
	Spec string
}

// StateRule launches opportunistically from transcript or state changes.
type StateRule struct {
	ID       string
	Evaluate StateEvaluator
}

// Snapshot is the trigger-evaluation view of current runtime state.
type Snapshot struct {
	NowUTC        time.Time
	HeadLastSeq   int64
	HeadUpdatedAt time.Time
	Loader        ContextLoader
}

// RunCause records why one cognition job ran.
type RunCause struct {
	Kind         string    `json:"kind"`
	RuleID       string    `json:"rule_id"`
	FiredAt      time.Time `json:"fired_at"`
	ScheduledFor time.Time `json:"scheduled_for,omitempty"`
	Reason       string    `json:"reason,omitempty"`
}

// ConsolidationCheckpoint records the last episodic turn boundary that has
// been consolidated into working memory.
type ConsolidationCheckpoint struct {
	LastConsolidatedTurnID string    `json:"last_consolidated_turn_id,omitempty"`
	LastConsolidatedSeq    int64     `json:"last_consolidated_seq,omitempty"`
	LastConsolidatedAt     time.Time `json:"last_consolidated_at,omitempty"`
	UpdatedAt              time.Time `json:"updated_at,omitempty"`
}

// SemanticExtractionCheckpoint records the last episodic turn boundary that has
// been successfully processed by semantic-memory extraction.
type SemanticExtractionCheckpoint struct {
	LastExtractedTurnID string    `json:"last_extracted_turn_id,omitempty"`
	LastExtractedSeq    int64     `json:"last_extracted_seq,omitempty"`
	LastExtractedAt     time.Time `json:"last_extracted_at,omitempty"`
	UpdatedAt           time.Time `json:"updated_at,omitempty"`
}

// JobState tracks persisted trigger/controller state for one job type.
type JobState struct {
	LastRunAt           time.Time            `json:"last_run_at,omitempty"`
	LastSuccessAt       time.Time            `json:"last_success_at,omitempty"`
	LastFailureAt       time.Time            `json:"last_failure_at,omitempty"`
	LastObservedSeq     int64                `json:"last_observed_seq,omitempty"`
	LastRunInputSeq     int64                `json:"last_run_input_seq,omitempty"`
	LastSuccessInputSeq int64                `json:"last_success_input_seq,omitempty"`
	DirtySinceSeq       int64                `json:"dirty_since_seq,omitempty"`
	DirtySinceAt        time.Time            `json:"dirty_since_at,omitempty"`
	ConsecutiveFailures int                  `json:"consecutive_failures,omitempty"`
	LastRunCause        RunCause             `json:"last_run_cause,omitempty"`
	LastScheduledFor    map[string]time.Time `json:"last_scheduled_for,omitempty"`
}

// AttemptFailure records one failed model attempt within a cognition run.
type AttemptFailure struct {
	ModelRef string `json:"model_ref,omitempty"`
	Error    string `json:"error,omitempty"`
}

// RunRecord is the append-only persisted provenance record for one cognition
// run attempt.
type RunRecord struct {
	Type            string            `json:"type"`
	Cause           RunCause          `json:"cause"`
	StartedAt       time.Time         `json:"started_at"`
	FinishedAt      time.Time         `json:"finished_at"`
	InputSeq        int64             `json:"input_seq,omitempty"`
	InputUpdatedAt  time.Time         `json:"input_updated_at,omitempty"`
	OutputSeq       int64             `json:"output_seq,omitempty"`
	OutputUpdatedAt time.Time         `json:"output_updated_at,omitempty"`
	Succeeded       bool              `json:"succeeded"`
	Summary         string            `json:"summary,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
	ModelRef        string            `json:"model_ref,omitempty"`
	AttemptFailures []AttemptFailure  `json:"attempt_failures,omitempty"`
	Error           string            `json:"error,omitempty"`
}

// ControllerStore persists controller state and run provenance.
type ControllerStore interface {
	LoadHead(context.Context) (int64, time.Time, error)
	LoadJobState(context.Context, string) (JobState, error)
	StoreJobState(context.Context, string, JobState) error
	StoreConsolidationCheckpoint(
		context.Context,
		ConsolidationCheckpoint,
	) (ConsolidationCheckpoint, error)
	StoreSemanticExtractionCheckpoint(
		context.Context,
		SemanticExtractionCheckpoint,
	) (SemanticExtractionCheckpoint, error)
	AppendRunRecord(context.Context, RunRecord) error
}

// Controller coordinates startup, scheduled, and state-driven cognition jobs.
type Controller struct {
	runner  *Runner
	store   ControllerStore
	loader  ContextLoader
	jobs    []registeredJob
	started time.Time

	stateChanges chan struct{}
}

type registeredJob struct {
	jobType  string
	newJob   func() JobDefinition
	startup  []StartupRule
	schedule []compiledScheduleRule
	state    []StateRule
}

type compiledScheduleRule struct {
	id   string
	spec string
	expr cronExpr
}

type pendingRun struct {
	job     registeredJob
	cause   RunCause
	state   JobState
	headSeq int64
	headAt  time.Time
}

// NewController constructs a serial cognition trigger controller.
func NewController(
	runner *Runner,
	store ControllerStore,
	loader ContextLoader,
	registrations ...JobRegistration,
) (*Controller, error) {
	if runner == nil {
		return nil, fmt.Errorf("cognition runner is required")
	}
	if store == nil {
		return nil, fmt.Errorf("controller store is required")
	}
	if loader == nil {
		return nil, fmt.Errorf("context loader is required")
	}

	jobs := make([]registeredJob, 0, len(registrations))
	seenJobs := make(map[string]struct{}, len(registrations))
	for i, registration := range registrations {
		if registration.NewJob == nil {
			return nil, fmt.Errorf("job registration %d new job is required", i)
		}
		job := registration.NewJob()
		if job == nil {
			return nil, fmt.Errorf("job registration %d returned nil job", i)
		}
		jobType := strings.TrimSpace(job.Type())
		if jobType == "" {
			return nil, fmt.Errorf("job registration %d type is required", i)
		}
		if _, ok := seenJobs[jobType]; ok {
			return nil, fmt.Errorf("duplicate cognition job type %q", jobType)
		}
		seenJobs[jobType] = struct{}{}

		compiled := registeredJob{
			jobType:  jobType,
			newJob:   registration.NewJob,
			startup:  make([]StartupRule, 0, len(registration.Policy.Startup)),
			schedule: make([]compiledScheduleRule, 0, len(registration.Policy.Schedule)),
			state:    make([]StateRule, 0, len(registration.Policy.State)),
		}
		seenRules := make(map[string]struct{})
		for _, rule := range registration.Policy.Startup {
			ruleID := strings.TrimSpace(rule.ID)
			if ruleID == "" {
				return nil, fmt.Errorf("job %q startup rule id is required", jobType)
			}
			if rule.Evaluate == nil {
				return nil, fmt.Errorf(
					"job %q startup rule %q evaluator is required",
					jobType,
					ruleID,
				)
			}
			if _, ok := seenRules[ruleID]; ok {
				return nil, fmt.Errorf("job %q duplicate rule id %q", jobType, ruleID)
			}
			seenRules[ruleID] = struct{}{}
			compiled.startup = append(compiled.startup, StartupRule{
				ID:       ruleID,
				Evaluate: rule.Evaluate,
			})
		}
		for _, rule := range registration.Policy.Schedule {
			ruleID := strings.TrimSpace(rule.ID)
			if ruleID == "" {
				return nil, fmt.Errorf("job %q schedule rule id is required", jobType)
			}
			if _, ok := seenRules[ruleID]; ok {
				return nil, fmt.Errorf("job %q duplicate rule id %q", jobType, ruleID)
			}
			seenRules[ruleID] = struct{}{}
			spec := strings.TrimSpace(rule.Spec)
			if spec == "" {
				return nil, fmt.Errorf("job %q schedule rule %q spec is required", jobType, ruleID)
			}
			expr, err := parseCronExpr(spec)
			if err != nil {
				return nil, fmt.Errorf("job %q schedule rule %q: %w", jobType, ruleID, err)
			}
			compiled.schedule = append(compiled.schedule, compiledScheduleRule{
				id:   ruleID,
				spec: spec,
				expr: expr,
			})
		}
		for _, rule := range registration.Policy.State {
			ruleID := strings.TrimSpace(rule.ID)
			if ruleID == "" {
				return nil, fmt.Errorf("job %q state rule id is required", jobType)
			}
			if rule.Evaluate == nil {
				return nil, fmt.Errorf(
					"job %q state rule %q evaluator is required",
					jobType,
					ruleID,
				)
			}
			if _, ok := seenRules[ruleID]; ok {
				return nil, fmt.Errorf("job %q duplicate rule id %q", jobType, ruleID)
			}
			seenRules[ruleID] = struct{}{}
			compiled.state = append(compiled.state, StateRule{
				ID:       ruleID,
				Evaluate: rule.Evaluate,
			})
		}
		jobs = append(jobs, compiled)
	}

	return &Controller{
		runner:       runner,
		store:        store,
		loader:       loader,
		jobs:         jobs,
		stateChanges: make(chan struct{}, 1),
	}, nil
}

// NotifyStateChange schedules a non-blocking state-trigger evaluation pass.
func (c *Controller) NotifyStateChange() {
	if c == nil {
		return
	}
	select {
	case c.stateChanges <- struct{}{}:
	default:
	}
}

// Run starts the controller loop and blocks until the context is canceled or a
// controller error occurs.
func (c *Controller) Run(ctx context.Context) error {
	if c == nil {
		return fmt.Errorf("controller is required")
	}

	c.started = time.Now().UTC()
	if err := c.drain(ctx, true, true, true); err != nil {
		return err
	}

	timer := time.NewTimer(time.Hour)
	defer timer.Stop()

	for {
		nextAt, err := c.nextScheduledAt(ctx, time.Now().UTC())
		if err != nil {
			return err
		}
		resetTimer(timer, nextAt)

		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
			if err := c.drain(ctx, false, true, true); err != nil {
				return err
			}
		case <-c.stateChanges:
			if err := c.drain(ctx, false, true, true); err != nil {
				return err
			}
		}
	}
}

func (c *Controller) drain(
	ctx context.Context,
	includeStartup bool,
	includeSchedule bool,
	includeState bool,
) error {
	for {
		pending, ok, err := c.nextPendingRun(ctx, includeStartup, includeSchedule, includeState)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		includeStartup = false
		if err := c.runPending(ctx, pending); err != nil {
			return err
		}
	}
}

func (c *Controller) nextPendingRun(
	ctx context.Context,
	includeStartup bool,
	includeSchedule bool,
	includeState bool,
) (pendingRun, bool, error) {
	now := time.Now().UTC()
	headSeq, headAt, err := c.store.LoadHead(ctx)
	if err != nil {
		return pendingRun{}, false, err
	}

	for _, job := range c.jobs {
		state, err := c.store.LoadJobState(ctx, job.jobType)
		if err != nil {
			return pendingRun{}, false, err
		}
		state, changed := syncDirtyState(state, headSeq, headAt)
		if changed {
			if err := c.store.StoreJobState(ctx, job.jobType, state); err != nil {
				return pendingRun{}, false, err
			}
		}

		snapshot := Snapshot{
			NowUTC:        now,
			HeadLastSeq:   headSeq,
			HeadUpdatedAt: headAt,
			Loader:        c.loader,
		}

		if includeStartup {
			for _, rule := range job.startup {
				shouldRun, reason, err := rule.Evaluate(ctx, snapshot, state)
				if err != nil {
					return pendingRun{}, false, fmt.Errorf(
						"evaluate startup rule %q for job %q: %w",
						rule.ID,
						job.jobType,
						err,
					)
				}
				if shouldRun {
					return pendingRun{
						job: job,
						cause: RunCause{
							Kind:    RunCauseStartup,
							RuleID:  rule.ID,
							FiredAt: now,
							Reason:  strings.TrimSpace(reason),
						},
						state:   state,
						headSeq: headSeq,
						headAt:  headAt,
					}, true, nil
				}
			}
		}

		if includeSchedule {
			for _, rule := range job.schedule {
				scheduledFor, ok := c.latestDue(rule, state, now)
				if ok {
					return pendingRun{
						job: job,
						cause: RunCause{
							Kind:         RunCauseSchedule,
							RuleID:       rule.id,
							FiredAt:      now,
							ScheduledFor: scheduledFor,
						},
						state:   state,
						headSeq: headSeq,
						headAt:  headAt,
					}, true, nil
				}
			}
		}

		if includeState {
			if !shouldEvaluateStateRules(state, headSeq) {
				continue
			}
			for _, rule := range job.state {
				shouldRun, reason, err := rule.Evaluate(ctx, snapshot, state)
				if err != nil {
					return pendingRun{}, false, fmt.Errorf(
						"evaluate state rule %q for job %q: %w",
						rule.ID,
						job.jobType,
						err,
					)
				}
				if shouldRun {
					return pendingRun{
						job: job,
						cause: RunCause{
							Kind:    RunCauseState,
							RuleID:  rule.ID,
							FiredAt: now,
							Reason:  strings.TrimSpace(reason),
						},
						state:   state,
						headSeq: headSeq,
						headAt:  headAt,
					}, true, nil
				}
			}
		}
	}

	return pendingRun{}, false, nil
}

func (c *Controller) runPending(ctx context.Context, pending pendingRun) error {
	startedAt := time.Now().UTC()
	state := cloneJobState(pending.state)
	state.LastRunAt = startedAt
	state.LastRunInputSeq = pending.headSeq
	state.LastRunCause = pending.cause
	if pending.cause.Kind == RunCauseSchedule {
		if state.LastScheduledFor == nil {
			state.LastScheduledFor = make(map[string]time.Time)
		}
		state.LastScheduledFor[pending.cause.RuleID] = pending.cause.ScheduledFor
	}
	if err := c.store.StoreJobState(ctx, pending.job.jobType, state); err != nil {
		return err
	}

	result, runErr := c.runner.Run(ctx, pending.job.newJob(), nil)
	var consolidationCheckpoint ConsolidationCheckpoint
	if runErr == nil && shouldAdvanceConsolidationCheckpoint(pending.job.jobType) {
		consolidationCheckpoint, runErr = c.store.StoreConsolidationCheckpoint(
			ctx,
			ConsolidationCheckpoint{
				LastConsolidatedSeq: pending.headSeq,
				LastConsolidatedAt:  pending.headAt,
			},
		)
		if runErr != nil {
			runErr = fmt.Errorf("store consolidation checkpoint: %w", runErr)
		}
	}
	var semanticExtractionCheckpoint SemanticExtractionCheckpoint
	if runErr == nil && shouldAdvanceSemanticExtractionCheckpoint(pending.job.jobType) {
		semanticExtractionCheckpoint, runErr = c.store.StoreSemanticExtractionCheckpoint(
			ctx,
			SemanticExtractionCheckpoint{
				LastExtractedSeq: pending.headSeq,
				LastExtractedAt:  pending.headAt,
			},
		)
		if runErr != nil {
			runErr = fmt.Errorf("store semantic extraction checkpoint: %w", runErr)
		}
	}
	finishedAt := time.Now().UTC()

	headSeq, headAt, err := c.store.LoadHead(ctx)
	if err != nil {
		return err
	}
	state.LastObservedSeq = headSeq
	if runErr != nil {
		state.LastFailureAt = finishedAt
		state.ConsecutiveFailures++
		state = preserveDirtyState(state, pending.headSeq, headSeq, headAt)
	} else {
		state.LastSuccessAt = finishedAt
		state.LastSuccessInputSeq = pending.headSeq
		state.ConsecutiveFailures = 0
		state = clearOrAdvanceDirtyState(state, pending.headSeq, headSeq, headAt)
	}
	if err := c.store.StoreJobState(ctx, pending.job.jobType, state); err != nil {
		return err
	}

	record := RunRecord{
		Type:            pending.job.jobType,
		Cause:           pending.cause,
		StartedAt:       startedAt,
		FinishedAt:      finishedAt,
		InputSeq:        pending.headSeq,
		InputUpdatedAt:  pending.headAt,
		OutputSeq:       headSeq,
		OutputUpdatedAt: headAt,
		Succeeded:       runErr == nil,
	}
	if runErr != nil {
		record.Error = runErr.Error()
		record.AttemptFailures = attemptFailuresFromError(runErr)
	} else {
		record.Summary = strings.TrimSpace(result.Summary)
		record.Metadata = cloneRunRecordMetadata(result.Metadata)
		record.Metadata = withConsolidationCheckpointMetadata(
			record.Metadata,
			consolidationCheckpoint,
		)
		record.Metadata = withSemanticExtractionCheckpointMetadata(
			record.Metadata,
			semanticExtractionCheckpoint,
		)
		record.ModelRef = result.ModelRef
	}
	if err := c.store.AppendRunRecord(ctx, record); err != nil {
		return err
	}
	if headSeq > pending.headSeq {
		c.NotifyStateChange()
	}
	return nil
}

func attemptFailuresFromError(err error) []AttemptFailure {
	var fallbackErr *agent.ModelFallbackError
	if !errors.As(err, &fallbackErr) || fallbackErr == nil {
		return nil
	}

	out := make([]AttemptFailure, 0, len(fallbackErr.AttemptFailures))
	for _, failure := range fallbackErr.AttemptFailures {
		modelRef := strings.TrimSpace(failure.ModelRef)
		if modelRef == "" || failure.Err == nil {
			continue
		}
		out = append(out, AttemptFailure{
			ModelRef: modelRef,
			Error:    failure.Err.Error(),
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func cloneRunRecordMetadata(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func withConsolidationCheckpointMetadata(
	metadata map[string]string,
	checkpoint ConsolidationCheckpoint,
) map[string]string {
	if checkpoint.LastConsolidatedSeq == 0 &&
		strings.TrimSpace(checkpoint.LastConsolidatedTurnID) == "" &&
		checkpoint.LastConsolidatedAt.IsZero() {
		return metadata
	}
	if metadata == nil {
		metadata = make(map[string]string)
	}
	metadata["consolidation_checkpoint_seq"] = fmt.Sprintf(
		"%d",
		checkpoint.LastConsolidatedSeq,
	)
	if turnID := strings.TrimSpace(checkpoint.LastConsolidatedTurnID); turnID != "" {
		metadata["consolidation_checkpoint_turn_id"] = turnID
	}
	if !checkpoint.LastConsolidatedAt.IsZero() {
		metadata["consolidation_checkpoint_at"] = checkpoint.LastConsolidatedAt.UTC().Format(
			time.RFC3339Nano,
		)
	}
	return metadata
}

func withSemanticExtractionCheckpointMetadata(
	metadata map[string]string,
	checkpoint SemanticExtractionCheckpoint,
) map[string]string {
	if checkpoint.LastExtractedSeq == 0 &&
		strings.TrimSpace(checkpoint.LastExtractedTurnID) == "" &&
		checkpoint.LastExtractedAt.IsZero() {
		return metadata
	}
	if metadata == nil {
		metadata = make(map[string]string)
	}
	metadata["semantic_extraction_checkpoint_seq"] = fmt.Sprintf(
		"%d",
		checkpoint.LastExtractedSeq,
	)
	if turnID := strings.TrimSpace(checkpoint.LastExtractedTurnID); turnID != "" {
		metadata["semantic_extraction_checkpoint_turn_id"] = turnID
	}
	if !checkpoint.LastExtractedAt.IsZero() {
		metadata["semantic_extraction_checkpoint_at"] = checkpoint.LastExtractedAt.UTC().Format(
			time.RFC3339Nano,
		)
	}
	return metadata
}

func (c *Controller) latestDue(
	rule compiledScheduleRule,
	state JobState,
	now time.Time,
) (time.Time, bool) {
	base := c.started
	if base.IsZero() {
		base = now
	}
	if last, ok := state.LastScheduledFor[rule.id]; ok && !last.IsZero() {
		base = last
	}

	due, ok := rule.expr.next(base)
	if !ok || due.After(now) {
		return time.Time{}, false
	}
	latest := due
	for {
		nextDue, ok := rule.expr.next(latest)
		if !ok || nextDue.After(now) {
			return latest, true
		}
		latest = nextDue
	}
}

func (c *Controller) nextScheduledAt(ctx context.Context, now time.Time) (time.Time, error) {
	if len(c.jobs) == 0 {
		return time.Time{}, nil
	}

	var nextAt time.Time
	for _, job := range c.jobs {
		state, err := c.store.LoadJobState(ctx, job.jobType)
		if err != nil {
			return time.Time{}, err
		}
		for _, rule := range job.schedule {
			base, ok := state.LastScheduledFor[rule.id]
			if !ok || base.IsZero() {
				base = c.started
			}
			candidate, ok := rule.expr.next(base)
			if !ok {
				continue
			}
			if candidate.Before(now) || candidate.Equal(now) {
				latest, ok := c.latestDue(rule, state, now)
				if ok {
					candidate = latest
				}
			}
			if nextAt.IsZero() || candidate.Before(nextAt) {
				nextAt = candidate
			}
		}
	}
	return nextAt, nil
}

func shouldEvaluateStateRules(state JobState, headSeq int64) bool {
	if state.LastRunInputSeq == 0 {
		return true
	}
	return headSeq > state.LastRunInputSeq
}

func shouldAdvanceConsolidationCheckpoint(jobType string) bool {
	return strings.TrimSpace(jobType) == workingMemoryConsolidationJobType
}

func shouldAdvanceSemanticExtractionCheckpoint(jobType string) bool {
	return strings.TrimSpace(jobType) == semanticMemoryExtractionJobType
}

func syncDirtyState(state JobState, headSeq int64, headAt time.Time) (JobState, bool) {
	if headSeq <= state.LastObservedSeq {
		return state, false
	}
	if state.DirtySinceSeq == 0 {
		dirtySinceSeq := state.LastObservedSeq + 1
		if dirtySinceSeq <= 0 {
			dirtySinceSeq = 1
		}
		state.DirtySinceSeq = dirtySinceSeq
		state.DirtySinceAt = headAt
	}
	state.LastObservedSeq = headSeq
	return state, true
}

func preserveDirtyState(state JobState, runHeadSeq, headSeq int64, headAt time.Time) JobState {
	if headSeq <= runHeadSeq {
		return state
	}
	nextDirtySeq := runHeadSeq + 1
	if state.DirtySinceSeq == 0 || state.DirtySinceSeq > nextDirtySeq {
		state.DirtySinceSeq = nextDirtySeq
		state.DirtySinceAt = headAt
	}
	return state
}

func clearOrAdvanceDirtyState(
	state JobState,
	runHeadSeq, headSeq int64,
	headAt time.Time,
) JobState {
	if headSeq <= runHeadSeq {
		state.DirtySinceSeq = 0
		state.DirtySinceAt = time.Time{}
		return state
	}
	state.DirtySinceSeq = runHeadSeq + 1
	state.DirtySinceAt = headAt
	return state
}

func cloneJobState(state JobState) JobState {
	out := state
	if len(state.LastScheduledFor) > 0 {
		out.LastScheduledFor = make(map[string]time.Time, len(state.LastScheduledFor))
		for key, value := range state.LastScheduledFor {
			key = strings.TrimSpace(key)
			if key == "" {
				continue
			}
			out.LastScheduledFor[key] = value
		}
	}
	return out
}

func resetTimer(timer *time.Timer, nextAt time.Time) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	wait := time.Hour
	if !nextAt.IsZero() {
		wait = time.Until(nextAt)
		if wait < 0 {
			wait = 0
		}
	}
	timer.Reset(wait)
}
