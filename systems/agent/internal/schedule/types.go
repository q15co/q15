// Package schedule provides persisted, owner-scoped scheduled agent jobs.
package schedule

import (
	"context"
	"errors"
	"time"

	"github.com/q15co/q15/systems/agent/internal/bus"
)

const (
	// SchemaVersion is the current persisted scheduled-job schema version.
	SchemaVersion = 1

	// KindOneShot runs a job once at its RunAt timestamp.
	KindOneShot Kind = "oneshot"
	// KindRecurring runs a job on its strict five-field UTC cron expression.
	KindRecurring Kind = "recurring"
)

const (
	// ContextProfileMinimal provides only the context required to execute the
	// stored job prompt.
	ContextProfileMinimal ContextProfile = "minimal"
	// ContextProfileAgent adds the agent's stable identity and operational
	// context. It does not grant tools or other execution authority.
	ContextProfileAgent ContextProfile = "agent"
)

const (
	// StatusPending means a job is waiting for its next due time.
	StatusPending Status = "pending"
	// StatusRunning means a due run has been durably claimed.
	StatusRunning Status = "running"
	// StatusSucceeded means the most recent run completed successfully.
	StatusSucceeded Status = "succeeded"
	// StatusFailed means the most recent run returned an execution error.
	StatusFailed Status = "failed"
	// StatusInterrupted means the runtime stopped during a claimed run.
	StatusInterrupted Status = "interrupted"
	// StatusModelUnavailable means the job's exact provider/model target could
	// not be used for this occurrence.
	StatusModelUnavailable Status = "model_unavailable"
)

var (
	// ErrNotFound hides whether a job is absent or owned by another chat.
	ErrNotFound = errors.New("scheduled job not found")
	// ErrMaxJobsReached means the configured active-job limit is full.
	ErrMaxJobsReached = errors.New("scheduled job limit reached")
	// ErrModelUnavailable identifies an attempt that could not run because its
	// exact provider/model target was unavailable.
	ErrModelUnavailable = errors.New("scheduled job model unavailable")
)

// Kind identifies a scheduled job's timing policy.
type Kind string

// Status identifies the most recent execution state of a job or run record.
type Status string

// ContextProfile selects the non-authoritative context assembled for a
// scheduled run.
type ContextProfile string

// ModelTarget identifies one exact provider/model pair. Scheduled runs never
// fall back to a different target.
type ModelTarget struct {
	Provider string `json:"provider"`
	Ref      string `json:"ref"`
}

// String returns the unambiguous provider-qualified model name.
func (t ModelTarget) String() string {
	if t.Provider == "" {
		return t.Ref
	}
	if t.Ref == "" {
		return t.Provider
	}
	return t.Provider + "/" + t.Ref
}

// ModelUnavailableError reports that an exact scheduled model target could
// not be used. Executors return this type so the manager can apply its
// retry/advance policy without treating the condition as an ordinary failure.
type ModelUnavailableError struct {
	Model ModelTarget
	Cause error
}

// Error implements error.
func (e *ModelUnavailableError) Error() string {
	if e == nil {
		return ErrModelUnavailable.Error()
	}
	if e.Cause == nil {
		return "scheduled job model " + e.Model.String() + " is unavailable"
	}
	return "scheduled job model " + e.Model.String() + " is unavailable: " + e.Cause.Error()
}

// Unwrap exposes the underlying provider error, when present.
func (e *ModelUnavailableError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// Is makes every ModelUnavailableError match ErrModelUnavailable.
func (e *ModelUnavailableError) Is(target error) bool {
	return target == ErrModelUnavailable
}

// Owner is the immutable transport identity that owns and receives a job.
type Owner struct {
	Channel string `json:"channel"`
	ChatID  string `json:"chat_id"`
	UserID  string `json:"user_id"`
}

// Job is one persisted scheduled mini-agent task.
type Job struct {
	SchemaVersion int `json:"schema_version"`

	ID             string         `json:"id"`
	Name           string         `json:"name"`
	Prompt         string         `json:"prompt"`
	ContextProfile ContextProfile `json:"context_profile"`
	Kind           Kind           `json:"kind"`
	RunAt          time.Time      `json:"run_at,omitempty"`
	Cron           string         `json:"cron,omitempty"`
	Notify         bool           `json:"notify"`
	Model          ModelTarget    `json:"model"`
	MaxTurns       int            `json:"max_turns"`
	AllowedTools   []string       `json:"allowed_tools"`
	Owner          Owner          `json:"owner"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	LastRunAt           time.Time `json:"last_run_at,omitempty"`
	LastSuccessAt       time.Time `json:"last_success_at,omitempty"`
	LastFailureAt       time.Time `json:"last_failure_at,omitempty"`
	LastScheduledFor    time.Time `json:"last_scheduled_for,omitempty"`
	NextFire            time.Time `json:"next_fire,omitempty"`
	ConsecutiveFailures int       `json:"consecutive_failures,omitempty"`
	LastStatus          Status    `json:"last_status,omitempty"`
	LastError           string    `json:"last_error,omitempty"`
	Done                bool      `json:"done,omitempty"`

	// ModelUnavailableNotified suppresses repeated notifications while the
	// exact target remains unavailable. Any subsequent success or ordinary
	// execution failure clears the latch.
	ModelUnavailableNotified bool `json:"model_unavailable_notified,omitempty"`

	// PendingRun is a system-owned recovery marker. Completion first persists
	// this record with the final job state and any notification intent, then
	// delivers the notification, records its transport acknowledgment in
	// conversation history, archives the run, and clears the marker (or deletes
	// a completed one-shot). A crash before the acknowledgment is persisted may
	// duplicate a notification, but cannot lose the durable intent or replay
	// the agent run.
	PendingRun *RunRecord `json:"pending_run,omitempty"`

	PendingNotification *Notification `json:"pending_notification,omitempty"`
}

// Notification is a durable transport delivery intent for a completed run.
type Notification struct {
	Channel     string    `json:"channel"`
	ChatID      string    `json:"chat_id"`
	Text        string    `json:"text"`
	DeliveredAt time.Time `json:"delivered_at,omitempty"`
}

// CreateRequest contains the model-controlled fields accepted when creating a
// scheduled job.
type CreateRequest struct {
	Name           string
	Prompt         string
	ContextProfile ContextProfile
	Kind           Kind
	RunAt          time.Time
	Cron           string
	Notify         bool
	Model          ModelTarget
	MaxTurns       int
	AllowedTools   []string
}

// UpdateRequest is an atomic patch for mutable job fields. Owner, ID, schema,
// creation time, and execution history are immutable.
type UpdateRequest struct {
	Name           *string
	Prompt         *string
	ContextProfile *ContextProfile
	Kind           *Kind
	RunAt          *time.Time
	Cron           *string
	Notify         *bool
	Model          *ModelTarget
	MaxTurns       *int
	AllowedTools   *[]string
}

// RunRequest contains the trusted runtime identity and timing for one
// scheduled execution. The manager, rather than the stored prompt or model,
// owns these fields.
type RunRequest struct {
	Job            Job
	RunID          string
	ScheduledFor   time.Time
	StartedAt      time.Time
	CurrentTimeUTC time.Time
}

// RunResult is the executor-produced result for one scheduled job attempt.
type RunResult struct {
	Text  string
	Model ModelTarget
}

// RunRecord is the append-only provenance record for one scheduled attempt.
type RunRecord struct {
	SchemaVersion int `json:"schema_version"`

	ID           string      `json:"id"`
	JobID        string      `json:"job_id"`
	JobName      string      `json:"job_name"`
	Owner        Owner       `json:"owner"`
	Status       Status      `json:"status"`
	Model        ModelTarget `json:"model"`
	AllowedTools []string    `json:"allowed_tools"`
	ScheduledFor time.Time   `json:"scheduled_for"`
	StartedAt    time.Time   `json:"started_at"`
	FinishedAt   time.Time   `json:"finished_at"`
	Output       string      `json:"output,omitempty"`
	Error        string      `json:"error,omitempty"`
	NotifyError  string      `json:"notify_error,omitempty"`
}

// RunFilter selects archived scheduled-run records. Since is inclusive and
// Before is exclusive. Limit bounds only the returned detail records; the
// aggregate counts always cover every matching record.
type RunFilter struct {
	JobID    string
	JobName  string
	Statuses []Status
	Since    time.Time
	Before   time.Time
	Limit    int
}

// RunQuery is the persistence-layer run-history query. Owner is system
// supplied by Manager and must match exactly; callers cannot inspect another
// chat's run records.
type RunQuery struct {
	Owner  Owner
	Filter RunFilter
}

// RunHistory contains exact aggregates and a bounded newest-first detail
// window for archived scheduled runs.
type RunHistory struct {
	Matched      int
	StatusCounts map[Status]int
	Runs         []RunRecord
}

// Store persists scheduled job definitions and run provenance.
// AppendRunRecord must be idempotent for an identical run ID.
type Store interface {
	LoadJobs(context.Context) ([]Job, error)
	StoreJob(context.Context, Job) error
	DeleteJob(context.Context, string) error
	AppendRunRecord(context.Context, RunRecord) error
	QueryRuns(context.Context, RunQuery) (RunHistory, error)
}

// Executor runs a scheduled mini-agent job.
type Executor interface {
	Execute(context.Context, RunRequest) (RunResult, error)
}

// Publisher delivers unsolicited transport-bound notifications and returns
// the endpoint's acknowledgement.
type Publisher interface {
	PublishOutboundAndWaitForDelivery(context.Context, bus.OutboundMessage) error
}

// DeliveredNotification is the exact scheduled notification acknowledged by
// its transport and ready to be recorded in canonical conversation history.
type DeliveredNotification struct {
	JobID       string
	RunID       string
	Channel     string
	ChatID      string
	Text        string
	DeliveredAt time.Time
}

// DeliveryRecorder records acknowledged scheduled notifications. Calls are
// retried after recovery and must be idempotent for the job/run identity.
type DeliveryRecorder interface {
	RecordDeliveredNotification(context.Context, DeliveredNotification) error
}

// Config supplies the scheduler's persistence, execution, policy, and clock
// dependencies.
type Config struct {
	Store            Store
	Executor         Executor
	Publisher        Publisher
	DeliveryRecorder DeliveryRecorder

	MaxJobs               int
	MaxTurns              int
	RunTimeout            time.Duration
	UnavailableRetryDelay time.Duration
	AllowedUserIDs        []int64

	DefaultModel func() ModelTarget
	ModelExists  func(ModelTarget) bool
	ToolExists   func(string) bool
	Now          func() time.Time
	NewID        func() (string, error)
}
