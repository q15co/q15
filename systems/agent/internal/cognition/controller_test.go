package cognition

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
)

type fakeControllerStore struct {
	mu                           sync.Mutex
	headSeq                      int64
	headAt                       time.Time
	states                       map[string]JobState
	records                      []RunRecord
	checkpoint                   ConsolidationCheckpoint
	semanticExtractionCheckpoint SemanticExtractionCheckpoint
	artifacts                    map[string]string
	deleted                      []string
}

func newFakeControllerStore() *fakeControllerStore {
	return &fakeControllerStore{
		headAt:    time.Date(2026, time.April, 6, 12, 0, 0, 0, time.UTC),
		states:    make(map[string]JobState),
		artifacts: make(map[string]string),
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

func (s *fakeControllerStore) StoreSemanticExtractionCheckpoint(
	_ context.Context,
	checkpoint SemanticExtractionCheckpoint,
) (SemanticExtractionCheckpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	checkpoint.LastExtractedSeq = max(0, checkpoint.LastExtractedSeq)
	if checkpoint.LastExtractedSeq > 0 {
		checkpoint.LastExtractedTurnID = fmt.Sprintf(
			"turn-%020d",
			checkpoint.LastExtractedSeq,
		)
		if checkpoint.LastExtractedAt.IsZero() {
			checkpoint.LastExtractedAt = s.headAt
		}
	} else {
		checkpoint.LastExtractedTurnID = ""
		checkpoint.LastExtractedAt = time.Time{}
	}
	if checkpoint.UpdatedAt.IsZero() {
		checkpoint.UpdatedAt = time.Now().UTC()
	}
	s.semanticExtractionCheckpoint = checkpoint
	return checkpoint, nil
}

func (s *fakeControllerStore) AppendRunRecord(_ context.Context, record RunRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, record)
	return nil
}

// DeleteCognitionArtifact implements the controller-side artifact delete
// primitive against the fake's artifact mirror, mirroring how a real store
// deletes the file the loader-side writes manage.
func (s *fakeControllerStore) DeleteCognitionArtifact(
	_ context.Context,
	relativePath string,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.artifacts, relativePath)
	s.deleted = append(s.deleted, relativePath)
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

func (s *fakeControllerStore) semanticCheckpoint() SemanticExtractionCheckpoint {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.semanticExtractionCheckpoint
}

func (s *fakeControllerStore) putArtifact(relativePath, content string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.artifacts == nil {
		s.artifacts = make(map[string]string)
	}
	s.artifacts[relativePath] = content
}

func (s *fakeControllerStore) artifact(relativePath string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	content, ok := s.artifacts[relativePath]
	return content, ok
}

func (s *fakeControllerStore) artifactPaths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	paths := make([]string, 0, len(s.artifacts))
	for path := range s.artifacts {
		paths = append(paths, path)
	}
	return paths
}

func (s *fakeControllerStore) deletedPaths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.deleted...)
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
							"path":    VerificationReviewRuntimePath,
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
	if got, want := records[0].Metadata["path"], VerificationReviewRuntimePath; got != want {
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

func TestControllerPersistsOrderedAttemptFailuresForModelFallback(t *testing.T) {
	store := newFakeControllerStore()
	store.setHead(1, time.Now().UTC())

	model := &fakeModelClient{
		errors: []error{
			errors.New("openai failed"),
			errors.New("glm failed"),
		},
	}
	controller, err := NewController(
		NewRunner(model, nil, []string{"gpt-5.4", "glm-5-turbo"}, &spyLoader{}),
		store,
		&spyLoader{},
		JobRegistration{
			NewJob: func() JobDefinition {
				return fakeJob{
					jobType: "verification.check",
					build: func(context.Context, ContextLoader) (Spec, error) {
						return Spec{
							Objective:          "Fail across model fallback.",
							CompletionContract: "Return `status: ok`.",
						}, nil
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
		},
	)
	if err != nil {
		t.Fatalf("NewController() error = %v", err)
	}
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

	records := store.runRecords()
	if len(records) != 1 {
		t.Fatalf("run records len = %d, want 1", len(records))
	}
	if records[0].Succeeded {
		t.Fatalf("Succeeded = true, want false")
	}
	if got, want := len(records[0].AttemptFailures), 2; got != want {
		t.Fatalf("attempt failures len = %d, want %d", got, want)
	}
	if got := records[0].AttemptFailures[0]; got.ModelRef != "gpt-5.4" ||
		got.Error != "openai failed" {
		t.Fatalf("attempt[0] = %#v", got)
	}
	if got := records[0].AttemptFailures[1]; got.ModelRef != "glm-5-turbo" ||
		got.Error != "glm failed" {
		t.Fatalf("attempt[1] = %#v", got)
	}
	if got := model.callModels; len(got) != 2 || got[0] != "gpt-5.4" || got[1] != "glm-5-turbo" {
		t.Fatalf("model calls = %#v, want ordered fallback attempts", got)
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
	if checkpoint := store.semanticCheckpoint(); checkpoint != (SemanticExtractionCheckpoint{}) {
		t.Fatalf("semantic checkpoint = %#v, want zero value", checkpoint)
	}
}

func TestControllerSuccessfulSemanticRunAdvancesSemanticExtractionCheckpoint(t *testing.T) {
	store := newFakeControllerStore()
	initialHeadAt := time.Date(2026, time.April, 7, 8, 9, 10, 0, time.UTC)
	store.setHead(3, initialHeadAt)

	controller := newControllerForTest(t, store, JobRegistration{
		NewJob: func() JobDefinition {
			return fakeJob{
				jobType: semanticMemoryExtractionJobType,
				build: func(context.Context, ContextLoader) (Spec, error) {
					return Spec{
						Objective:          "Extract semantic memory.",
						CompletionContract: "Return `status: ok`.",
					}, nil
				},
				apply: func(context.Context, ContextLoader, JobOutput) (ParsedResult, error) {
					return ParsedResult{Summary: "semantic"}, nil
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

	checkpoint := store.semanticCheckpoint()
	if checkpoint.LastExtractedSeq != 3 {
		t.Fatalf("checkpoint.LastExtractedSeq = %d, want 3", checkpoint.LastExtractedSeq)
	}
	if checkpoint.LastExtractedTurnID != "turn-00000000000000000003" {
		t.Fatalf(
			"checkpoint.LastExtractedTurnID = %q, want %q",
			checkpoint.LastExtractedTurnID,
			"turn-00000000000000000003",
		)
	}
	if !checkpoint.LastExtractedAt.Equal(initialHeadAt) {
		t.Fatalf(
			"checkpoint.LastExtractedAt = %s, want %s",
			checkpoint.LastExtractedAt,
			initialHeadAt,
		)
	}
	if checkpoint := store.consolidationCheckpoint(); checkpoint != (ConsolidationCheckpoint{}) {
		t.Fatalf("consolidation checkpoint = %#v, want zero value", checkpoint)
	}

	records := store.runRecords()
	if len(records) != 1 {
		t.Fatalf("run records len = %d, want 1", len(records))
	}
	if got, want := records[0].Metadata["semantic_extraction_checkpoint_seq"], "3"; got != want {
		t.Fatalf("run metadata[semantic_extraction_checkpoint_seq] = %q, want %q", got, want)
	}
	if got, want := records[0].Metadata["semantic_extraction_checkpoint_turn_id"], "turn-00000000000000000003"; got != want {
		t.Fatalf("run metadata[semantic_extraction_checkpoint_turn_id] = %q, want %q", got, want)
	}
	if got, want := records[0].Metadata["semantic_extraction_checkpoint_at"], initialHeadAt.Format(time.RFC3339Nano); got != want {
		t.Fatalf("run metadata[semantic_extraction_checkpoint_at] = %q, want %q", got, want)
	}
	if _, ok := records[0].Metadata["consolidation_checkpoint_seq"]; ok {
		t.Fatalf(
			"run metadata unexpectedly included consolidation checkpoint: %#v",
			records[0].Metadata,
		)
	}
}

func TestControllerPartialSemanticRunAdvancesCheckpointToCoveredWindow(t *testing.T) {
	store := newFakeControllerStore()
	store.setHead(100, time.Date(2026, time.April, 8, 9, 30, 0, 0, time.UTC))

	controller := newControllerForTest(t, store, JobRegistration{
		NewJob: func() JobDefinition {
			return fakeJob{
				jobType: semanticMemoryExtractionJobType,
				build: func(context.Context, ContextLoader) (Spec, error) {
					return Spec{
						Objective:          "Extract semantic memory.",
						CompletionContract: "Return `status: ok`.",
					}, nil
				},
				apply: func(context.Context, ContextLoader, JobOutput) (ParsedResult, error) {
					return ParsedResult{Summary: "window", CheckpointSeq: 24}, nil
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

	checkpoint := store.semanticCheckpoint()
	if checkpoint.LastExtractedSeq != 24 {
		t.Fatalf(
			"checkpoint.LastExtractedSeq = %d, want the covered window boundary 24",
			checkpoint.LastExtractedSeq,
		)
	}

	state := store.state(semanticMemoryExtractionJobType)
	if state.LastRunInputSeq != 24 {
		t.Fatalf(
			"state.LastRunInputSeq = %d, want 24 so the backlog keeps draining",
			state.LastRunInputSeq,
		)
	}
	if state.LastSuccessInputSeq != 24 {
		t.Fatalf("state.LastSuccessInputSeq = %d, want 24", state.LastSuccessInputSeq)
	}
	if state.DirtySinceSeq != 25 {
		t.Fatalf("state.DirtySinceSeq = %d, want 25", state.DirtySinceSeq)
	}

	// The job stays eligible while its backlog lasts, so the next pass drains the
	// following window instead of waiting for a new turn.
	if _, ok, err := controller.nextPendingRun(context.Background(), false, false, true); err != nil {
		t.Fatalf("nextPendingRun() (second) error = %v", err)
	} else if !ok {
		t.Fatal("nextPendingRun() (second) ok = false, want the backlog to stay eligible")
	}
}

// TestControllerRepairRunLeavesZeroExtractionCheckpointUntouched pins the
// checkpoint rule for a repair run: it consumes no transcript, so a store
// whose extraction checkpoint is still zero stays at zero even though the
// run reports no boundary at all (which every other job reads as "advance to
// the head"). An ordinary run on the same fixture still advances.
func TestControllerRepairRunLeavesZeroExtractionCheckpointUntouched(t *testing.T) {
	store := newFakeControllerStore()
	store.setHead(3, time.Date(2026, time.April, 8, 9, 30, 0, 0, time.UTC))

	repair := true
	controller := newControllerForTest(t, store, JobRegistration{
		NewJob: func() JobDefinition {
			return fakeJob{
				jobType: semanticMemoryExtractionJobType,
				build: func(context.Context, ContextLoader) (Spec, error) {
					return Spec{
						Objective:          "Extract semantic memory.",
						CompletionContract: "Return `status: ok`.",
					}, nil
				},
				apply: func(context.Context, ContextLoader, JobOutput) (ParsedResult, error) {
					if repair {
						// The repair run reports the checkpoint it was handed
						// (zero here) and carries the repair bit.
						return ParsedResult{Summary: "repair", Repair: true}, nil
					}
					return ParsedResult{Summary: "semantic"}, nil
				},
			}
		},
		Policy: TriggerPolicy{
			State: []StateRule{{
				ID: "dirty",
				Evaluate: func(context.Context, Snapshot, JobState) (bool, string, error) {
					return true, "dirty", nil
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

	if checkpoint := store.semanticCheckpoint(); checkpoint != (SemanticExtractionCheckpoint{}) {
		t.Fatalf(
			"after a repair run the semantic checkpoint = %#v, want the zero value untouched",
			checkpoint,
		)
	}
	records := store.runRecords()
	if len(records) != 1 || !records[0].Succeeded {
		t.Fatalf("run records = %#v, want one successful repair run", records)
	}
	if _, ok := records[0].Metadata["semantic_extraction_checkpoint_seq"]; ok {
		t.Fatalf(
			"repair run metadata = %#v, want no semantic checkpoint metadata",
			records[0].Metadata,
		)
	}

	// The ordinary run on the same fixture still advances the checkpoint to
	// the head it covered.
	repair = false
	store.setHead(4, time.Date(2026, time.April, 8, 9, 31, 0, 0, time.UTC))

	pending, ok, err = controller.nextPendingRun(context.Background(), false, false, true)
	if err != nil {
		t.Fatalf("nextPendingRun() (ordinary) error = %v", err)
	}
	if !ok {
		t.Fatal("nextPendingRun() (ordinary) ok = false, want true")
	}
	if err := controller.runPending(context.Background(), pending); err != nil {
		t.Fatalf("runPending() (ordinary) error = %v", err)
	}
	if checkpoint := store.semanticCheckpoint(); checkpoint.LastExtractedSeq != 4 {
		t.Fatalf(
			"after the ordinary run checkpoint.LastExtractedSeq = %d, want the covered head 4",
			checkpoint.LastExtractedSeq,
		)
	}
}

func TestControllerPartialWorkingMemoryRunAdvancesConsolidationCheckpointToCoveredWindow(
	t *testing.T,
) {
	store := newFakeControllerStore()
	store.setHead(100, time.Date(2026, time.April, 8, 9, 30, 0, 0, time.UTC))

	controller := newControllerForTest(t, store, JobRegistration{
		NewJob: func() JobDefinition {
			return fakeJob{
				jobType: workingMemoryConsolidationJobType,
				build: func(context.Context, ContextLoader) (Spec, error) {
					return Spec{
						Objective:          "Consolidate working memory.",
						CompletionContract: "Return `status: ok`.",
					}, nil
				},
				apply: func(context.Context, ContextLoader, JobOutput) (ParsedResult, error) {
					return ParsedResult{Summary: "window", CheckpointSeq: 24}, nil
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

	// The consolidation checkpoint is the working-memory job's window cursor. A
	// windowed run must leave it at the boundary it covered, not at the run head,
	// or the next run windows past the backlog the design exists to drain.
	checkpoint := store.consolidationCheckpoint()
	if checkpoint.LastConsolidatedSeq != 24 {
		t.Fatalf(
			"checkpoint.LastConsolidatedSeq = %d, want the covered window boundary 24",
			checkpoint.LastConsolidatedSeq,
		)
	}

	state := store.state(workingMemoryConsolidationJobType)
	if state.LastRunInputSeq != 24 {
		t.Fatalf(
			"state.LastRunInputSeq = %d, want 24 so the backlog keeps draining",
			state.LastRunInputSeq,
		)
	}

	if _, ok, err := controller.nextPendingRun(context.Background(), false, false, true); err != nil {
		t.Fatalf("nextPendingRun() (second) error = %v", err)
	} else if !ok {
		t.Fatal("nextPendingRun() (second) ok = false, want the backlog to stay eligible")
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

func TestControllerFailedSemanticRunLeavesCheckpointUnchanged(t *testing.T) {
	store := newFakeControllerStore()
	store.setHead(1, time.Now().UTC())

	controller := newControllerForTest(t, store, JobRegistration{
		NewJob: func() JobDefinition {
			return fakeJob{
				jobType: semanticMemoryExtractionJobType,
				build: func(context.Context, ContextLoader) (Spec, error) {
					return Spec{
						Objective:          "Fail semantic extraction.",
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

	if checkpoint := store.semanticCheckpoint(); checkpoint != (SemanticExtractionCheckpoint{}) {
		t.Fatalf("semantic checkpoint = %#v, want zero value", checkpoint)
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
	if checkpoint := store.semanticCheckpoint(); checkpoint != (SemanticExtractionCheckpoint{}) {
		t.Fatalf("semantic checkpoint = %#v, want zero value", checkpoint)
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
	if _, ok := records[0].Metadata["semantic_extraction_checkpoint_seq"]; ok {
		t.Fatalf(
			"run metadata unexpectedly included semantic checkpoint: %#v",
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

// stallProbeLoader mirrors the runtime wiring for the stall-escalation
// controller test: the job's Build reads its persisted state from the same
// store the controller drives, the controller's artifact writes land in the
// store's artifact mirror, and a fixed consolidation checkpoint keeps the real
// working-memory Build on the windowed replay path so the run's window size is
// observable.
type stallProbeLoader struct {
	*spyLoader
	store      *fakeControllerStore
	checkpoint ConsolidationCheckpoint

	mu          sync.Mutex
	windowTurns []int
}

func (l *stallProbeLoader) LoadJobState(
	ctx context.Context,
	jobType string,
) (JobState, error) {
	return l.store.LoadJobState(ctx, jobType)
}

func (l *stallProbeLoader) LoadConsolidationCheckpoint(
	context.Context,
) (ConsolidationCheckpoint, error) {
	return l.checkpoint, nil
}

func (l *stallProbeLoader) LoadMessagesSinceSeqWindow(
	_ context.Context,
	_ int64,
	maxTurns int,
) (TurnWindow, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.windowTurns = append(l.windowTurns, maxTurns)
	return TurnWindow{}, nil
}

func (l *stallProbeLoader) LoadCognitionArtifact(
	_ context.Context,
	relativePath string,
) (Artifact, error) {
	content, ok := l.store.artifact(relativePath)
	if !ok {
		return Artifact{}, nil
	}
	return Artifact{RelativePath: relativePath, Content: content}, nil
}

func (l *stallProbeLoader) StoreCognitionArtifact(_ context.Context, artifact Artifact) error {
	l.store.putArtifact(artifact.RelativePath, artifact.Content)
	return nil
}

func (l *stallProbeLoader) requestedWindowTurns() []int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]int(nil), l.windowTurns...)
}

// stallScriptJob runs the real working-memory Build so the degradation
// decision is exercised, and scripts the run outcome through ApplyResult.
type stallScriptJob struct {
	fail *bool
}

func (j stallScriptJob) Type() string {
	return workingMemoryConsolidationJobType
}

func (j stallScriptJob) Build(ctx context.Context, loader ContextLoader) (Spec, error) {
	return workingMemoryConsolidationJob{}.Build(ctx, loader)
}

func (j stallScriptJob) ApplyResult(
	context.Context,
	ContextLoader,
	JobOutput,
) (ParsedResult, error) {
	if j.fail != nil && *j.fail {
		return ParsedResult{}, errors.New("scripted consolidation failure")
	}
	return ParsedResult{Summary: "consolidated working memory"}, nil
}

// TestControllerStallEscalationHalvesWindowAndAlerts covers the stall
// escalation of a checkpoint-advancing job end to end: three consecutive
// failures halve the next run's input window, six leave an alert artifact
// naming the job and the count, two failures change nothing, and the next
// success deletes the artifact and resets the failure count.
func TestControllerStallEscalationHalvesWindowAndAlerts(t *testing.T) {
	store := newFakeControllerStore()
	loader := &stallProbeLoader{
		spyLoader:  &spyLoader{},
		store:      store,
		checkpoint: ConsolidationCheckpoint{LastConsolidatedSeq: 40},
	}
	model := &fakeModelClient{}
	model.complete = func(
		context.Context,
		string,
		[]conversation.Message,
		[]agent.ToolDefinition,
	) (agent.ModelClientResult, error) {
		return assistantResult("status: ok"), nil
	}

	failRuns := true
	controller, err := NewController(
		NewRunner(model, nil, []string{"cognition"}, loader),
		store,
		loader,
		JobRegistration{
			NewJob: func() JobDefinition {
				return stallScriptJob{fail: &failRuns}
			},
			Policy: TriggerPolicy{
				State: []StateRule{{
					ID: "dirty",
					Evaluate: func(context.Context, Snapshot, JobState) (bool, string, error) {
						return true, "scripted dirty tail", nil
					},
				}},
			},
		},
	)
	if err != nil {
		t.Fatalf("NewController() error = %v", err)
	}
	controller.started = time.Now().UTC()

	alertPath := StallAlertPath(workingMemoryConsolidationJobType)
	base := time.Date(2026, time.October, 13, 9, 0, 0, 0, time.UTC)
	ctx := context.Background()

	for run := 1; run <= 6; run++ {
		store.setHead(int64(run), base)
		pending, ok, err := controller.nextPendingRun(ctx, false, false, true)
		if err != nil {
			t.Fatalf("run %d: nextPendingRun() error = %v", run, err)
		}
		if !ok {
			t.Fatalf("run %d: nextPendingRun() ok = false, want a pending run", run)
		}
		if err := controller.runPending(ctx, pending); err != nil {
			t.Fatalf("run %d: runPending() error = %v", run, err)
		}

		if run == 2 {
			// Two consecutive failures change nothing: the window stays at
			// its default and no alert artifact exists.
			if got := loader.requestedWindowTurns(); !reflect.DeepEqual(
				got,
				[]int{workingMemoryWindowTurns, workingMemoryWindowTurns},
			) {
				t.Fatalf("window turns after two failures = %v, want no degradation", got)
			}
			if got := store.artifactPaths(); len(got) != 0 {
				t.Fatalf("artifacts after two failures = %v, want none", got)
			}
		}
	}

	wantTurns := []int{
		workingMemoryWindowTurns,
		workingMemoryWindowTurns,
		workingMemoryWindowTurns,
		workingMemoryWindowTurns / 2,
		workingMemoryWindowTurns / 2,
		workingMemoryWindowTurns / 2,
	}
	if got := loader.requestedWindowTurns(); !reflect.DeepEqual(got, wantTurns) {
		t.Fatalf("window turns per run = %v, want %v (halved from run 4)", got, wantTurns)
	}

	content, ok := store.artifact(alertPath)
	if !ok {
		t.Fatalf("stall alert artifact %q missing after six failures", alertPath)
	}
	for _, want := range []string{
		workingMemoryConsolidationJobType,
		"consecutive_failures: 6",
		"last_failure_at:",
		"last_failure: apply cognition result for job",
		"scripted consolidation failure",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("stall alert content missing %q:\n%s", want, content)
		}
	}

	// The next success deletes the alert and resets the failure count.
	failRuns = false
	store.setHead(7, base)
	pending, ok, err := controller.nextPendingRun(ctx, false, false, true)
	if err != nil {
		t.Fatalf("recovery: nextPendingRun() error = %v", err)
	}
	if !ok {
		t.Fatal("recovery: nextPendingRun() ok = false, want a pending run")
	}
	if err := controller.runPending(ctx, pending); err != nil {
		t.Fatalf("recovery: runPending() error = %v", err)
	}

	if got := store.state(workingMemoryConsolidationJobType).ConsecutiveFailures; got != 0 {
		t.Fatalf("ConsecutiveFailures after success = %d, want 0", got)
	}
	if got := store.artifactPaths(); len(got) != 0 {
		t.Fatalf("artifacts after success = %v, want the alert deleted", got)
	}
	deleted := store.deletedPaths()
	found := false
	for _, path := range deleted {
		if path == alertPath {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("delete calls = %v, want %q among them", deleted, alertPath)
	}
}
