// Package schedule implements the owner-scoped scheduled-job tool surface.
package schedule

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/q15co/q15/systems/agent/internal/agent"
	scheduled "github.com/q15co/q15/systems/agent/internal/schedule"
	"github.com/q15co/q15/systems/agent/internal/turnctx"
)

// Create creates owner-scoped scheduled jobs.
type Create struct {
	manager *scheduled.Manager
}

// List lists owner-scoped scheduled jobs.
type List struct {
	manager *scheduled.Manager
}

// Runs queries owner-scoped archived scheduled-run history.
type Runs struct {
	manager *scheduled.Manager
}

// Update patches owner-scoped scheduled jobs.
type Update struct {
	manager *scheduled.Manager
}

// Delete deletes owner-scoped scheduled jobs.
type Delete struct {
	manager *scheduled.Manager
}

// NewCreate constructs schedule_create.
func NewCreate(manager *scheduled.Manager) *Create {
	return &Create{manager: manager}
}

// NewList constructs schedule_list.
func NewList(manager *scheduled.Manager) *List {
	return &List{manager: manager}
}

// NewRuns constructs schedule_runs.
func NewRuns(manager *scheduled.Manager) *Runs {
	return &Runs{manager: manager}
}

// NewUpdate constructs schedule_update.
func NewUpdate(manager *scheduled.Manager) *Update {
	return &Update{manager: manager}
}

// NewDelete constructs schedule_delete.
func NewDelete(manager *scheduled.Manager) *Delete {
	return &Delete{manager: manager}
}

// Definition returns the schedule_create schema.
func (t *Create) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name: "schedule_create",
		Description: "Create a durable one-shot or recurring UTC job that runs an isolated " +
			"mini-agent task and optionally notifies this chat",
		PromptGuidance: []string{
			"Use RFC3339 UTC timestamps for one-shot run_at values and strict five-field UTC cron expressions for recurring jobs.",
			"Omit both provider and model to pin the current interactive provider/model pair, or supply both from list_models.",
			"Context profile minimal is the default; agent adds stable agent context but never the interactive conversation.",
			"Choose the smallest allowed_tools set needed by this task. Use exact tool names visible in the current tool catalog, or [] when the task needs no tools.",
			"Context profiles do not change tool authority; allowed_tools is the complete capability set for this job.",
			"Ownership and notification destination are injected from the current chat and cannot be supplied by the model.",
		},
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "Short human-readable job label.",
				},
				"prompt": map[string]any{
					"type":        "string",
					"description": "Task for the scheduled mini-agent.",
				},
				"context_profile": map[string]any{
					"type": "string",
					"enum": []string{
						string(scheduled.ContextProfileMinimal),
						string(scheduled.ContextProfileAgent),
					},
					"default": string(scheduled.ContextProfileMinimal),
					"description": "Context assembled for the run. This does not change " +
						"the per-job allowed_tools policy.",
				},
				"kind": map[string]any{
					"type": "string",
					"enum": []string{
						string(scheduled.KindOneShot),
						string(scheduled.KindRecurring),
					},
				},
				"run_at": map[string]any{
					"type":        "string",
					"format":      "date-time",
					"description": "Future RFC3339 UTC timestamp; required for oneshot.",
				},
				"cron": map[string]any{
					"type":        "string",
					"description": "Strict five-field UTC cron expression; required for recurring.",
				},
				"notify": map[string]any{
					"type":    "boolean",
					"default": true,
				},
				"provider": map[string]any{
					"type":        "string",
					"description": "Optional configured provider name; must be supplied together with model.",
				},
				"model": map[string]any{
					"type":        "string",
					"description": "Optional agent-side model ref; must be supplied together with provider.",
				},
				"max_turns": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"maximum":     128,
					"description": "Optional run turn cap, bounded by operator configuration.",
				},
				"allowed_tools": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "string",
					},
					"uniqueItems": true,
					"description": "Exact tool names this job may use. Use an empty array " +
						"when the task needs no tools.",
				},
			},
			"required": []string{"name", "prompt", "kind", "allowed_tools"},
		},
	}
}

// Run executes schedule_create.
func (t *Create) Run(ctx context.Context, arguments string) (string, error) {
	if t == nil || t.manager == nil {
		return "", fmt.Errorf("schedule manager is not configured")
	}
	var args struct {
		Name           string                   `json:"name"`
		Prompt         string                   `json:"prompt"`
		ContextProfile scheduled.ContextProfile `json:"context_profile"`
		Kind           scheduled.Kind           `json:"kind"`
		RunAt          string                   `json:"run_at"`
		Cron           string                   `json:"cron"`
		Notify         *bool                    `json:"notify"`
		Provider       string                   `json:"provider"`
		ModelRef       string                   `json:"model"`
		MaxTurns       int                      `json:"max_turns"`
		AllowedTools   *[]string                `json:"allowed_tools"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments JSON: %w", err)
	}
	if args.AllowedTools == nil {
		return "", fmt.Errorf("missing required argument: allowed_tools")
	}
	runAt, err := parseOptionalUTC(args.RunAt)
	if err != nil {
		return "", err
	}
	if err := validateTimingArgs(args.Kind, args.RunAt, args.Cron); err != nil {
		return "", err
	}
	notify := true
	if args.Notify != nil {
		notify = *args.Notify
	}
	model, err := optionalModelTarget(args.Provider, args.ModelRef)
	if err != nil {
		return "", err
	}
	owner, err := ownerFromContext(ctx)
	if err != nil {
		return "", err
	}
	job, err := t.manager.Create(ctx, owner, scheduled.CreateRequest{
		Name:           args.Name,
		Prompt:         args.Prompt,
		ContextProfile: args.ContextProfile,
		Kind:           args.Kind,
		RunAt:          runAt,
		Cron:           args.Cron,
		Notify:         notify,
		Model:          model,
		MaxTurns:       args.MaxTurns,
		AllowedTools:   *args.AllowedTools,
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(
		"Created scheduled job %s (%q), context profile %s, pinned to %s, allowed tools: %s. Next fire: %s UTC.",
		job.ID,
		job.Name,
		job.ContextProfile,
		job.Model,
		formatAllowedTools(job.AllowedTools),
		job.NextFire.UTC().Format(time.RFC3339),
	), nil
}

// Definition returns the schedule_list schema.
func (t *List) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name:        "schedule_list",
		Description: "List active scheduled jobs owned by this exact chat",
		PromptGuidance: []string{
			"Use this before changing or deleting a job when its id is not already known.",
			"Context profiles never imply tool authority; allowed_tools is the exact per-job capability set.",
		},
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}
}

// Run executes schedule_list.
func (t *List) Run(ctx context.Context, arguments string) (string, error) {
	if t == nil || t.manager == nil {
		return "", fmt.Errorf("schedule manager is not configured")
	}
	if err := validateEmptyObject(arguments); err != nil {
		return "", err
	}
	owner, err := ownerFromContext(ctx)
	if err != nil {
		return "", err
	}
	jobs, err := t.manager.List(ctx, owner)
	if err != nil {
		return "", err
	}
	if len(jobs) == 0 {
		return "No active scheduled jobs for this chat.", nil
	}

	var b strings.Builder
	b.WriteString(
		"id | name | kind | context_profile | allowed_tools | next_fire_utc | last_run_utc | status | prompt\n",
	)
	b.WriteString("--- | --- | --- | --- | --- | --- | --- | --- | ---\n")
	for _, job := range jobs {
		fmt.Fprintf(
			&b,
			"%s | %s | %s | %s | %s | %s | %s | %s | %s\n",
			job.ID,
			tableCell(job.Name),
			job.Kind,
			job.ContextProfile,
			tableCell(formatAllowedTools(job.AllowedTools)),
			formatTime(job.NextFire),
			formatTime(job.LastRunAt),
			job.LastStatus,
			tableCell(truncate(job.Prompt, 120)),
		)
	}
	return strings.TrimSpace(b.String()), nil
}

// Definition returns the schedule_runs schema.
func (t *Runs) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name: "schedule_runs",
		Description: "Query the canonical archived run history for scheduled jobs owned " +
			"by this exact chat, including exact aggregate counts and bounded details",
		PromptGuidance: []string{
			"Use matched_runs and status_counts as the source of truth for execution counts; never count transcript messages or notification text.",
			"Use job_name for historical or completed one-shot jobs that are no longer returned by schedule_list. Names are exact and may match more than one job id.",
			"Use schedule_list first when an active job id is needed. Ownership is injected from the current chat and cannot be supplied by the model.",
			"The limit bounds detail rows only; matched_runs and status_counts always cover every matching archived run.",
		},
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"job_id": map[string]any{
					"type":        "string",
					"description": "Optional exact immutable job id.",
				},
				"job_name": map[string]any{
					"type":        "string",
					"description": "Optional exact historical job name.",
				},
				"statuses": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "string",
						"enum": []string{
							string(scheduled.StatusSucceeded),
							string(scheduled.StatusFailed),
							string(scheduled.StatusInterrupted),
							string(scheduled.StatusModelUnavailable),
						},
					},
					"uniqueItems": true,
					"description": "Optional terminal run statuses to include.",
				},
				"since": map[string]any{
					"type":        "string",
					"format":      "date-time",
					"description": "Optional inclusive RFC3339 UTC lower bound on run start.",
				},
				"before": map[string]any{
					"type":        "string",
					"format":      "date-time",
					"description": "Optional exclusive RFC3339 UTC upper bound on run start.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"maximum":     100,
					"default":     20,
					"description": "Maximum newest-first detail records; aggregate counts are unbounded.",
				},
			},
		},
	}
}

// Run executes schedule_runs.
func (t *Runs) Run(ctx context.Context, arguments string) (string, error) {
	if t == nil || t.manager == nil {
		return "", fmt.Errorf("schedule manager is not configured")
	}
	if strings.TrimSpace(arguments) == "" {
		arguments = "{}"
	}
	var args struct {
		JobID    string             `json:"job_id"`
		JobName  string             `json:"job_name"`
		Statuses []scheduled.Status `json:"statuses"`
		Since    string             `json:"since"`
		Before   string             `json:"before"`
		Limit    int                `json:"limit"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments JSON: %w", err)
	}
	since, err := parseUTCField("since", args.Since)
	if err != nil {
		return "", err
	}
	before, err := parseUTCField("before", args.Before)
	if err != nil {
		return "", err
	}
	if args.Limit == 0 {
		args.Limit = 20
	}
	owner, err := ownerFromContext(ctx)
	if err != nil {
		return "", err
	}
	history, err := t.manager.RunHistory(ctx, owner, scheduled.RunFilter{
		JobID:    args.JobID,
		JobName:  args.JobName,
		Statuses: args.Statuses,
		Since:    since,
		Before:   before,
		Limit:    args.Limit,
	})
	if err != nil {
		return "", err
	}
	output, err := json.MarshalIndent(runHistoryOutput(history), "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode scheduled run history: %w", err)
	}
	return string(output), nil
}

type runsOutput struct {
	MatchedRuns int               `json:"matched_runs"`
	Returned    int               `json:"returned_runs"`
	HasMore     bool              `json:"has_more"`
	StatusCount map[string]int    `json:"status_counts"`
	Runs        []runRecordOutput `json:"runs"`
}

type runRecordOutput struct {
	RunID             string   `json:"run_id"`
	JobID             string   `json:"job_id"`
	JobName           string   `json:"job_name"`
	Status            string   `json:"status"`
	ScheduledForUTC   string   `json:"scheduled_for_utc"`
	StartedAtUTC      string   `json:"started_at_utc"`
	FinishedAtUTC     string   `json:"finished_at_utc"`
	DurationMillis    int64    `json:"duration_ms"`
	Model             string   `json:"model"`
	AllowedTools      []string `json:"allowed_tools"`
	Output            string   `json:"output,omitempty"`
	Error             string   `json:"error,omitempty"`
	NotificationError string   `json:"notification_error,omitempty"`
}

func runHistoryOutput(history scheduled.RunHistory) runsOutput {
	output := runsOutput{
		MatchedRuns: history.Matched,
		Returned:    len(history.Runs),
		HasMore:     history.Matched > len(history.Runs),
		StatusCount: make(map[string]int, len(history.StatusCounts)),
		Runs:        make([]runRecordOutput, 0, len(history.Runs)),
	}
	for status, count := range history.StatusCounts {
		output.StatusCount[string(status)] = count
	}
	for _, record := range history.Runs {
		duration := record.FinishedAt.Sub(record.StartedAt)
		if duration < 0 {
			duration = 0
		}
		output.Runs = append(output.Runs, runRecordOutput{
			RunID:           record.ID,
			JobID:           record.JobID,
			JobName:         record.JobName,
			Status:          string(record.Status),
			ScheduledForUTC: formatTime(record.ScheduledFor),
			StartedAtUTC:    formatTime(record.StartedAt),
			FinishedAtUTC:   formatTime(record.FinishedAt),
			DurationMillis:  duration.Milliseconds(),
			Model:           record.Model.String(),
			AllowedTools: append(
				make([]string, 0, len(record.AllowedTools)),
				record.AllowedTools...),
			Output:            truncate(record.Output, 500),
			Error:             truncate(record.Error, 500),
			NotificationError: truncate(record.NotifyError, 500),
		})
	}
	return output
}

// Definition returns the schedule_update schema.
func (t *Update) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name:        "schedule_update",
		Description: "Update an active scheduled job owned by this exact chat without changing its id or history",
		PromptGuidance: []string{
			"Only supplied fields change. Ownership and notification destination are immutable.",
			"When changing kind or timing, supply run_at for oneshot or cron for recurring.",
			"When changing the model target, supply both provider and model from list_models.",
			"Set allowed_tools to the smallest complete set needed by the task; use [] to revoke all tool access.",
			"Changing context_profile changes context only; tool authority comes exclusively from allowed_tools.",
		},
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":     map[string]any{"type": "string"},
				"name":   map[string]any{"type": "string"},
				"prompt": map[string]any{"type": "string"},
				"context_profile": map[string]any{
					"type": "string",
					"enum": []string{
						string(scheduled.ContextProfileMinimal),
						string(scheduled.ContextProfileAgent),
					},
					"description": "Context assembled for the run. This does not change " +
						"the per-job allowed_tools policy.",
				},
				"kind": map[string]any{
					"type": "string",
					"enum": []string{
						string(scheduled.KindOneShot),
						string(scheduled.KindRecurring),
					},
				},
				"run_at": map[string]any{"type": "string", "format": "date-time"},
				"cron":   map[string]any{"type": "string"},
				"notify": map[string]any{"type": "boolean"},
				"provider": map[string]any{
					"type":        "string",
					"description": "Configured provider name; must be supplied together with model.",
				},
				"model": map[string]any{
					"type":        "string",
					"description": "Agent-side model ref; must be supplied together with provider.",
				},
				"max_turns": map[string]any{
					"type":    "integer",
					"minimum": 1,
					"maximum": 128,
				},
				"allowed_tools": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "string",
					},
					"uniqueItems": true,
					"description": "Replacement exact tool-name allow-list. An empty " +
						"array revokes all tool access.",
				},
			},
			"required": []string{"id"},
		},
	}
}

// Run executes schedule_update.
func (t *Update) Run(ctx context.Context, arguments string) (string, error) {
	if t == nil || t.manager == nil {
		return "", fmt.Errorf("schedule manager is not configured")
	}
	var args struct {
		ID             string                    `json:"id"`
		Name           *string                   `json:"name"`
		Prompt         *string                   `json:"prompt"`
		ContextProfile *scheduled.ContextProfile `json:"context_profile"`
		Kind           *scheduled.Kind           `json:"kind"`
		RunAt          *string                   `json:"run_at"`
		Cron           *string                   `json:"cron"`
		Notify         *bool                     `json:"notify"`
		Provider       *string                   `json:"provider"`
		ModelRef       *string                   `json:"model"`
		MaxTurns       *int                      `json:"max_turns"`
		AllowedTools   *[]string                 `json:"allowed_tools"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments JSON: %w", err)
	}
	if strings.TrimSpace(args.ID) == "" {
		return "", fmt.Errorf("missing required argument: id")
	}
	if args.RunAt != nil && args.Cron != nil {
		return "", fmt.Errorf("run_at and cron are mutually exclusive")
	}
	var runAt *time.Time
	if args.RunAt != nil {
		parsed, err := parseOptionalUTC(*args.RunAt)
		if err != nil {
			return "", err
		}
		runAt = &parsed
	}
	if args.Kind != nil {
		runAtValue := ""
		if args.RunAt != nil {
			runAtValue = *args.RunAt
		}
		cronValue := ""
		if args.Cron != nil {
			cronValue = *args.Cron
		}
		if err := validateTimingArgs(*args.Kind, runAtValue, cronValue); err != nil {
			return "", err
		}
	}
	model, err := updatedModelTarget(args.Provider, args.ModelRef)
	if err != nil {
		return "", err
	}

	owner, err := ownerFromContext(ctx)
	if err != nil {
		return "", err
	}
	job, err := t.manager.Update(ctx, owner, args.ID, scheduled.UpdateRequest{
		Name:           args.Name,
		Prompt:         args.Prompt,
		ContextProfile: args.ContextProfile,
		Kind:           args.Kind,
		RunAt:          runAt,
		Cron:           args.Cron,
		Notify:         args.Notify,
		Model:          model,
		MaxTurns:       args.MaxTurns,
		AllowedTools:   args.AllowedTools,
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(
		"Updated scheduled job %s (%q), context profile %s, pinned to %s, allowed tools: %s. Next fire: %s UTC.",
		job.ID,
		job.Name,
		job.ContextProfile,
		job.Model,
		formatAllowedTools(job.AllowedTools),
		job.NextFire.UTC().Format(time.RFC3339),
	), nil
}

// Definition returns the schedule_delete schema.
func (t *Delete) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name:        "schedule_delete",
		Description: "Idempotently delete an active scheduled job owned by this exact chat",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "string"},
			},
			"required": []string{"id"},
		},
	}
}

// Run executes schedule_delete.
func (t *Delete) Run(ctx context.Context, arguments string) (string, error) {
	if t == nil || t.manager == nil {
		return "", fmt.Errorf("schedule manager is not configured")
	}
	var args struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments JSON: %w", err)
	}
	args.ID = strings.TrimSpace(args.ID)
	if args.ID == "" {
		return "", fmt.Errorf("missing required argument: id")
	}
	owner, err := ownerFromContext(ctx)
	if err != nil {
		return "", err
	}
	if err := t.manager.Delete(ctx, owner, args.ID); err != nil {
		if errors.Is(err, scheduled.ErrNotFound) {
			return "", scheduled.ErrNotFound
		}
		return "", err
	}
	return fmt.Sprintf("Deleted scheduled job %s (already absent is also success).", args.ID), nil
}

func ownerFromContext(ctx context.Context) (scheduled.Owner, error) {
	origin, ok := turnctx.OriginFrom(ctx)
	if !ok {
		return scheduled.Owner{}, fmt.Errorf("scheduled jobs require an originating chat")
	}
	return scheduled.Owner{
		Channel: origin.Channel,
		ChatID:  origin.ChatID,
		UserID:  origin.UserID,
	}, nil
}

func parseOptionalUTC(raw string) (time.Time, error) {
	return parseUTCField("run_at", raw)
}

func parseUTCField(field, raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s must be RFC3339: %w", field, err)
	}
	_, offset := parsed.Zone()
	if offset != 0 {
		return time.Time{}, fmt.Errorf("%s must use UTC (Z or +00:00)", field)
	}
	return parsed.UTC(), nil
}

func optionalModelTarget(provider, model string) (scheduled.ModelTarget, error) {
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	if provider == "" && model == "" {
		return scheduled.ModelTarget{}, nil
	}
	if provider == "" || model == "" {
		return scheduled.ModelTarget{}, fmt.Errorf(
			"provider and model must be supplied together",
		)
	}
	return scheduled.ModelTarget{Provider: provider, Ref: model}, nil
}

func updatedModelTarget(provider, model *string) (*scheduled.ModelTarget, error) {
	if provider == nil && model == nil {
		return nil, nil
	}
	if provider == nil || model == nil {
		return nil, fmt.Errorf("provider and model must be supplied together")
	}
	target, err := optionalModelTarget(*provider, *model)
	if err != nil {
		return nil, err
	}
	if target.Provider == "" {
		return nil, fmt.Errorf("provider and model must not be blank")
	}
	return &target, nil
}

func validateTimingArgs(kind scheduled.Kind, runAt, cron string) error {
	switch kind {
	case scheduled.KindOneShot:
		if strings.TrimSpace(runAt) == "" {
			return fmt.Errorf("run_at is required for oneshot jobs")
		}
		if strings.TrimSpace(cron) != "" {
			return fmt.Errorf("cron must be omitted for oneshot jobs")
		}
	case scheduled.KindRecurring:
		if strings.TrimSpace(cron) == "" {
			return fmt.Errorf("cron is required for recurring jobs")
		}
		if strings.TrimSpace(runAt) != "" {
			return fmt.Errorf("run_at must be omitted for recurring jobs")
		}
	default:
		return fmt.Errorf("kind must be %q or %q", scheduled.KindOneShot, scheduled.KindRecurring)
	}
	return nil
}

func validateEmptyObject(arguments string) error {
	arguments = strings.TrimSpace(arguments)
	if arguments == "" {
		return nil
	}
	var args map[string]json.RawMessage
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return fmt.Errorf("invalid arguments JSON: %w", err)
	}
	if len(args) != 0 {
		return fmt.Errorf("schedule_list accepts no arguments")
	}
	return nil
}

func tableCell(value string) string {
	value = strings.ReplaceAll(value, "|", "\\|")
	value = strings.Join(strings.Fields(value), " ")
	return value
}

func truncate(value string, limit int) string {
	if limit <= 0 || utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	if limit == 1 {
		return "…"
	}
	return string(runes[:limit-1]) + "…"
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return value.UTC().Format(time.RFC3339)
}

func formatAllowedTools(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}
