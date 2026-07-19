package schedule

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/q15co/q15/systems/agent/internal/bus"
)

var (
	testNow          = time.Date(2026, time.July, 18, 12, 0, 30, 0, time.UTC)
	testOwner        = Owner{Channel: "telegram", ChatID: "chat-1", UserID: "42"}
	testDefaultModel = ModelTarget{Provider: "default", Ref: "model"}
	testPinnedModel  = ModelTarget{Provider: "pinned", Ref: "model"}
)

type managerTestStore struct {
	mu           sync.Mutex
	jobs         map[string]Job
	records      []RunRecord
	history      RunHistory
	lastRunQuery RunQuery
}

func newManagerTestStore(jobs ...Job) *managerTestStore {
	store := &managerTestStore{jobs: make(map[string]Job, len(jobs))}
	for _, job := range jobs {
		store.jobs[job.ID] = job
	}
	return store
}

func (s *managerTestStore) LoadJobs(context.Context) ([]Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Job, 0, len(s.jobs))
	for _, job := range s.jobs {
		out = append(out, job)
	}
	return out, nil
}

func (s *managerTestStore) StoreJob(_ context.Context, job Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs[job.ID] = job
	return nil
}

func (s *managerTestStore) DeleteJob(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.jobs, id)
	return nil
}

func (s *managerTestStore) AppendRunRecord(_ context.Context, record RunRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, record)
	return nil
}

func (s *managerTestStore) QueryRuns(_ context.Context, query RunQuery) (RunHistory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastRunQuery = query
	return s.history, nil
}

func (s *managerTestStore) job(id string) (Job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[id]
	return job, ok
}

func (s *managerTestStore) runRecords() []RunRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]RunRecord(nil), s.records...)
}

func (s *managerTestStore) runQuery() RunQuery {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastRunQuery
}

type managerTestExecutor struct {
	execute func(context.Context, RunRequest) (RunResult, error)
}

func (e managerTestExecutor) Execute(
	ctx context.Context,
	request RunRequest,
) (RunResult, error) {
	if e.execute == nil {
		return RunResult{Text: "done", Model: request.Job.Model}, nil
	}
	return e.execute(ctx, request)
}

type managerTestPublisher struct {
	mu       sync.Mutex
	messages []bus.OutboundMessage
	err      error
}

type managerTestDeliveryRecorder struct {
	mu         sync.Mutex
	deliveries []DeliveredNotification
	err        error
}

func (r *managerTestDeliveryRecorder) RecordDeliveredNotification(
	_ context.Context,
	delivery DeliveredNotification,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deliveries = append(r.deliveries, delivery)
	return r.err
}

func (r *managerTestDeliveryRecorder) recorded() []DeliveredNotification {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]DeliveredNotification(nil), r.deliveries...)
}

func (p *managerTestPublisher) PublishOutboundAndWaitForDelivery(
	_ context.Context,
	msg bus.OutboundMessage,
) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.messages = append(p.messages, msg)
	return p.err
}

func (p *managerTestPublisher) published() []bus.OutboundMessage {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]bus.OutboundMessage(nil), p.messages...)
}

func newManagerForTest(
	t *testing.T,
	store *managerTestStore,
	executor Executor,
	publisher Publisher,
	opts ...func(*Config),
) *Manager {
	t.Helper()
	cfg := managerConfigForTest(store, executor, publisher)
	for _, opt := range opts {
		opt(&cfg)
	}
	manager, err := NewManager(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	return manager
}

func managerConfigForTest(
	store *managerTestStore,
	executor Executor,
	publisher Publisher,
) Config {
	return Config{
		Store:            store,
		Executor:         executor,
		Publisher:        publisher,
		DeliveryRecorder: &managerTestDeliveryRecorder{},
		MaxJobs:          64,
		MaxTurns:         16,
		RunTimeout:       time.Second,
		AllowedUserIDs:   []int64{42},
		DefaultModel: func() ModelTarget {
			return testDefaultModel
		},
		ModelExists: func(model ModelTarget) bool {
			return model == testDefaultModel || model == testPinnedModel
		},
		ToolExists: func(name string) bool {
			return name == "read_file" || name == "web_fetch"
		},
		Now: func() time.Time {
			return testNow
		},
		NewID: func() (string, error) {
			return "job-test", nil
		},
	}
}

func TestManagerCreateValidatesOwnerLimitsAndModel(t *testing.T) {
	store := newManagerTestStore()
	manager := newManagerForTest(t, store, managerTestExecutor{}, &managerTestPublisher{},
		func(cfg *Config) {
			cfg.MaxJobs = 1
			cfg.MaxTurns = 8
		},
	)

	job, err := manager.Create(context.Background(), testOwner, CreateRequest{
		Name:     "Morning review",
		Prompt:   "Review the workspace.",
		Kind:     KindOneShot,
		RunAt:    testNow.Add(time.Hour),
		Notify:   true,
		MaxTurns: 4,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if job.ID != "job-test" || job.Owner != testOwner {
		t.Fatalf("created job identity = %#v", job)
	}
	if job.Model != testDefaultModel {
		t.Fatalf("Model = %#v, want %#v", job.Model, testDefaultModel)
	}
	if job.ContextProfile != ContextProfileMinimal {
		t.Fatalf("ContextProfile = %q, want %q", job.ContextProfile, ContextProfileMinimal)
	}
	if !job.NextFire.Equal(testNow.Add(time.Hour)) {
		t.Fatalf("NextFire = %s", job.NextFire)
	}

	if _, err := manager.Create(context.Background(), testOwner, CreateRequest{
		Name:   "Second",
		Prompt: "Exceeds the configured limit.",
		Kind:   KindRecurring,
		Cron:   "* * * * *",
	}); !errors.Is(err, ErrMaxJobsReached) {
		t.Fatalf("Create() max-jobs error = %v, want %v", err, ErrMaxJobsReached)
	}

	unauthorized := Owner{Channel: "telegram", ChatID: "chat-1", UserID: "99"}
	if _, err := manager.Create(context.Background(), unauthorized, CreateRequest{
		Name:   "Unauthorized",
		Prompt: "Must fail.",
		Kind:   KindRecurring,
		Cron:   "* * * * *",
	}); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("Create() unauthorized error = %v", err)
	}

	otherManager := newManagerForTest(
		t,
		newManagerTestStore(),
		managerTestExecutor{},
		&managerTestPublisher{},
	)
	if _, err := otherManager.Create(context.Background(), testOwner, CreateRequest{
		Name:   "Unknown model",
		Prompt: "Must fail.",
		Kind:   KindRecurring,
		Cron:   "* * * * *",
		Model:  ModelTarget{Provider: "missing", Ref: testDefaultModel.Ref},
	}); err == nil || !strings.Contains(err.Error(), "unknown scheduled job model") {
		t.Fatalf("Create() unknown-model error = %v", err)
	}
	if _, err := otherManager.Create(context.Background(), testOwner, CreateRequest{
		Name:   "Bare model ref",
		Prompt: "Must fail.",
		Kind:   KindRecurring,
		Cron:   "* * * * *",
		Model:  ModelTarget{Ref: testDefaultModel.Ref},
	}); err == nil || !strings.Contains(err.Error(), "model provider is required") {
		t.Fatalf("Create() bare-model-ref error = %v", err)
	}
}

func TestManagerAllowedToolsAreNormalizedValidatedAndAtomicallyUpdated(t *testing.T) {
	store := newManagerTestStore()
	manager := newManagerForTest(t, store, managerTestExecutor{}, &managerTestPublisher{})
	requested := []string{" read_file ", "web_fetch", "read_file", ""}
	created, err := manager.Create(context.Background(), testOwner, CreateRequest{
		Name:         "Tool policy",
		Prompt:       "Read and fetch.",
		Kind:         KindRecurring,
		Cron:         "*/5 * * * *",
		AllowedTools: requested,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if got, want := created.AllowedTools, []string{"read_file", "web_fetch"}; !slices.Equal(
		got,
		want,
	) {
		t.Fatalf("AllowedTools = %#v, want %#v", got, want)
	}

	requested[0] = "mutated"
	created.AllowedTools[0] = "mutated"
	listed, err := manager.List(context.Background(), testOwner)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if got, want := listed[0].AllowedTools, []string{"read_file", "web_fetch"}; !slices.Equal(
		got,
		want,
	) {
		t.Fatalf("persisted AllowedTools = %#v, want %#v", got, want)
	}
	listed[0].AllowedTools[0] = "mutated"
	relisted, err := manager.List(context.Background(), testOwner)
	if err != nil {
		t.Fatalf("second List() error = %v", err)
	}
	if relisted[0].AllowedTools[0] != "read_file" {
		t.Fatalf("List() leaked mutable AllowedTools backing array: %#v", relisted[0].AllowedTools)
	}

	unknown := []string{"exec"}
	if _, err := manager.Update(
		context.Background(),
		testOwner,
		created.ID,
		UpdateRequest{AllowedTools: &unknown},
	); err == nil || !strings.Contains(err.Error(), `unknown scheduled job tool "exec"`) {
		t.Fatalf("Update(unknown tool) error = %v", err)
	}
	persisted, _ := store.job(created.ID)
	if got, want := persisted.AllowedTools, []string{"read_file", "web_fetch"}; !slices.Equal(
		got,
		want,
	) {
		t.Fatalf("failed update changed AllowedTools to %#v, want %#v", got, want)
	}

	none := []string{}
	updated, err := manager.Update(
		context.Background(),
		testOwner,
		created.ID,
		UpdateRequest{AllowedTools: &none},
	)
	if err != nil {
		t.Fatalf("Update(clear tools) error = %v", err)
	}
	if len(updated.AllowedTools) != 0 {
		t.Fatalf("AllowedTools after clear = %#v, want empty", updated.AllowedTools)
	}

	if _, err := manager.Create(context.Background(), testOwner, CreateRequest{
		Name:         "Unknown tool",
		Prompt:       "Must fail.",
		Kind:         KindRecurring,
		Cron:         "*/5 * * * *",
		AllowedTools: []string{"missing"},
	}); err == nil || !strings.Contains(err.Error(), `unknown scheduled job tool "missing"`) {
		t.Fatalf("Create(unknown tool) error = %v", err)
	}
}

func TestNewManagerRequiresToolCatalogAndRejectsMalformedStoredToolPolicy(t *testing.T) {
	cfg := managerConfigForTest(
		newManagerTestStore(),
		managerTestExecutor{},
		&managerTestPublisher{},
	)
	cfg.ToolExists = nil
	if _, err := NewManager(context.Background(), cfg); err == nil ||
		!strings.Contains(err.Error(), "tool catalog is required") {
		t.Fatalf("NewManager(no tool catalog) error = %v", err)
	}

	tests := []struct {
		name  string
		tools []string
		want  string
	}{
		{
			name:  "duplicate",
			tools: []string{"read_file", "read_file"},
			want:  "allowed_tools must contain unique",
		},
		{
			name:  "whitespace",
			tools: []string{" read_file "},
			want:  "allowed_tools entries must not have surrounding whitespace",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			job := persistedRecurring("job-invalid-tools", testNow.Add(time.Minute))
			job.AllowedTools = tt.tools
			cfg := managerConfigForTest(
				newManagerTestStore(job),
				managerTestExecutor{},
				&managerTestPublisher{},
			)
			if _, err := NewManager(context.Background(), cfg); err == nil ||
				!strings.Contains(err.Error(), tt.want) {
				t.Fatalf("NewManager() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestNewManagerKeepsJobWhenAllowedToolIsCurrentlyUnavailable(t *testing.T) {
	job := persistedRecurring("job-unavailable-tool", testNow.Add(time.Minute))
	job.AllowedTools = []string{"exec"}
	store := newManagerTestStore(job)

	manager := newManagerForTest(t, store, managerTestExecutor{}, &managerTestPublisher{})

	jobs, err := manager.List(context.Background(), testOwner)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(jobs) != 1 || !slices.Equal(jobs[0].AllowedTools, []string{"exec"}) {
		t.Fatalf("loaded jobs = %#v, want unavailable tool policy preserved", jobs)
	}
}

func TestNewManagerPersistsExplicitEmptyToolPolicyForExistingJob(t *testing.T) {
	job := persistedRecurring("job-missing-tool-policy", testNow.Add(time.Minute))
	job.AllowedTools = nil
	store := newManagerTestStore(job)

	newManagerForTest(t, store, managerTestExecutor{}, &managerTestPublisher{})

	persisted, ok := store.job(job.ID)
	if !ok {
		t.Fatalf("persisted job %q is missing", job.ID)
	}
	if persisted.AllowedTools == nil {
		t.Fatal("persisted AllowedTools = nil, want explicit empty list")
	}
}

func TestManagerRunHistoryInjectsOwnerAndValidatesFilters(t *testing.T) {
	store := newManagerTestStore()
	store.history = RunHistory{
		Matched:      3,
		StatusCounts: map[Status]int{StatusSucceeded: 3},
		Runs: []RunRecord{{
			ID:     "run-3",
			JobID:  "job-1",
			Owner:  testOwner,
			Status: StatusSucceeded,
		}},
	}
	manager := newManagerForTest(t, store, managerTestExecutor{}, &managerTestPublisher{})
	since := testNow.Add(-time.Hour)
	before := testNow

	history, err := manager.RunHistory(context.Background(), testOwner, RunFilter{
		JobID:    " job-1 ",
		Statuses: []Status{StatusSucceeded, StatusSucceeded},
		Since:    since,
		Before:   before,
	})
	if err != nil {
		t.Fatalf("RunHistory() error = %v", err)
	}
	if history.Matched != 3 || len(history.Runs) != 1 {
		t.Fatalf("RunHistory() = %#v", history)
	}
	query := store.runQuery()
	if query.Owner != testOwner {
		t.Fatalf("QueryRuns owner = %#v, want %#v", query.Owner, testOwner)
	}
	if query.Filter.JobID != "job-1" || query.Filter.Limit != defaultRunHistoryLimit {
		t.Fatalf("QueryRuns filter = %#v", query.Filter)
	}
	if len(query.Filter.Statuses) != 1 ||
		query.Filter.Statuses[0] != StatusSucceeded {
		t.Fatalf("QueryRuns statuses = %#v", query.Filter.Statuses)
	}

	tests := []struct {
		name   string
		filter RunFilter
	}{
		{name: "negative limit", filter: RunFilter{Limit: -1}},
		{name: "excessive limit", filter: RunFilter{Limit: maxRunHistoryLimit + 1}},
		{name: "nonterminal status", filter: RunFilter{Statuses: []Status{StatusRunning}}},
		{name: "unknown status", filter: RunFilter{Statuses: []Status{"mystery"}}},
		{name: "empty time window", filter: RunFilter{Since: testNow, Before: testNow}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := manager.RunHistory(
				context.Background(),
				testOwner,
				tt.filter,
			); err == nil {
				t.Fatal("RunHistory() error = nil, want non-nil")
			}
		})
	}
}

func TestManagerContextProfileUpdatesAndValidates(t *testing.T) {
	store := newManagerTestStore()
	manager := newManagerForTest(t, store, managerTestExecutor{}, &managerTestPublisher{})
	created, err := manager.Create(context.Background(), testOwner, CreateRequest{
		Name:           "Agent context",
		Prompt:         "Use stable agent context.",
		ContextProfile: ContextProfileAgent,
		Kind:           KindRecurring,
		Cron:           "*/5 * * * *",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.ContextProfile != ContextProfileAgent {
		t.Fatalf("ContextProfile = %q, want %q", created.ContextProfile, ContextProfileAgent)
	}

	minimal := ContextProfileMinimal
	updated, err := manager.Update(
		context.Background(),
		testOwner,
		created.ID,
		UpdateRequest{ContextProfile: &minimal},
	)
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if updated.ContextProfile != ContextProfileMinimal {
		t.Fatalf("updated ContextProfile = %q, want %q", updated.ContextProfile, minimal)
	}

	invalid := ContextProfile("full")
	if _, err := manager.Update(
		context.Background(),
		testOwner,
		created.ID,
		UpdateRequest{ContextProfile: &invalid},
	); err == nil || !strings.Contains(err.Error(), "context_profile must be") {
		t.Fatalf("Update(invalid context profile) error = %v", err)
	}
	persisted, _ := store.job(created.ID)
	if persisted.ContextProfile != ContextProfileMinimal {
		t.Fatalf("invalid update changed ContextProfile to %q", persisted.ContextProfile)
	}
}

func TestNewManagerRejectsInvalidStoredContextProfile(t *testing.T) {
	job := persistedRecurring("job-invalid-context", testNow.Add(time.Minute))
	job.ContextProfile = ContextProfile("full")
	cfg := managerConfigForTest(
		newManagerTestStore(job),
		managerTestExecutor{},
		&managerTestPublisher{},
	)

	if _, err := NewManager(context.Background(), cfg); err == nil ||
		!strings.Contains(err.Error(), "context_profile must be") {
		t.Fatalf("NewManager() invalid-context error = %v", err)
	}
}

func TestNewManagerBoundsUnavailableRetryDelay(t *testing.T) {
	for _, delay := range []time.Duration{
		minUnavailableRetryDelay - time.Nanosecond,
		maxUnavailableRetryDelay + time.Nanosecond,
	} {
		cfg := managerConfigForTest(
			newManagerTestStore(),
			managerTestExecutor{},
			&managerTestPublisher{},
		)
		cfg.UnavailableRetryDelay = delay
		if _, err := NewManager(context.Background(), cfg); err == nil ||
			!strings.Contains(err.Error(), "retry delay must be between") {
			t.Fatalf("NewManager(UnavailableRetryDelay=%s) error = %v", delay, err)
		}
	}
}

func TestManagerCRUDEnforcesExactOwnerAndStableIdentity(t *testing.T) {
	store := newManagerTestStore()
	manager := newManagerForTest(t, store, managerTestExecutor{}, &managerTestPublisher{})
	created, err := manager.Create(context.Background(), testOwner, CreateRequest{
		Name:   "Original",
		Prompt: "Original prompt.",
		Kind:   KindRecurring,
		Cron:   "*/5 * * * *",
		Model:  testPinnedModel,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	wrongOwner := Owner{Channel: "telegram", ChatID: "chat-2", UserID: "42"}
	if jobs, err := manager.List(context.Background(), wrongOwner); err != nil || len(jobs) != 0 {
		t.Fatalf("List(wrong owner) = %#v, %v", jobs, err)
	}
	name := "Updated"
	prompt := "Updated prompt."
	updated, err := manager.Update(context.Background(), testOwner, created.ID, UpdateRequest{
		Name:   &name,
		Prompt: &prompt,
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if updated.ID != created.ID || updated.Owner != created.Owner ||
		!updated.CreatedAt.Equal(created.CreatedAt) {
		t.Fatalf("Update() changed immutable identity: before=%#v after=%#v", created, updated)
	}
	if updated.ContextProfile != created.ContextProfile {
		t.Fatalf(
			"Update() ContextProfile = %q, want preserved %q",
			updated.ContextProfile,
			created.ContextProfile,
		)
	}
	if updated.Name != name || updated.Prompt != prompt {
		t.Fatalf("Update() = %#v", updated)
	}
	if _, err := manager.Update(context.Background(), wrongOwner, created.ID, UpdateRequest{
		Name: &name,
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Update(wrong owner) error = %v, want %v", err, ErrNotFound)
	}
	if err := manager.Delete(context.Background(), wrongOwner, created.ID); !errors.Is(
		err,
		ErrNotFound,
	) {
		t.Fatalf("Delete(wrong owner) error = %v, want %v", err, ErrNotFound)
	}
	if err := manager.Delete(context.Background(), testOwner, created.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if err := manager.Delete(context.Background(), testOwner, created.ID); err != nil {
		t.Fatalf("Delete() idempotent error = %v", err)
	}
}

func TestManagerUpdateAllowsUnrelatedChangesWhileModelIsUnavailable(t *testing.T) {
	job := persistedRecurring("job-unavailable-update", testNow.Add(time.Minute))
	job.Model = ModelTarget{Provider: "offline", Ref: "model"}
	store := newManagerTestStore(job)
	availabilityChecks := 0
	manager := newManagerForTest(
		t,
		store,
		managerTestExecutor{},
		&managerTestPublisher{},
		func(cfg *Config) {
			cfg.ModelExists = func(ModelTarget) bool {
				availabilityChecks++
				return false
			}
		},
	)

	prompt := "Updated while the pinned target is absent."
	notify := !job.Notify
	updated, err := manager.Update(
		context.Background(),
		testOwner,
		job.ID,
		UpdateRequest{
			Prompt: &prompt,
			Notify: &notify,
		},
	)
	if err != nil {
		t.Fatalf("Update(unrelated fields) error = %v", err)
	}
	if availabilityChecks != 0 {
		t.Fatalf("live availability checks = %d, want 0 for unrelated update", availabilityChecks)
	}
	if updated.Prompt != prompt || updated.Notify != notify || updated.Model != job.Model {
		t.Fatalf("Update(unrelated fields) = %#v", updated)
	}

	replacement := testPinnedModel
	if _, err := manager.Update(
		context.Background(),
		testOwner,
		job.ID,
		UpdateRequest{Model: &replacement},
	); err == nil || !strings.Contains(err.Error(), "unknown scheduled job model") {
		t.Fatalf("Update(explicit unavailable model) error = %v", err)
	}
	if availabilityChecks != 1 {
		t.Fatalf(
			"live availability checks = %d, want 1 after explicit model update",
			availabilityChecks,
		)
	}
}

func TestManagerRunOverdueOneShotClaimsBeforeExecuteAndDeletes(t *testing.T) {
	job := persistedOneShot("job-overdue", testNow.Add(-time.Hour), true)
	job.AllowedTools = []string{"read_file"}
	store := newManagerTestStore(job)
	publisher := &managerTestPublisher{}
	executed := make(chan struct{}, 1)
	manager := newManagerForTest(t, store, managerTestExecutor{
		execute: func(_ context.Context, request RunRequest) (RunResult, error) {
			got := request.Job
			claimed, ok := store.job(got.ID)
			if !ok || !claimed.Done || !claimed.NextFire.IsZero() ||
				claimed.LastStatus != StatusRunning {
				t.Errorf("job was not durably claimed before execution: %#v ok=%v", claimed, ok)
			}
			if request.RunID != runID(got.ID, got.LastRunAt) ||
				!request.ScheduledFor.Equal(got.RunAt) ||
				!request.StartedAt.Equal(got.LastRunAt) ||
				!request.CurrentTimeUTC.Equal(testNow) ||
				!slices.Equal(request.Job.AllowedTools, []string{"read_file"}) {
				t.Errorf("trusted run request = %#v", request)
			}
			executed <- struct{}{}
			return RunResult{Text: "scheduled output", Model: got.Model}, nil
		},
	}, publisher)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()

	waitChannel(t, executed)
	waitForManagerTest(t, func() bool {
		return len(store.runRecords()) == 1
	})
	cancel()
	if err := waitManagerDone(t, done); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if _, ok := store.job(job.ID); ok {
		t.Fatal("completed oneshot job still persisted")
	}
	records := store.runRecords()
	if records[0].Status != StatusSucceeded ||
		!records[0].ScheduledFor.Equal(job.RunAt) ||
		records[0].Output != "scheduled output" ||
		!slices.Equal(records[0].AllowedTools, []string{"read_file"}) {
		t.Fatalf("run record = %#v", records[0])
	}
	messages := publisher.published()
	if len(messages) != 1 || messages[0].Channel != testOwner.Channel ||
		messages[0].ChatID != testOwner.ChatID {
		t.Fatalf("notifications = %#v", messages)
	}
	wantNotification := strings.Join([]string{
		"⏰ One shot",
		"",
		"scheduled output",
		"",
		"Scheduled for 18 Jul 2026, 11:00 UTC",
	}, "\n")
	if messages[0].Text != wantNotification {
		t.Fatalf("notification = %q, want %q", messages[0].Text, wantNotification)
	}
}

func TestNotificationTextIsHumanFirstAndKeepsScheduleContext(t *testing.T) {
	job := persistedRecurring("job-long-internal-id", testNow.Add(time.Minute))
	job.Name = "joburg-weather-hourly"
	record := RunRecord{
		Status:       StatusSucceeded,
		ScheduledFor: testNow.Truncate(time.Minute),
		Output:       "Everything is on track.",
	}

	got := notificationText(job, record)
	want := strings.Join([]string{
		"⏰ Joburg weather hourly",
		"",
		"Everything is on track.",
		"",
		"Scheduled for 18 Jul 2026, 12:00 UTC",
		"Next run 18 Jul 2026, 12:01 UTC",
	}, "\n")
	if got != want {
		t.Fatalf("notificationText() = %q, want %q", got, want)
	}
	if strings.Contains(got, job.ID) {
		t.Fatalf("notification exposes internal job id: %q", got)
	}

	record.Status = StatusFailed
	record.Output = ""
	record.Error = "provider is temporarily unavailable"
	got = notificationText(job, record)
	want = strings.Join([]string{
		"⚠️ Joburg weather hourly failed",
		"",
		"provider is temporarily unavailable",
		"",
		"Scheduled for 18 Jul 2026, 12:00 UTC",
		"Next run 18 Jul 2026, 12:01 UTC",
	}, "\n")
	if got != want {
		t.Fatalf("failure notificationText() = %q, want %q", got, want)
	}

	job.Name = "  Morning \n Briefing  "
	if got := notificationDisplayName(job.Name); got != "Morning Briefing" {
		t.Fatalf("notificationDisplayName() = %q, want existing casing preserved", got)
	}
}

func TestManagerRunRecurringCoalescesMissedOccurrences(t *testing.T) {
	job := persistedRecurring("job-recurring", testNow.Add(-5*time.Minute))
	store := newManagerTestStore(job)
	executed := make(chan struct{}, 1)
	manager := newManagerForTest(t, store, managerTestExecutor{
		execute: func(_ context.Context, request RunRequest) (RunResult, error) {
			executed <- struct{}{}
			return RunResult{Text: "ok", Model: request.Job.Model}, nil
		},
	}, &managerTestPublisher{})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()
	waitChannel(t, executed)
	waitForManagerTest(t, func() bool {
		return len(store.runRecords()) == 1
	})
	cancel()
	if err := waitManagerDone(t, done); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	record := store.runRecords()[0]
	wantScheduled := testNow.Truncate(time.Minute)
	if !record.ScheduledFor.Equal(wantScheduled) {
		t.Fatalf("ScheduledFor = %s, want latest missed %s", record.ScheduledFor, wantScheduled)
	}
	persisted, ok := store.job(job.ID)
	if !ok {
		t.Fatal("recurring job was deleted")
	}
	wantNext := testNow.Truncate(time.Minute).Add(time.Minute)
	if !persisted.NextFire.Equal(wantNext) {
		t.Fatalf("NextFire = %s, want %s", persisted.NextFire, wantNext)
	}
	if persisted.LastStatus != StatusSucceeded || persisted.ConsecutiveFailures != 0 {
		t.Fatalf("recurring job state = %#v", persisted)
	}
}

func TestManagerNotificationUsesPostRunNextFire(t *testing.T) {
	now := testNow
	job := persistedRecurring("job-crosses-boundary", now.Add(-time.Minute))
	job.Notify = true
	store := newManagerTestStore(job)
	publisher := &managerTestPublisher{}
	manager := newManagerForTest(t, store, managerTestExecutor{
		execute: func(_ context.Context, request RunRequest) (RunResult, error) {
			now = testNow.Add(2 * time.Minute)
			return RunResult{Text: "Late result", Model: request.Job.Model}, nil
		},
	}, publisher, func(cfg *Config) {
		cfg.Now = func() time.Time { return now }
	})

	runNextDue(t, manager)

	messages := publisher.published()
	if len(messages) != 1 {
		t.Fatalf("notifications = %#v, want one", messages)
	}
	if !strings.Contains(messages[0].Text, "Next run 18 Jul 2026, 12:03 UTC") {
		t.Fatalf("notification has stale next run: %q", messages[0].Text)
	}
	if strings.Contains(messages[0].Text, "Next run 18 Jul 2026, 12:01 UTC") {
		t.Fatalf("notification advertises a past next run: %q", messages[0].Text)
	}
}

func TestManagerRunTimeoutPersistsFailureAndAdvancesRecurringJob(t *testing.T) {
	job := persistedRecurring("job-timeout", testNow.Add(-time.Minute))
	store := newManagerTestStore(job)
	manager := newManagerForTest(t, store, managerTestExecutor{
		execute: func(ctx context.Context, _ RunRequest) (RunResult, error) {
			<-ctx.Done()
			return RunResult{}, ctx.Err()
		},
	}, &managerTestPublisher{}, func(cfg *Config) {
		cfg.RunTimeout = 20 * time.Millisecond
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()
	waitForManagerTest(t, func() bool {
		return len(store.runRecords()) == 1
	})
	cancel()
	if err := waitManagerDone(t, done); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	record := store.runRecords()[0]
	if record.Status != StatusFailed || !strings.Contains(record.Error, "timed out") {
		t.Fatalf("timeout run record = %#v", record)
	}
	if record.Model != job.Model {
		t.Fatalf("timeout run model = %#v, want %#v", record.Model, job.Model)
	}
	persisted, ok := store.job(job.ID)
	if !ok {
		t.Fatal("recurring timeout job was deleted")
	}
	if persisted.LastStatus != StatusFailed || persisted.ConsecutiveFailures != 1 ||
		persisted.LastFailureAt.IsZero() || persisted.NextFire.IsZero() {
		t.Fatalf("timeout job state = %#v", persisted)
	}
}

func TestManagerRejectsExecutorModelFallback(t *testing.T) {
	job := persistedRecurring("job-no-fallback", testNow.Add(-time.Minute))
	store := newManagerTestStore(job)
	manager := newManagerForTest(t, store, managerTestExecutor{
		execute: func(_ context.Context, _ RunRequest) (RunResult, error) {
			return RunResult{
				Text:  "output from the wrong model",
				Model: testPinnedModel,
			}, nil
		},
	}, &managerTestPublisher{})

	runNextDue(t, manager)

	records := store.runRecords()
	if len(records) != 1 || records[0].Status != StatusFailed ||
		!strings.Contains(records[0].Error, "instead of exact target") {
		t.Fatalf("fallback run records = %#v", records)
	}
	persisted, ok := store.job(job.ID)
	if !ok {
		t.Fatal("recurring job was deleted after rejected fallback")
	}
	if persisted.LastStatus != StatusFailed ||
		persisted.ModelUnavailableNotified {
		t.Fatalf("fallback job state = %#v", persisted)
	}
}

func TestManagerUnavailableOneShotRemainsActiveForExactTargetRetry(t *testing.T) {
	job := persistedOneShot("job-model-retry", testNow.Add(-time.Minute), true)
	store := newManagerTestStore(job)
	publisher := &managerTestPublisher{}
	retryDelay := 2 * time.Minute
	manager := newManagerForTest(t, store, managerTestExecutor{
		execute: func(_ context.Context, request RunRequest) (RunResult, error) {
			got := request.Job
			if got.Model != testDefaultModel {
				t.Fatalf(
					"Execute() model = %#v, want exact target %#v",
					got.Model,
					testDefaultModel,
				)
			}
			return RunResult{Model: got.Model}, &ModelUnavailableError{
				Model: got.Model,
				Cause: errors.New("provider is offline"),
			}
		},
	}, publisher, func(cfg *Config) {
		cfg.UnavailableRetryDelay = retryDelay
	})

	runNextDue(t, manager)

	records := store.runRecords()
	if len(records) != 1 || records[0].Status != StatusModelUnavailable ||
		records[0].Model != testDefaultModel {
		t.Fatalf("unavailable run records = %#v", records)
	}
	persisted, ok := store.job(job.ID)
	if !ok {
		t.Fatal("unavailable oneshot job was deleted")
	}
	if persisted.Done || !persisted.NextFire.Equal(testNow.Add(retryDelay)) {
		t.Fatalf("unavailable oneshot retry state = %#v", persisted)
	}
	if persisted.LastStatus != StatusModelUnavailable ||
		!persisted.ModelUnavailableNotified ||
		persisted.ConsecutiveFailures != 1 ||
		persisted.PendingRun != nil {
		t.Fatalf("unavailable oneshot state = %#v", persisted)
	}
	messages := publisher.published()
	if len(messages) != 1 || !strings.Contains(messages[0].Text, "is unavailable") {
		t.Fatalf("unavailable notifications = %#v", messages)
	}
}

func TestManagerUnavailableNotificationLatchRequiresSuccessfulDelivery(t *testing.T) {
	job := persistedOneShot("job-model-notify-retry", testNow.Add(-time.Minute), true)
	store := newManagerTestStore(job)
	deliveryErr := errors.New("telegram unavailable")
	publisher := &managerTestPublisher{err: deliveryErr}
	manager := newManagerForTest(t, store, managerTestExecutor{
		execute: func(_ context.Context, request RunRequest) (RunResult, error) {
			got := request.Job
			return RunResult{Model: got.Model}, &ModelUnavailableError{
				Model: got.Model,
				Cause: errors.New("provider is offline"),
			}
		},
	}, publisher)

	runNextDue(t, manager)

	persisted, ok := store.job(job.ID)
	if !ok {
		t.Fatal("unavailable oneshot job was deleted")
	}
	if persisted.ModelUnavailableNotified {
		t.Fatalf("ModelUnavailableNotified = true after failed delivery")
	}
	records := store.runRecords()
	if len(records) != 1 || !strings.Contains(records[0].NotifyError, deliveryErr.Error()) {
		t.Fatalf("unavailable run records = %#v", records)
	}
}

func TestManagerRecurringUnavailableAdvancesAndNotifiesOnTransitions(t *testing.T) {
	now := testNow
	job := persistedRecurring("job-model-transitions", now.Add(-time.Minute))
	job.Notify = true
	store := newManagerTestStore(job)
	publisher := &managerTestPublisher{}
	attempt := 0
	manager := newManagerForTest(t, store, managerTestExecutor{
		execute: func(_ context.Context, request RunRequest) (RunResult, error) {
			got := request.Job
			attempt++
			if attempt == 3 {
				return RunResult{Model: got.Model}, errors.New("ordinary execution failure")
			}
			return RunResult{Model: got.Model}, &ModelUnavailableError{
				Model: got.Model,
				Cause: errors.New("provider is offline"),
			}
		},
	}, publisher, func(cfg *Config) {
		cfg.Now = func() time.Time { return now }
	})

	runNextDue(t, manager)
	first, ok := store.job(job.ID)
	if !ok {
		t.Fatal("recurring unavailable job was deleted")
	}
	if first.LastStatus != StatusModelUnavailable ||
		!first.NextFire.Equal(now.Truncate(time.Minute).Add(time.Minute)) {
		t.Fatalf("first unavailable recurring state = %#v", first)
	}

	now = first.NextFire.Add(30 * time.Second)
	runNextDue(t, manager)
	if got := len(publisher.published()); got != 1 {
		t.Fatalf("notifications after repeated unavailability = %d, want 1", got)
	}

	notify := false
	if _, err := manager.Update(
		context.Background(),
		testOwner,
		job.ID,
		UpdateRequest{Notify: &notify},
	); err != nil {
		t.Fatalf("Update(disable notify) error = %v", err)
	}
	second, _ := store.job(job.ID)
	now = second.NextFire.Add(30 * time.Second)
	runNextDue(t, manager)
	afterOrdinaryFailure, _ := store.job(job.ID)
	if afterOrdinaryFailure.LastStatus != StatusFailed ||
		afterOrdinaryFailure.ModelUnavailableNotified {
		t.Fatalf("ordinary failure did not clear unavailable latch: %#v", afterOrdinaryFailure)
	}

	notify = true
	if _, err := manager.Update(
		context.Background(),
		testOwner,
		job.ID,
		UpdateRequest{Notify: &notify},
	); err != nil {
		t.Fatalf("Update(enable notify) error = %v", err)
	}
	now = afterOrdinaryFailure.NextFire.Add(30 * time.Second)
	runNextDue(t, manager)

	records := store.runRecords()
	wantStatuses := []Status{
		StatusModelUnavailable,
		StatusModelUnavailable,
		StatusFailed,
		StatusModelUnavailable,
	}
	if len(records) != len(wantStatuses) {
		t.Fatalf("run record count = %d, want %d: %#v", len(records), len(wantStatuses), records)
	}
	for i, want := range wantStatuses {
		if records[i].Status != want {
			t.Fatalf("run record %d status = %q, want %q", i, records[i].Status, want)
		}
	}
	if got := len(publisher.published()); got != 2 {
		t.Fatalf("notifications after new unavailable transition = %d, want 2", got)
	}
}

func TestNewManagerRecoversPendingRunBeforeScheduling(t *testing.T) {
	job := persistedRecurring("job-recover", testNow.Add(time.Minute))
	record := RunRecord{
		SchemaVersion: SchemaVersion,
		ID:            runID(job.ID, testNow.Add(-time.Minute)),
		JobID:         job.ID,
		JobName:       job.Name,
		Owner:         job.Owner,
		Status:        StatusSucceeded,
		Model:         job.Model,
		ScheduledFor:  testNow.Add(-time.Minute),
		StartedAt:     testNow.Add(-time.Minute),
		FinishedAt:    testNow.Add(-30 * time.Second),
		Output:        "recovered output",
	}
	job.LastStatus = StatusSucceeded
	job.PendingRun = &record
	store := newManagerTestStore(job)

	_ = newManagerForTest(t, store, managerTestExecutor{}, &managerTestPublisher{})

	records := store.runRecords()
	if len(records) != 1 || !reflect.DeepEqual(records[0], record) {
		t.Fatalf("recovered records = %#v, want %#v", records, []RunRecord{record})
	}
	persisted, ok := store.job(job.ID)
	if !ok {
		t.Fatal("recovered recurring job was deleted")
	}
	if persisted.PendingRun != nil {
		t.Fatalf("PendingRun = %#v, want nil after recovery", persisted.PendingRun)
	}
}

func TestManagerRecoversPendingNotificationBeforeArchivingRun(t *testing.T) {
	job := persistedRecurring("job-recover-notification", testNow.Add(time.Hour))
	record := RunRecord{
		SchemaVersion: SchemaVersion,
		ID:            runID(job.ID, testNow.Add(-time.Minute)),
		JobID:         job.ID,
		JobName:       job.Name,
		Owner:         job.Owner,
		Status:        StatusModelUnavailable,
		Model:         job.Model,
		ScheduledFor:  testNow.Add(-time.Minute),
		StartedAt:     testNow.Add(-time.Minute),
		FinishedAt:    testNow,
		Error:         "model unavailable",
	}
	job.LastStatus = StatusModelUnavailable
	job.PendingRun = &record
	job.PendingNotification = &Notification{
		Channel: job.Owner.Channel,
		ChatID:  job.Owner.ChatID,
		Text:    "model unavailable",
	}
	store := newManagerTestStore(job)
	publisher := &managerTestPublisher{}
	recorder := &managerTestDeliveryRecorder{}
	manager := newManagerForTest(
		t,
		store,
		managerTestExecutor{},
		publisher,
		func(cfg *Config) {
			cfg.DeliveryRecorder = recorder
		},
	)

	if got := len(store.runRecords()); got != 0 {
		t.Fatalf("run records during construction = %d, want durable notification first", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()
	waitForManagerTest(t, func() bool {
		return len(store.runRecords()) == 1
	})
	cancel()
	if err := waitManagerDone(t, done); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got := publisher.published(); len(got) != 1 || got[0].Text != "model unavailable" {
		t.Fatalf("recovered notifications = %#v", got)
	}
	deliveries := recorder.recorded()
	if len(deliveries) != 1 {
		t.Fatalf("recorded deliveries = %#v, want one", deliveries)
	}
	if delivery := deliveries[0]; delivery.JobID != job.ID ||
		delivery.RunID != record.ID ||
		delivery.Channel != job.Owner.Channel ||
		delivery.ChatID != job.Owner.ChatID ||
		delivery.Text != "model unavailable" ||
		!delivery.DeliveredAt.Equal(testNow) {
		t.Fatalf("recorded delivery = %#v", delivery)
	}
	persisted, ok := store.job(job.ID)
	if !ok {
		t.Fatal("recovered recurring job was deleted")
	}
	if persisted.PendingRun != nil || persisted.PendingNotification != nil ||
		!persisted.ModelUnavailableNotified {
		t.Fatalf("recovered notification state = %#v", persisted)
	}
}

func TestManagerRetriesTranscriptRecordingWithoutRedelivering(t *testing.T) {
	job := persistedRecurring("job-delivery-record-retry", testNow.Add(time.Hour))
	startedAt := testNow.Add(-time.Minute)
	record := RunRecord{
		SchemaVersion: SchemaVersion,
		ID:            runID(job.ID, startedAt),
		JobID:         job.ID,
		JobName:       job.Name,
		Owner:         job.Owner,
		Status:        StatusSucceeded,
		Model:         job.Model,
		AllowedTools:  []string{},
		ScheduledFor:  startedAt,
		StartedAt:     startedAt,
		FinishedAt:    testNow,
		Output:        "completed",
	}
	job.LastStatus = StatusSucceeded
	job.PendingRun = &record
	job.PendingNotification = &Notification{
		Channel: job.Owner.Channel,
		ChatID:  job.Owner.ChatID,
		Text:    "exact delivered text",
	}

	store := newManagerTestStore(job)
	publisher := &managerTestPublisher{}
	recorder := &managerTestDeliveryRecorder{err: errors.New("memory temporarily unavailable")}
	manager := newManagerForTest(
		t,
		store,
		managerTestExecutor{},
		publisher,
		func(cfg *Config) {
			cfg.DeliveryRecorder = recorder
		},
	)

	err := manager.finalizePendingRun(context.Background(), job.ID)
	if err == nil || !strings.Contains(err.Error(), "record delivered scheduled notification") {
		t.Fatalf("finalizePendingRun() error = %v", err)
	}
	persisted, ok := store.job(job.ID)
	if !ok || persisted.PendingNotification == nil ||
		!persisted.PendingNotification.DeliveredAt.Equal(testNow) {
		t.Fatalf("durable acknowledged notification = %#v", persisted.PendingNotification)
	}
	if got := len(publisher.published()); got != 1 {
		t.Fatalf("transport deliveries after recorder failure = %d, want 1", got)
	}

	recorder.mu.Lock()
	recorder.err = nil
	recorder.mu.Unlock()
	if err := manager.finalizePendingRun(context.Background(), job.ID); err != nil {
		t.Fatalf("finalizePendingRun(retry) error = %v", err)
	}
	if got := len(publisher.published()); got != 1 {
		t.Fatalf("transport deliveries after recorder retry = %d, want 1", got)
	}
	deliveries := recorder.recorded()
	if len(deliveries) != 2 || deliveries[0] != deliveries[1] {
		t.Fatalf("recording attempts = %#v, want two identical attempts", deliveries)
	}
	if records := store.runRecords(); len(records) != 1 ||
		!reflect.DeepEqual(records[0], record) {
		t.Fatalf("archived records = %#v, want %#v", records, []RunRecord{record})
	}
}

func TestNewManagerRecoversUnavailableOneShotPendingRun(t *testing.T) {
	job := persistedOneShot("job-recover-unavailable", testNow.Add(-time.Minute), true)
	job.Done = false
	job.NextFire = testNow.Add(defaultUnavailableRetryDelay)
	job.LastStatus = StatusModelUnavailable
	job.LastFailureAt = testNow
	job.LastError = "scheduled model is unavailable"
	job.ModelUnavailableNotified = true
	record := RunRecord{
		SchemaVersion: SchemaVersion,
		ID:            runID(job.ID, testNow.Add(-time.Minute)),
		JobID:         job.ID,
		JobName:       job.Name,
		Owner:         job.Owner,
		Status:        StatusModelUnavailable,
		Model:         job.Model,
		ScheduledFor:  job.RunAt,
		StartedAt:     testNow.Add(-time.Minute),
		FinishedAt:    testNow,
		Error:         job.LastError,
	}
	job.PendingRun = &record
	store := newManagerTestStore(job)

	_ = newManagerForTest(t, store, managerTestExecutor{}, &managerTestPublisher{})

	records := store.runRecords()
	if len(records) != 1 || !reflect.DeepEqual(records[0], record) {
		t.Fatalf("recovered unavailable records = %#v, want %#v", records, []RunRecord{record})
	}
	persisted, ok := store.job(job.ID)
	if !ok {
		t.Fatal("recovered unavailable oneshot was deleted")
	}
	if persisted.PendingRun != nil || persisted.Done ||
		!persisted.NextFire.Equal(job.NextFire) ||
		!persisted.ModelUnavailableNotified {
		t.Fatalf("recovered unavailable oneshot state = %#v", persisted)
	}
}

func TestNewManagerInterruptedRecoveryClearsUnavailableNotificationLatch(t *testing.T) {
	job := persistedRecurring("job-recover-interrupted", testNow.Add(time.Minute))
	job.LastRunAt = testNow.Add(-time.Minute)
	job.LastScheduledFor = testNow.Add(-time.Minute)
	job.LastStatus = StatusRunning
	job.ModelUnavailableNotified = true
	store := newManagerTestStore(job)

	_ = newManagerForTest(t, store, managerTestExecutor{}, &managerTestPublisher{})

	persisted, ok := store.job(job.ID)
	if !ok {
		t.Fatal("recovered interrupted recurring job was deleted")
	}
	if persisted.LastStatus != StatusInterrupted ||
		persisted.ModelUnavailableNotified ||
		persisted.PendingRun != nil {
		t.Fatalf("recovered interrupted recurring state = %#v", persisted)
	}
	records := store.runRecords()
	if len(records) != 1 || records[0].Status != StatusInterrupted {
		t.Fatalf("recovered interrupted records = %#v", records)
	}
}

func TestNewManagerRecoversAndDeletesCompletedOneShot(t *testing.T) {
	job := persistedOneShot("job-recover-oneshot", testNow.Add(-time.Minute), true)
	record := RunRecord{
		SchemaVersion: SchemaVersion,
		ID:            runID(job.ID, testNow.Add(-time.Minute)),
		JobID:         job.ID,
		JobName:       job.Name,
		Owner:         job.Owner,
		Status:        StatusSucceeded,
		Model:         job.Model,
		ScheduledFor:  job.RunAt,
		StartedAt:     testNow.Add(-time.Minute),
		FinishedAt:    testNow,
		Output:        "finished",
	}
	job.Done = true
	job.NextFire = time.Time{}
	job.LastStatus = StatusSucceeded
	job.PendingRun = &record
	store := newManagerTestStore(job)

	_ = newManagerForTest(t, store, managerTestExecutor{}, &managerTestPublisher{})

	if _, ok := store.job(job.ID); ok {
		t.Fatal("recovered completed oneshot job still persisted")
	}
	records := store.runRecords()
	if len(records) != 1 || !reflect.DeepEqual(records[0], record) {
		t.Fatalf("recovered records = %#v, want %#v", records, []RunRecord{record})
	}
}

func runNextDue(t *testing.T, manager *Manager) {
	t.Helper()
	job, scheduledFor, ok, err := manager.claimNextDue(context.Background())
	if err != nil {
		t.Fatalf("claimNextDue() error = %v", err)
	}
	if !ok {
		t.Fatal("claimNextDue() ok = false, want due job")
	}
	if err := manager.executeClaimed(context.Background(), job, scheduledFor); err != nil {
		t.Fatalf("executeClaimed() error = %v", err)
	}
}

func persistedOneShot(id string, runAt time.Time, notify bool) Job {
	return Job{
		SchemaVersion:  SchemaVersion,
		ID:             id,
		Name:           "One shot",
		Prompt:         "Run the one-shot task.",
		ContextProfile: ContextProfileMinimal,
		Kind:           KindOneShot,
		RunAt:          runAt,
		Notify:         notify,
		Model:          testDefaultModel,
		MaxTurns:       8,
		Owner:          testOwner,
		CreatedAt:      runAt.Add(-time.Hour),
		UpdatedAt:      runAt.Add(-time.Hour),
		NextFire:       runAt,
		LastStatus:     StatusPending,
	}
}

func persistedRecurring(id string, nextFire time.Time) Job {
	return Job{
		SchemaVersion:  SchemaVersion,
		ID:             id,
		Name:           "Recurring",
		Prompt:         "Run the recurring task.",
		ContextProfile: ContextProfileMinimal,
		Kind:           KindRecurring,
		Cron:           "* * * * *",
		Model:          testDefaultModel,
		MaxTurns:       8,
		Owner:          testOwner,
		CreatedAt:      nextFire.Add(-time.Hour),
		UpdatedAt:      nextFire.Add(-time.Hour),
		NextFire:       nextFire,
		LastStatus:     StatusPending,
	}
}

func waitChannel(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for scheduled execution")
	}
}

func waitForManagerTest(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for condition")
}

func waitManagerDone(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for manager shutdown")
		return nil
	}
}
