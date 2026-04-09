package cognition

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
)

type fakeControllerStore struct {
	mu         sync.Mutex
	headSeq    int64
	headAt     time.Time
	states     map[string]JobState
	records    []RunRecord
	checkpoint ConsolidationCheckpoint
}

func newFakeControllerStore() *fakeControllerStore {
	return &fakeControllerStore{
		headAt: time.Date(2026, time.April, 6, 12, 0, 0, 0, time.UTC),
		states: make(map[string]JobState),
	}
}

func (s *fakeControllerStore) LoadHead(context.Context) (int64, time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.headSeq, s.headAt, nil
}

func (s *fakeControllerStore) LoadJobState(
	_ context.Context,
	jobType string,
) (JobState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneJobState(s.states[jobType]), nil
}

func (s *fakeControllerStore) StoreJobState(
	_ context.Context,
	jobType string,
	state JobState,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.states[jobType] = cloneJobState(state)
	return nil
}

func (s *fakeControllerStore) StoreConsolidationCheckpoint(
	_ context.Context,
	checkpoint ConsolidationCheckpoint,
) (ConsolidationCheckpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	checkpoint.LastConsolidatedSeq = max(0, checkpoint.LastConsolidatedSeq)
	if checkpoint.LastConsolidatedSeq > 0 {
		checkpoint.LastConsolidatedTurnID = fmt.Sprintf(
			"turn-%020d",
			checkpoint.LastConsolidatedSeq,
		)
		if checkpoint.LastConsolidatedAt.IsZero() {
			checkpoint.LastConsolidatedAt = s.headAt
		}
	} else {
		checkpoint.LastConsolidatedTurnID = ""
		checkpoint.LastConsolidatedAt = time.Time{}
	}
	if checkpoint.UpdatedAt.IsZero() {
		checkpoint.UpdatedAt = time.Now().UTC()
	}
	s.checkpoint = checkpoint
	return checkpoint, nil
}

func (s *fakeControllerStore) AppendRunRecord(_ context.Context, record RunRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, record)
	return nil
}

func (s *fakeControllerStore) setHead(seq int64, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.headSeq = seq
	s.headAt = at.UTC()
}

func (s *fakeControllerStore) state(jobType string) JobState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneJobState(s.states[jobType])
}

func (s *fakeControllerStore) runRecords() []RunRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]RunRecord, len(s.records))
	copy(out, s.records)
	return out
}

func (s *fakeControllerStore) consolidationCheckpoint() ConsolidationCheckpoint {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.checkpoint
}

func waitFor(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for condition")
}

func newControllerForTest(
	t *testing.T,
	store *fakeControllerStore,
	registrations ...JobRegistration,
) *Controller {
	t.Helper()

	model := &fakeModelClient{}
	model.results = make([]agent.ModelClientResult, 0, 8)
	for range 8 {
		model.results = append(model.results, assistantResult("status: ok"))
	}

	controller, err := NewController(
		NewRunner(model, nil, []string{"cognition"}, &spyLoader{}),
		store,
		&spyLoader{},
		registrations...,
	)
	if err != nil {
		t.Fatalf("NewController() error = %v", err)
	}
	return controller
}

func TestControllerRunsStartupTriggerOnceAtStartup(t *testing.T) {
	store := newFakeControllerStore()
	controller := newControllerForTest(t, store, JobRegistration{
		NewJob: func() JobDefinition {
			return fakeJob{
				jobType: "working_memory.consolidate",
				build: func(context.Context, ContextLoader) (Spec, error) {
					return Spec{
						Objective:          "Consolidate startup state.",
						CompletionContract: "Return `status: ok`.",
					}, nil
				},
				apply: func(context.Context, ContextLoader, JobOutput) (ParsedResult, error) {
					return ParsedResult{Summary: "startup"}, nil
				},
			}
		},
		Policy: TriggerPolicy{
			Startup: []StartupRule{{
				ID: "startup",
				Evaluate: func(context.Context, Snapshot, JobState) (bool, string, error) {
					return true, "startup tail", nil
				},
			}},
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- controller.Run(ctx)
	}()

	waitFor(t, 2*time.Second, func() bool {
		return len(store.runRecords()) == 1
	})

	records := store.runRecords()
	if records[0].Cause.Kind != RunCauseStartup {
		t.Fatalf("cause kind = %q, want %q", records[0].Cause.Kind, RunCauseStartup)
	}
	if records[0].Cause.RuleID != "startup" {
		t.Fatalf("cause rule id = %q, want startup", records[0].Cause.RuleID)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for controller exit")
	}
}

func TestControllerStateTriggerRunsOncePerHeadAdvance(t *testing.T) {
	store := newFakeControllerStore()
	controller := newControllerForTest(t, store, JobRegistration{
		NewJob: func() JobDefinition {
			return fakeJob{
				jobType: "verification.check",
				build: func(context.Context, ContextLoader) (Spec, error) {
					return Spec{
						Objective:          "Review dirty state.",
						CompletionContract: "Return `status: ok`.",
					}, nil
				},
				apply: func(context.Context, ContextLoader, JobOutput) (ParsedResult, error) {
					return ParsedResult{
						Summary: "checked",
						Metadata: map[string]string{
							"path":    "/memory/cognition/state/verification_review.md",
							"changed": "true",
						},
					}, nil
				},
			}
		},
		Policy: TriggerPolicy{
			State: []StateRule{{
				ID: "dirty",
				Evaluate: func(_ context.Context, _ Snapshot, state JobState) (bool, string, error) {
					return state.DirtySinceSeq > 0, "dirty transcript", nil
				},
			}},
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- controller.Run(ctx)
	}()

	store.setHead(1, time.Now().UTC())
	controller.NotifyStateChange()
	waitFor(t, 2*time.Second, func() bool {
		return len(store.runRecords()) == 1
	})

	state := store.state("verification.check")
	if state.DirtySinceSeq != 0 {
		t.Fatalf("DirtySinceSeq after first run = %d, want 0", state.DirtySinceSeq)
	}
	records := store.runRecords()
	if got, want := records[0].Metadata["path"], "/memory/cognition/state/verification_review.md"; got != want {
		t.Fatalf("run metadata[path] = %q, want %q", got, want)
	}
	if got, want := records[0].Metadata["changed"], "true"; got != want {
		t.Fatalf("run metadata[changed] = %q, want %q", got, want)
	}

	controller.NotifyStateChange()
	time.Sleep(50 * time.Millisecond)
	if got := len(store.runRecords()); got != 1 {
		t.Fatalf("run records after duplicate signal = %d, want 1", got)
	}

	store.setHead(2, time.Now().UTC())
	controller.NotifyStateChange()
	waitFor(t, 2*time.Second, func() bool {
		return len(store.runRecords()) == 2
	})

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for controller exit")
	}
}

func TestControllerRunsOverdueScheduleOnceAtStartup(t *testing.T) {
	store := newFakeControllerStore()
	store.states["verification.check"] = JobState{
		LastScheduledFor: map[string]time.Time{
			"nightly": time.Now().UTC().Add(-2 * time.Minute),
		},
	}

	controller := newControllerForTest(t, store, JobRegistration{
		NewJob: func() JobDefinition {
			return fakeJob{
				jobType: "verification.check",
				build: func(context.Context, ContextLoader) (Spec, error) {
					return Spec{
						Objective:          "Run on schedule.",
						CompletionContract: "Return `status: ok`.",
					}, nil
				},
				apply: func(context.Context, ContextLoader, JobOutput) (ParsedResult, error) {
					return ParsedResult{Summary: "scheduled"}, nil
				},
			}
		},
		Policy: TriggerPolicy{
			Schedule: []ScheduleRule{{
				ID:   "nightly",
				Spec: "* * * * *",
			}},
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- controller.Run(ctx)
	}()

	waitFor(t, 2*time.Second, func() bool {
		return len(store.runRecords()) == 1
	})

	record := store.runRecords()[0]
	if record.Cause.Kind != RunCauseSchedule {
		t.Fatalf("cause kind = %q, want %q", record.Cause.Kind, RunCauseSchedule)
	}
	if record.Cause.ScheduledFor.IsZero() {
		t.Fatal("scheduledFor = zero, want non-zero")
	}
	if record.Cause.ScheduledFor.After(record.Cause.FiredAt) {
		t.Fatalf("scheduledFor = %s, firedAt = %s", record.Cause.ScheduledFor, record.Cause.FiredAt)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for controller exit")
	}
}

func TestControllerFailureUpdatesStateAndRunRecord(t *testing.T) {
	store := newFakeControllerStore()
	store.setHead(1, time.Now().UTC())

	controller := newControllerForTest(t, store, JobRegistration{
		NewJob: func() JobDefinition {
			return fakeJob{
				jobType: "verification.check",
				build: func(context.Context, ContextLoader) (Spec, error) {
					return Spec{
						Objective:          "Fail deliberately.",
						CompletionContract: "Return `status: ok`.",
					}, nil
				},
				apply: func(context.Context, ContextLoader, JobOutput) (ParsedResult, error) {
					return ParsedResult{}, errors.New("apply failed")
				},
			}
		},
		Policy: TriggerPolicy{
			State: []StateRule{{
				ID: "dirty",
				Evaluate: func(_ context.Context, _ Snapshot, state JobState) (bool, string, error) {
					return state.DirtySinceSeq > 0, "dirty", nil
				},
			}},
		},
	})
	controller.started = time.Now().UTC()

	pending, ok, err := controller.nextPendingRun(context.Background(), false, false, true)
	if err != nil {
		t.Fatalf("nextPendingRun() error = %v", err)
	}
	if !ok {
		t.Fatal("nextPendingRun() ok = false, want true")
	}
	if err := controller.runPending(context.Background(), pending); err != nil {
		t.Fatalf("runPending() error = %v", err)
	}

	state := store.state("verification.check")
	if state.LastFailureAt.IsZero() {
		t.Fatal("LastFailureAt = zero, want non-zero")
	}
	if state.ConsecutiveFailures != 1 {
		t.Fatalf("ConsecutiveFailures = %d, want 1", state.ConsecutiveFailures)
	}
	records := store.runRecords()
	if len(records) != 1 {
		t.Fatalf("run records len = %d, want 1", len(records))
	}
	if records[0].Succeeded {
		t.Fatalf("Succeeded = true, want false")
	}
	if records[0].Error == "" {
		t.Fatal("Error = empty, want apply failure")
	}

	pending, ok, err = controller.nextPendingRun(context.Background(), false, false, true)
	if err != nil {
		t.Fatalf("nextPendingRun() after failure error = %v", err)
	}
	if ok {
		t.Fatalf("nextPendingRun() after failure = %#v, want no immediate retry", pending)
	}

	store.setHead(2, time.Now().UTC())
	_, ok, err = controller.nextPendingRun(context.Background(), false, false, true)
	if err != nil {
		t.Fatalf("nextPendingRun() after head advance error = %v", err)
	}
	if !ok {
		t.Fatal("nextPendingRun() after head advance ok = false, want true")
	}
}

func TestControllerPreservesDirtyStateWhenHeadAdvancesDuringRun(t *testing.T) {
	store := newFakeControllerStore()
	initialHeadAt := time.Now().UTC()
	store.setHead(1, initialHeadAt)

	controller := newControllerForTest(t, store, JobRegistration{
		NewJob: func() JobDefinition {
			return fakeJob{
				jobType: "working_memory.consolidate",
				build: func(context.Context, ContextLoader) (Spec, error) {
					return Spec{
						Objective:          "Consolidate dirty state.",
						CompletionContract: "Return `status: ok`.",
					}, nil
				},
				apply: func(context.Context, ContextLoader, JobOutput) (ParsedResult, error) {
					store.setHead(2, time.Now().UTC())
					return ParsedResult{Summary: "ok"}, nil
				},
			}
		},
		Policy: TriggerPolicy{
			State: []StateRule{{
				ID: "dirty",
				Evaluate: func(_ context.Context, _ Snapshot, state JobState) (bool, string, error) {
					return state.DirtySinceSeq > 0, "dirty", nil
				},
			}},
		},
	})
	controller.started = time.Now().UTC()

	pending, ok, err := controller.nextPendingRun(context.Background(), false, false, true)
	if err != nil {
		t.Fatalf("nextPendingRun() error = %v", err)
	}
	if !ok {
		t.Fatal("nextPendingRun() ok = false, want true")
	}
	if err := controller.runPending(context.Background(), pending); err != nil {
		t.Fatalf("runPending() error = %v", err)
	}

	state := store.state("working_memory.consolidate")
	if state.LastSuccessInputSeq != 1 {
		t.Fatalf("LastSuccessInputSeq = %d, want 1", state.LastSuccessInputSeq)
	}
	if state.LastObservedSeq != 2 {
		t.Fatalf("LastObservedSeq = %d, want 2", state.LastObservedSeq)
	}
	if state.DirtySinceSeq != 2 {
		t.Fatalf("DirtySinceSeq = %d, want 2", state.DirtySinceSeq)
	}

	checkpoint := store.consolidationCheckpoint()
	if checkpoint.LastConsolidatedSeq != 1 {
		t.Fatalf("checkpoint.LastConsolidatedSeq = %d, want 1", checkpoint.LastConsolidatedSeq)
	}
	if checkpoint.LastConsolidatedTurnID != "turn-00000000000000000001" {
		t.Fatalf(
			"checkpoint.LastConsolidatedTurnID = %q, want %q",
			checkpoint.LastConsolidatedTurnID,
			"turn-00000000000000000001",
		)
	}
	if !checkpoint.LastConsolidatedAt.Equal(initialHeadAt) {
		t.Fatalf(
			"checkpoint.LastConsolidatedAt = %s, want %s",
			checkpoint.LastConsolidatedAt,
			initialHeadAt,
		)
	}

	records := store.runRecords()
	if len(records) != 1 {
		t.Fatalf("run records len = %d, want 1", len(records))
	}
	if got, want := records[0].Metadata["consolidation_checkpoint_seq"], "1"; got != want {
		t.Fatalf("run metadata[consolidation_checkpoint_seq] = %q, want %q", got, want)
	}
	if got, want := records[0].Metadata["consolidation_checkpoint_turn_id"], "turn-00000000000000000001"; got != want {
		t.Fatalf("run metadata[consolidation_checkpoint_turn_id] = %q, want %q", got, want)
	}
	if got, want := records[0].Metadata["consolidation_checkpoint_at"], initialHeadAt.Format(time.RFC3339Nano); got != want {
		t.Fatalf("run metadata[consolidation_checkpoint_at] = %q, want %q", got, want)
	}
}

func TestControllerFailedWorkingMemoryRunLeavesCheckpointUnchanged(t *testing.T) {
	store := newFakeControllerStore()
	store.setHead(1, time.Now().UTC())

	controller := newControllerForTest(t, store, JobRegistration{
		NewJob: func() JobDefinition {
			return fakeJob{
				jobType: workingMemoryConsolidationJobType,
				build: func(context.Context, ContextLoader) (Spec, error) {
					return Spec{
						Objective:          "Fail consolidation.",
						CompletionContract: "Return `status: ok`.",
					}, nil
				},
				apply: func(context.Context, ContextLoader, JobOutput) (ParsedResult, error) {
					return ParsedResult{}, errors.New("apply failed")
				},
			}
		},
		Policy: TriggerPolicy{
			State: []StateRule{{
				ID: "dirty",
				Evaluate: func(_ context.Context, _ Snapshot, state JobState) (bool, string, error) {
					return state.DirtySinceSeq > 0, "dirty", nil
				},
			}},
		},
	})
	controller.started = time.Now().UTC()

	pending, ok, err := controller.nextPendingRun(context.Background(), false, false, true)
	if err != nil {
		t.Fatalf("nextPendingRun() error = %v", err)
	}
	if !ok {
		t.Fatal("nextPendingRun() ok = false, want true")
	}
	if err := controller.runPending(context.Background(), pending); err != nil {
		t.Fatalf("runPending() error = %v", err)
	}

	if checkpoint := store.consolidationCheckpoint(); checkpoint != (ConsolidationCheckpoint{}) {
		t.Fatalf("checkpoint = %#v, want zero value", checkpoint)
	}
}

func TestControllerVerificationRunDoesNotAdvanceCheckpoint(t *testing.T) {
	store := newFakeControllerStore()
	store.setHead(1, time.Now().UTC())

	controller := newControllerForTest(t, store, JobRegistration{
		NewJob: func() JobDefinition {
			return fakeJob{
				jobType: verificationReviewJobType,
				build: func(context.Context, ContextLoader) (Spec, error) {
					return Spec{
						Objective:          "Verify state.",
						CompletionContract: "Return `status: ok`.",
					}, nil
				},
				apply: func(context.Context, ContextLoader, JobOutput) (ParsedResult, error) {
					return ParsedResult{Summary: "verified"}, nil
				},
			}
		},
		Policy: TriggerPolicy{
			State: []StateRule{{
				ID: "dirty",
				Evaluate: func(_ context.Context, _ Snapshot, state JobState) (bool, string, error) {
					return state.DirtySinceSeq > 0, "dirty", nil
				},
			}},
		},
	})
	controller.started = time.Now().UTC()

	pending, ok, err := controller.nextPendingRun(context.Background(), false, false, true)
	if err != nil {
		t.Fatalf("nextPendingRun() error = %v", err)
	}
	if !ok {
		t.Fatal("nextPendingRun() ok = false, want true")
	}
	if err := controller.runPending(context.Background(), pending); err != nil {
		t.Fatalf("runPending() error = %v", err)
	}

	if checkpoint := store.consolidationCheckpoint(); checkpoint != (ConsolidationCheckpoint{}) {
		t.Fatalf("checkpoint = %#v, want zero value", checkpoint)
	}
	records := store.runRecords()
	if len(records) != 1 {
		t.Fatalf("run records len = %d, want 1", len(records))
	}
	if _, ok := records[0].Metadata["consolidation_checkpoint_seq"]; ok {
		t.Fatalf(
			"run metadata unexpectedly included consolidation checkpoint: %#v",
			records[0].Metadata,
		)
	}
}

func TestControllerRunsSeriallyWhenSignalsQueue(t *testing.T) {
	store := newFakeControllerStore()
	controllerReady := make(chan struct{})
	releaseFirst := make(chan struct{})

	var mu sync.Mutex
	applyCalls := 0
	currentRuns := 0
	maxRuns := 0

	controller := newControllerForTest(t, store, JobRegistration{
		NewJob: func() JobDefinition {
			return fakeJob{
				jobType: "verification.check",
				build: func(context.Context, ContextLoader) (Spec, error) {
					return Spec{
						Objective:          "Run serially.",
						CompletionContract: "Return `status: ok`.",
					}, nil
				},
				apply: func(context.Context, ContextLoader, JobOutput) (ParsedResult, error) {
					mu.Lock()
					applyCalls++
					currentRuns++
					if currentRuns > maxRuns {
						maxRuns = currentRuns
					}
					first := applyCalls == 1
					mu.Unlock()

					if first {
						close(controllerReady)
						<-releaseFirst
					}

					mu.Lock()
					currentRuns--
					mu.Unlock()
					return ParsedResult{Summary: "ok"}, nil
				},
			}
		},
		Policy: TriggerPolicy{
			State: []StateRule{{
				ID: "dirty",
				Evaluate: func(_ context.Context, _ Snapshot, state JobState) (bool, string, error) {
					return state.DirtySinceSeq > 0, "dirty", nil
				},
			}},
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- controller.Run(ctx)
	}()

	store.setHead(1, time.Now().UTC())
	controller.NotifyStateChange()

	select {
	case <-controllerReady:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first run to start")
	}

	store.setHead(2, time.Now().UTC())
	controller.NotifyStateChange()
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	if maxRuns != 1 {
		mu.Unlock()
		t.Fatalf("max concurrent runs = %d, want 1", maxRuns)
	}
	if applyCalls != 1 {
		mu.Unlock()
		t.Fatalf("apply calls before release = %d, want 1", applyCalls)
	}
	mu.Unlock()

	close(releaseFirst)
	waitFor(t, 2*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return applyCalls == 2
	})

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for controller exit")
	}
}

func TestControllerRunWithNoJobsExitsCleanly(t *testing.T) {
	controller := newControllerForTest(t, newFakeControllerStore())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := controller.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}
