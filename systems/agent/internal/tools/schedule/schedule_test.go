package schedule

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/bus"
	scheduled "github.com/q15co/q15/systems/agent/internal/schedule"
	"github.com/q15co/q15/systems/agent/internal/turnctx"
)

var toolTestNow = time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC)

type toolTestStore struct {
	mu      sync.Mutex
	jobs    map[string]scheduled.Job
	records []scheduled.RunRecord
}

func newToolTestStore() *toolTestStore {
	return &toolTestStore{jobs: make(map[string]scheduled.Job)}
}

func (s *toolTestStore) LoadJobs(context.Context) ([]scheduled.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	jobs := make([]scheduled.Job, 0, len(s.jobs))
	for _, job := range s.jobs {
		jobs = append(jobs, job)
	}
	return jobs, nil
}

func (s *toolTestStore) StoreJob(_ context.Context, job scheduled.Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs[job.ID] = job
	return nil
}

func (s *toolTestStore) DeleteJob(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.jobs, id)
	return nil
}

func (s *toolTestStore) AppendRunRecord(_ context.Context, record scheduled.RunRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, record)
	return nil
}

func (s *toolTestStore) QueryRuns(
	_ context.Context,
	query scheduled.RunQuery,
) (scheduled.RunHistory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	history := scheduled.RunHistory{StatusCounts: make(map[scheduled.Status]int)}
	for index := len(s.records) - 1; index >= 0; index-- {
		record := s.records[index]
		if record.Owner != query.Owner ||
			(query.Filter.JobID != "" && record.JobID != query.Filter.JobID) ||
			(query.Filter.JobName != "" && record.JobName != query.Filter.JobName) ||
			(!query.Filter.Since.IsZero() && record.StartedAt.Before(query.Filter.Since)) ||
			(!query.Filter.Before.IsZero() && !record.StartedAt.Before(query.Filter.Before)) ||
			!toolStatusMatches(query.Filter.Statuses, record.Status) {
			continue
		}
		history.Matched++
		history.StatusCounts[record.Status]++
		if len(history.Runs) < query.Filter.Limit {
			history.Runs = append(history.Runs, record)
		}
	}
	return history, nil
}

func toolStatusMatches(statuses []scheduled.Status, candidate scheduled.Status) bool {
	if len(statuses) == 0 {
		return true
	}
	for _, status := range statuses {
		if status == candidate {
			return true
		}
	}
	return false
}

type toolTestExecutor struct{}

func (toolTestExecutor) Execute(
	context.Context,
	scheduled.RunRequest,
) (scheduled.RunResult, error) {
	return scheduled.RunResult{}, nil
}

type toolTestPublisher struct{}

func (toolTestPublisher) PublishOutboundAndWaitForDelivery(
	context.Context,
	bus.OutboundMessage,
) error {
	return nil
}

type toolTestDeliveryRecorder struct{}

func (toolTestDeliveryRecorder) RecordDeliveredNotification(
	context.Context,
	scheduled.DeliveredNotification,
) error {
	return nil
}

func newToolTestManager(t *testing.T, maxTurns int) *scheduled.Manager {
	t.Helper()

	manager, _ := newToolTestManagerWithStore(t, maxTurns)
	return manager
}

func newToolTestManagerWithStore(
	t *testing.T,
	maxTurns int,
) (*scheduled.Manager, *toolTestStore) {
	t.Helper()

	nextID := 0
	store := newToolTestStore()
	manager, err := scheduled.NewManager(context.Background(), scheduled.Config{
		Store:            store,
		Executor:         toolTestExecutor{},
		Publisher:        toolTestPublisher{},
		DeliveryRecorder: toolTestDeliveryRecorder{},
		MaxJobs:          16,
		MaxTurns:         maxTurns,
		RunTimeout:       time.Minute,
		AllowedUserIDs:   []int64{42},
		DefaultModel: func() scheduled.ModelTarget {
			return scheduled.ModelTarget{Provider: "default", Ref: "model"}
		},
		ModelExists: func(scheduled.ModelTarget) bool {
			return true
		},
		ToolExists: func(name string) bool {
			switch name {
			case "read_file", "web_fetch":
				return true
			default:
				return false
			}
		},
		Now: func() time.Time {
			return toolTestNow
		},
		NewID: func() (string, error) {
			nextID++
			return fmt.Sprintf("job-%d", nextID), nil
		},
	})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	return manager, store
}

func toolTestContext(chatID string) context.Context {
	return turnctx.WithOrigin(context.Background(), turnctx.Origin{
		Channel:   bus.ChannelTelegram,
		ChatID:    chatID,
		UserID:    "42",
		MessageID: "message-1",
	})
}

func TestToolDefinitions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		tool       agent.Tool
		wantName   string
		properties []string
		required   []string
	}{
		{
			name:     "create",
			tool:     NewCreate(nil),
			wantName: "schedule_create",
			properties: []string{
				"allowed_tools",
				"context_profile",
				"cron",
				"kind",
				"max_turns",
				"model",
				"name",
				"notify",
				"prompt",
				"provider",
				"run_at",
			},
			required: []string{"allowed_tools", "kind", "name", "prompt"},
		},
		{
			name:       "list",
			tool:       NewList(nil),
			wantName:   "schedule_list",
			properties: []string{},
			required:   nil,
		},
		{
			name:     "runs",
			tool:     NewRuns(nil),
			wantName: "schedule_runs",
			properties: []string{
				"before",
				"job_id",
				"job_name",
				"limit",
				"since",
				"statuses",
			},
			required: nil,
		},
		{
			name:     "update",
			tool:     NewUpdate(nil),
			wantName: "schedule_update",
			properties: []string{
				"allowed_tools",
				"context_profile",
				"cron",
				"id",
				"kind",
				"max_turns",
				"model",
				"name",
				"notify",
				"prompt",
				"provider",
				"run_at",
			},
			required: []string{"id"},
		},
		{
			name:       "delete",
			tool:       NewDelete(nil),
			wantName:   "schedule_delete",
			properties: []string{"id"},
			required:   []string{"id"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			def := tt.tool.Definition()
			if def.Name != tt.wantName {
				t.Fatalf("Definition().Name = %q, want %q", def.Name, tt.wantName)
			}
			if strings.TrimSpace(def.Description) == "" {
				t.Fatal("Definition().Description is empty")
			}
			if got := schemaStringKeys(t, def.Parameters, "properties"); !equalStringSets(
				got,
				tt.properties,
			) {
				t.Fatalf("schema properties = %v, want %v", got, tt.properties)
			}
			if got := schemaStrings(t, def.Parameters, "required"); !equalStringSets(
				got,
				tt.required,
			) {
				t.Fatalf("schema required = %v, want %v", got, tt.required)
			}
		})
	}
}

func TestRunsUsesCanonicalOwnerScopedAggregatesBeyondDetailLimit(t *testing.T) {
	t.Parallel()

	manager, store := newToolTestManagerWithStore(t, 9)
	owner := scheduled.Owner{
		Channel: bus.ChannelTelegram,
		ChatID:  "chat-1",
		UserID:  "42",
	}
	base := scheduled.RunRecord{
		SchemaVersion: scheduled.SchemaVersion,
		ID:            "run-1",
		JobID:         "job-1",
		JobName:       "minute check",
		Owner:         owner,
		Status:        scheduled.StatusSucceeded,
		Model:         scheduled.ModelTarget{Provider: "provider", Ref: "model"},
		AllowedTools:  []string{"read_file"},
		ScheduledFor:  toolTestNow.Add(-3 * time.Minute),
		StartedAt:     toolTestNow.Add(-3 * time.Minute),
		FinishedAt:    toolTestNow.Add(-3*time.Minute + time.Second),
		Output:        "first",
	}
	for index, status := range []scheduled.Status{
		scheduled.StatusSucceeded,
		scheduled.StatusFailed,
		scheduled.StatusSucceeded,
	} {
		record := base
		record.ID = fmt.Sprintf("run-%d", index+1)
		record.Status = status
		record.StartedAt = base.StartedAt.Add(time.Duration(index) * time.Minute)
		record.FinishedAt = record.StartedAt.Add(time.Second)
		record.ScheduledFor = record.StartedAt
		if status == scheduled.StatusFailed {
			record.Output = ""
			record.Error = "request failed"
		}
		if err := store.AppendRunRecord(context.Background(), record); err != nil {
			t.Fatalf("AppendRunRecord() error = %v", err)
		}
	}
	otherOwner := base
	otherOwner.ID = "run-other"
	otherOwner.Owner.ChatID = "chat-2"
	if err := store.AppendRunRecord(context.Background(), otherOwner); err != nil {
		t.Fatalf("AppendRunRecord(other owner) error = %v", err)
	}

	raw, err := NewRuns(manager).Run(
		toolTestContext("chat-1"),
		`{"job_name":"minute check","limit":1}`,
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	var output runsOutput
	if err := json.Unmarshal([]byte(raw), &output); err != nil {
		t.Fatalf("Unmarshal(output) error = %v; output = %q", err, raw)
	}
	if output.MatchedRuns != 3 || output.Returned != 1 || !output.HasMore {
		t.Fatalf("output aggregates = %#v", output)
	}
	if output.StatusCount[string(scheduled.StatusSucceeded)] != 2 ||
		output.StatusCount[string(scheduled.StatusFailed)] != 1 {
		t.Fatalf("output status counts = %#v", output.StatusCount)
	}
	if len(output.Runs) != 1 || output.Runs[0].RunID != "run-3" {
		t.Fatalf("output runs = %#v, want newest run-3", output.Runs)
	}
	if got := output.Runs[0].AllowedTools; len(got) != 1 || got[0] != "read_file" {
		t.Fatalf("output run allowed_tools = %#v, want read_file", got)
	}
}

func TestRunsValidatesArgumentsWithoutRelyingOnSchema(t *testing.T) {
	t.Parallel()

	manager := newToolTestManager(t, 9)
	tool := NewRuns(manager)
	ctx := toolTestContext("chat-1")
	if output, err := tool.Run(ctx, ""); err != nil {
		t.Fatalf("Run(empty) error = %v", err)
	} else if !strings.Contains(output, `"matched_runs": 0`) {
		t.Fatalf("Run(empty) output = %q", output)
	}

	for _, arguments := range []string{
		`{"limit":101}`,
		`{"limit":-1}`,
		`{"statuses":["running"]}`,
		`{"statuses":["unknown"]}`,
		`{"since":"2026-07-18T12:00:00+01:00"}`,
		`{"since":"2026-07-18T12:00:00Z","before":"2026-07-18T12:00:00Z"}`,
	} {
		if _, err := tool.Run(ctx, arguments); err == nil {
			t.Fatalf("Run(%s) error = nil, want non-nil", arguments)
		}
	}
}

func TestCreateDefaultsAndRequiresUTC(t *testing.T) {
	t.Parallel()

	manager := newToolTestManager(t, 9)
	tool := NewCreate(manager)
	ctx := toolTestContext("chat-1")
	runAt := toolTestNow.Add(2 * time.Hour)

	output, err := tool.Run(ctx, fmt.Sprintf(
		`{"name":"Morning review","prompt":"Review the workspace.","kind":"oneshot","run_at":%q,"allowed_tools":[]}`,
		runAt.Format("2006-01-02T15:04:05+00:00"),
	))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(output, "Created scheduled job job-1") ||
		!strings.Contains(output, "context profile minimal") ||
		!strings.Contains(output, "pinned to default/model") {
		t.Fatalf("Run() output = %q", output)
	}

	jobs, err := manager.List(context.Background(), scheduled.Owner{
		Channel: bus.ChannelTelegram,
		ChatID:  "chat-1",
		UserID:  "42",
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("List() jobs = %d, want 1", len(jobs))
	}
	job := jobs[0]
	if !job.Notify {
		t.Fatal("Notify = false, want default true")
	}
	if job.Model != (scheduled.ModelTarget{Provider: "default", Ref: "model"}) {
		t.Fatalf("Model = %#v, want default/model", job.Model)
	}
	if job.ContextProfile != scheduled.ContextProfileMinimal {
		t.Fatalf(
			"ContextProfile = %q, want %q",
			job.ContextProfile,
			scheduled.ContextProfileMinimal,
		)
	}
	if job.MaxTurns != 9 {
		t.Fatalf("MaxTurns = %d, want manager default 9", job.MaxTurns)
	}
	if !job.RunAt.Equal(runAt) || !job.NextFire.Equal(runAt) {
		t.Fatalf("job timing = RunAt %s NextFire %s, want %s", job.RunAt, job.NextFire, runAt)
	}

	_, err = tool.Run(ctx, fmt.Sprintf(
		`{"name":"Wrong zone","prompt":"Must fail.","kind":"oneshot","run_at":%q,"allowed_tools":[]}`,
		runAt.In(time.FixedZone("UTC+2", 2*60*60)).Format(time.RFC3339),
	))
	if err == nil || !strings.Contains(err.Error(), "run_at must use UTC") {
		t.Fatalf("non-UTC Run() error = %v, want UTC validation error", err)
	}
}

func TestAllowedToolsSchemasAndToolBehavior(t *testing.T) {
	t.Parallel()

	createDefinition := NewCreate(nil).Definition()
	createAllowedTools := schemaProperty(t, createDefinition.Parameters, "allowed_tools")
	if createAllowedTools["type"] != "array" || createAllowedTools["uniqueItems"] != true {
		t.Fatalf("create allowed_tools schema = %#v", createAllowedTools)
	}
	updateDefinition := NewUpdate(nil).Definition()
	updateAllowedTools := schemaProperty(t, updateDefinition.Parameters, "allowed_tools")
	if updateAllowedTools["type"] != "array" || updateAllowedTools["uniqueItems"] != true {
		t.Fatalf("update allowed_tools schema = %#v", updateAllowedTools)
	}

	manager := newToolTestManager(t, 8)
	ctx := toolTestContext("chat-tools")
	create := NewCreate(manager)
	if _, err := create.Run(
		ctx,
		`{"name":"Missing policy","prompt":"Fail.","kind":"recurring","cron":"0 * * * *"}`,
	); err == nil || !strings.Contains(err.Error(), "missing required argument: allowed_tools") {
		t.Fatalf("Create.Run(missing allowed_tools) error = %v", err)
	}
	output, err := create.Run(
		ctx,
		`{"name":"Read and fetch","prompt":"Use both.","kind":"recurring","cron":"0 * * * *","allowed_tools":[" read_file ","web_fetch","read_file",""]}`,
	)
	if err != nil {
		t.Fatalf("Create.Run() error = %v", err)
	}
	if !strings.Contains(output, "allowed tools: read_file, web_fetch") {
		t.Fatalf("Create.Run() output = %q", output)
	}
	job := onlyToolTestJob(t, manager, "chat-tools")
	if got, want := job.AllowedTools, []string{"read_file", "web_fetch"}; !equalStringSets(
		got,
		want,
	) || len(got) != len(want) {
		t.Fatalf("created AllowedTools = %#v, want %#v", got, want)
	}

	if _, err := NewUpdate(manager).Run(
		ctx,
		`{"id":"job-1","allowed_tools":["exec"]}`,
	); err == nil || !strings.Contains(err.Error(), `unknown scheduled job tool "exec"`) {
		t.Fatalf("Update.Run(unknown tool) error = %v", err)
	}
	job = onlyToolTestJob(t, manager, "chat-tools")
	if len(job.AllowedTools) != 2 {
		t.Fatalf("failed update changed AllowedTools to %#v", job.AllowedTools)
	}

	output, err = NewUpdate(manager).Run(
		ctx,
		`{"id":"job-1","allowed_tools":[]}`,
	)
	if err != nil {
		t.Fatalf("Update.Run(clear tools) error = %v", err)
	}
	if !strings.Contains(output, "allowed tools: none") {
		t.Fatalf("Update.Run(clear tools) output = %q", output)
	}
	job = onlyToolTestJob(t, manager, "chat-tools")
	if len(job.AllowedTools) != 0 {
		t.Fatalf("updated AllowedTools = %#v, want empty", job.AllowedTools)
	}
}

func TestContextProfileSchemasAndToolBehavior(t *testing.T) {
	t.Parallel()

	createDefinition := NewCreate(nil).Definition()
	createProfile := schemaProperty(t, createDefinition.Parameters, "context_profile")
	if got := schemaStrings(t, createProfile, "enum"); !equalStringSets(got, []string{
		string(scheduled.ContextProfileMinimal),
		string(scheduled.ContextProfileAgent),
	}) {
		t.Fatalf("create context_profile enum = %v", got)
	}
	if got := createProfile["default"]; got != string(scheduled.ContextProfileMinimal) {
		t.Fatalf("create context_profile default = %#v, want minimal", got)
	}
	updateDefinition := NewUpdate(nil).Definition()
	updateProfile := schemaProperty(t, updateDefinition.Parameters, "context_profile")
	if got := schemaStrings(t, updateProfile, "enum"); !equalStringSets(got, []string{
		string(scheduled.ContextProfileMinimal),
		string(scheduled.ContextProfileAgent),
	}) {
		t.Fatalf("update context_profile enum = %v", got)
	}
	for _, definition := range []agent.ToolDefinition{createDefinition, updateDefinition} {
		if !strings.Contains(strings.Join(definition.PromptGuidance, " "), "tool authority") {
			t.Fatalf("%s guidance does not separate context from tool authority", definition.Name)
		}
	}

	manager := newToolTestManager(t, 8)
	ctx := toolTestContext("chat-context")
	if _, err := NewCreate(manager).Run(
		ctx,
		`{"name":"Invalid context","prompt":"Fail.","kind":"recurring","cron":"0 * * * *","context_profile":"full","allowed_tools":[]}`,
	); err == nil || !strings.Contains(err.Error(), "context_profile must be") {
		t.Fatalf("Create.Run(invalid context profile) error = %v", err)
	}
	output, err := NewCreate(manager).Run(
		ctx,
		`{"name":"Context job","prompt":"Use agent context.","kind":"recurring","cron":"0 * * * *","context_profile":"agent","allowed_tools":[]}`,
	)
	if err != nil {
		t.Fatalf("Create.Run() error = %v", err)
	}
	if !strings.Contains(output, "context profile agent") {
		t.Fatalf("Create.Run() output = %q", output)
	}
	job := onlyToolTestJob(t, manager, "chat-context")
	if job.ContextProfile != scheduled.ContextProfileAgent {
		t.Fatalf("created ContextProfile = %q, want agent", job.ContextProfile)
	}

	output, err = NewUpdate(manager).Run(
		ctx,
		`{"id":"job-1","context_profile":"minimal"}`,
	)
	if err != nil {
		t.Fatalf("Update.Run() error = %v", err)
	}
	if !strings.Contains(output, "context profile minimal") {
		t.Fatalf("Update.Run() output = %q", output)
	}
	job = onlyToolTestJob(t, manager, "chat-context")
	if job.ContextProfile != scheduled.ContextProfileMinimal {
		t.Fatalf("updated ContextProfile = %q, want minimal", job.ContextProfile)
	}

	if _, err := NewUpdate(manager).Run(
		ctx,
		`{"id":"job-1","context_profile":"full"}`,
	); err == nil || !strings.Contains(err.Error(), "context_profile must be") {
		t.Fatalf("Update.Run(invalid context profile) error = %v", err)
	}
}

func TestToolsRequireOriginatingChat(t *testing.T) {
	t.Parallel()

	manager := newToolTestManager(t, 8)
	runAt := toolTestNow.Add(time.Hour).Format(time.RFC3339)
	tests := []struct {
		name string
		tool agent.Tool
		args string
	}{
		{
			name: "create",
			tool: NewCreate(manager),
			args: fmt.Sprintf(
				`{"name":"No owner","prompt":"Must fail.","kind":"oneshot","run_at":%q,"allowed_tools":[]}`,
				runAt,
			),
		},
		{name: "list", tool: NewList(manager), args: `{}`},
		{name: "runs", tool: NewRuns(manager), args: `{}`},
		{name: "update", tool: NewUpdate(manager), args: `{"id":"job-1","notify":false}`},
		{name: "delete", tool: NewDelete(manager), args: `{"id":"job-1"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.tool.Run(context.Background(), tt.args)
			if err == nil || !strings.Contains(err.Error(), "require an originating chat") {
				t.Fatalf("Run() error = %v, want missing-origin error", err)
			}
		})
	}
}

func TestListScopesOwnerAndShowsPerJobToolPolicy(t *testing.T) {
	t.Parallel()

	manager := newToolTestManager(t, 8)
	create := NewCreate(manager)
	runAt := toolTestNow.Add(time.Hour).Format(time.RFC3339)

	ownerOutput, err := create.Run(toolTestContext("chat-1"), fmt.Sprintf(
		`{"name":"Owned job","prompt":"Visible task summary.","kind":"oneshot","run_at":%q,"notify":false,"provider":"private","model":"model","max_turns":3,"allowed_tools":["read_file"]}`,
		runAt,
	))
	if err != nil {
		t.Fatalf("create owner job error = %v", err)
	}
	if !strings.Contains(ownerOutput, "job-1") {
		t.Fatalf("create owner output = %q", ownerOutput)
	}
	if _, err := create.Run(toolTestContext("chat-2"), fmt.Sprintf(
		`{"name":"Other chat secret","prompt":"Do not disclose.","kind":"oneshot","run_at":%q,"allowed_tools":["web_fetch"]}`,
		runAt,
	)); err != nil {
		t.Fatalf("create other job error = %v", err)
	}

	output, err := NewList(manager).Run(toolTestContext("chat-1"), `{}`)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for _, want := range []string{
		"job-1",
		"Owned job",
		"minimal",
		"read_file",
		"Visible task summary.",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("Run() output missing %q: %s", want, output)
		}
	}
	for _, sensitive := range []string{
		"job-2",
		"Other chat secret",
		"Do not disclose.",
		"web_fetch",
		"private/model",
		"private",
		"model_ref",
		"max_turns",
		"notify",
		"chat-1",
		"telegram",
	} {
		if strings.Contains(output, sensitive) {
			t.Fatalf("Run() output disclosed %q: %s", sensitive, output)
		}
	}
}

func TestCreateAndUpdateRequireProviderModelPair(t *testing.T) {
	t.Parallel()

	manager := newToolTestManager(t, 8)
	ctx := toolTestContext("chat-1")
	runAt := toolTestNow.Add(time.Hour).Format(time.RFC3339)
	create := NewCreate(manager)

	for _, args := range []string{
		fmt.Sprintf(
			`{"name":"Missing provider","prompt":"Fail.","kind":"oneshot","run_at":%q,"model":"model","allowed_tools":[]}`,
			runAt,
		),
		fmt.Sprintf(
			`{"name":"Missing model","prompt":"Fail.","kind":"oneshot","run_at":%q,"provider":"provider","allowed_tools":[]}`,
			runAt,
		),
	} {
		if _, err := create.Run(ctx, args); err == nil ||
			!strings.Contains(err.Error(), "must be supplied together") {
			t.Fatalf("Create.Run() error = %v, want provider/model pair error", err)
		}
	}

	if _, err := create.Run(ctx, fmt.Sprintf(
		`{"name":"Exact target","prompt":"Run.","kind":"oneshot","run_at":%q,"provider":"provider","model":"model","allowed_tools":[]}`,
		runAt,
	)); err != nil {
		t.Fatalf("Create.Run() exact target error = %v", err)
	}
	job := onlyToolTestJob(t, manager, "chat-1")
	if job.Model != (scheduled.ModelTarget{Provider: "provider", Ref: "model"}) {
		t.Fatalf("created Model = %#v", job.Model)
	}

	if _, err := NewUpdate(manager).Run(
		ctx,
		`{"id":"job-1","provider":"other"}`,
	); err == nil || !strings.Contains(err.Error(), "must be supplied together") {
		t.Fatalf("Update.Run() error = %v, want provider/model pair error", err)
	}
	if _, err := NewUpdate(manager).Run(
		ctx,
		`{"id":"job-1","provider":"other","model":"replacement"}`,
	); err != nil {
		t.Fatalf("Update.Run() exact target error = %v", err)
	}
	job = onlyToolTestJob(t, manager, "chat-1")
	if job.Model != (scheduled.ModelTarget{Provider: "other", Ref: "replacement"}) {
		t.Fatalf("updated Model = %#v", job.Model)
	}
}

func TestUpdatePreservesFalsePointerAndReplacesTiming(t *testing.T) {
	t.Parallel()

	manager := newToolTestManager(t, 8)
	ctx := toolTestContext("chat-1")
	createOutput, err := NewCreate(manager).Run(
		ctx,
		`{"name":"Recurring","prompt":"Run often.","kind":"recurring","cron":"*/5 * * * *","allowed_tools":[]}`,
	)
	if err != nil {
		t.Fatalf("create Run() error = %v", err)
	}
	if !strings.Contains(createOutput, "job-1") {
		t.Fatalf("create output = %q", createOutput)
	}

	if _, err := NewUpdate(manager).Run(ctx, `{"id":"job-1","notify":false}`); err != nil {
		t.Fatalf("notify update Run() error = %v", err)
	}
	job := onlyToolTestJob(t, manager, "chat-1")
	if job.Notify {
		t.Fatal("Notify = true after explicit false update")
	}
	if job.Kind != scheduled.KindRecurring || job.Cron != "*/5 * * * *" ||
		job.NextFire.IsZero() {
		t.Fatalf("notify-only update changed timing: %#v", job)
	}

	runAt := toolTestNow.Add(3 * time.Hour)
	if _, err := NewUpdate(manager).Run(ctx, fmt.Sprintf(
		`{"id":"job-1","kind":"oneshot","run_at":%q}`,
		runAt.Format(time.RFC3339),
	)); err != nil {
		t.Fatalf("timing update Run() error = %v", err)
	}
	job = onlyToolTestJob(t, manager, "chat-1")
	if job.Kind != scheduled.KindOneShot || !job.RunAt.Equal(runAt) ||
		!job.NextFire.Equal(runAt) || job.Cron != "" {
		t.Fatalf("timing update job = %#v", job)
	}
	if job.Notify {
		t.Fatal("timing update lost prior Notify=false")
	}

	_, err = NewUpdate(manager).Run(
		ctx,
		`{"id":"job-1","run_at":"2026-07-18T16:00:00Z","cron":"0 * * * *"}`,
	)
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("conflicting timing error = %v, want mutually-exclusive error", err)
	}
}

func TestDeleteIsIdempotentAndOwnerScoped(t *testing.T) {
	t.Parallel()

	manager := newToolTestManager(t, 8)
	ownerCtx := toolTestContext("chat-1")
	otherCtx := toolTestContext("chat-2")
	runAt := toolTestNow.Add(time.Hour).Format(time.RFC3339)
	if _, err := NewCreate(manager).Run(ownerCtx, fmt.Sprintf(
		`{"name":"Owned","prompt":"Delete me.","kind":"oneshot","run_at":%q,"allowed_tools":[]}`,
		runAt,
	)); err != nil {
		t.Fatalf("create Run() error = %v", err)
	}

	tool := NewDelete(manager)
	if _, err := tool.Run(otherCtx, `{"id":"job-1"}`); !errors.Is(err, scheduled.ErrNotFound) {
		t.Fatalf("cross-owner delete error = %v, want %v", err, scheduled.ErrNotFound)
	}
	if got := onlyToolTestJob(t, manager, "chat-1").ID; got != "job-1" {
		t.Fatalf("job after cross-owner delete = %q, want job-1", got)
	}

	output, err := tool.Run(ownerCtx, `{"id":"job-1"}`)
	if err != nil {
		t.Fatalf("delete Run() error = %v", err)
	}
	if !strings.Contains(output, "Deleted scheduled job job-1") {
		t.Fatalf("delete output = %q", output)
	}

	output, err = tool.Run(ownerCtx, `{"id":"job-1"}`)
	if err != nil {
		t.Fatalf("idempotent delete Run() error = %v", err)
	}
	if !strings.Contains(output, "already absent is also success") {
		t.Fatalf("idempotent delete output = %q", output)
	}
}

func onlyToolTestJob(
	t *testing.T,
	manager *scheduled.Manager,
	chatID string,
) scheduled.Job {
	t.Helper()

	jobs, err := manager.List(context.Background(), scheduled.Owner{
		Channel: bus.ChannelTelegram,
		ChatID:  chatID,
		UserID:  "42",
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("List() jobs = %d, want 1", len(jobs))
	}
	return jobs[0]
}

func schemaStringKeys(t *testing.T, schema map[string]any, key string) []string {
	t.Helper()

	values, ok := schema[key].(map[string]any)
	if !ok {
		t.Fatalf("schema[%q] = %#v, want map[string]any", key, schema[key])
	}
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func schemaProperty(t *testing.T, schema map[string]any, name string) map[string]any {
	t.Helper()

	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema properties = %#v, want map[string]any", schema["properties"])
	}
	property, ok := properties[name].(map[string]any)
	if !ok {
		t.Fatalf("schema property %q = %#v, want map[string]any", name, properties[name])
	}
	return property
}

func schemaStrings(t *testing.T, schema map[string]any, key string) []string {
	t.Helper()

	raw, ok := schema[key]
	if !ok {
		return nil
	}
	values, ok := raw.([]string)
	if !ok {
		t.Fatalf("schema[%q] = %#v, want []string", key, raw)
	}
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}

func equalStringSets(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	got = append([]string(nil), got...)
	want = append([]string(nil), want...)
	sort.Strings(got)
	sort.Strings(want)
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
