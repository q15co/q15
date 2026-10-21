package cognition

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNormalizeErrorSignatureCollapsesVariablePayloads(t *testing.T) {
	t.Parallel()

	first := normalizeErrorSignature(
		`apply cognition result for job "semantic_memory.extract": invalid written semantic claim in semantic/facts.md: claim requires a stable ID and statement (seq 4696)`,
	)
	second := normalizeErrorSignature(
		`apply cognition result for job "semantic_memory.extract": invalid written semantic claim in semantic/facts.md: claim requires a stable ID and statement (seq 5001)`,
	)
	if first != second {
		t.Fatalf("signature differs across variable payloads:\n%q\n%q", first, second)
	}

	other := normalizeErrorSignature(
		`build cognition job "semantic_memory.extract": prepare conversion recovery: memory recovery is pending; recover it before another run`,
	)
	if first == other {
		t.Fatal("signature identical across distinct failure classes")
	}
	if strings.Contains(first, "4696") {
		t.Fatalf("signature still embeds variable digits: %q", first)
	}
}

func TestUpdateFailureBackoffLadderProgression(t *testing.T) {
	t.Parallel()

	finished := time.Date(2026, time.October, 20, 12, 0, 0, 0, time.UTC)
	runErr := errors.New("apply failed: stable cause")

	state := JobState{}
	expectedDelays := []time.Duration{7, 30, 120, 24 * 60} // minutes
	for i, minutes := range expectedDelays {
		state = updateFailureBackoff(state, "semantic_memory.extract", runErr, finished)
		if got, want := state.FailureBackoffStep, i+1; got != want {
			t.Fatalf("step %d: FailureBackoffStep = %d, want %d", i, got, want)
		}
		want := finished.Add(time.Duration(minutes) * time.Minute)
		if !state.FailureRetryAt.Equal(want) {
			t.Fatalf("step %d: FailureRetryAt = %s, want %s", i, state.FailureRetryAt, want)
		}
		if failureParked(state) {
			t.Fatalf("step %d: parked = true, want false", i)
		}
	}
	// Step 5 and beyond stay at the 24h cap until the park threshold.
	state = updateFailureBackoff(state, "semantic_memory.extract", runErr, finished)
	if got, want := state.FailureRetryAt, finished.Add(24*time.Hour); !got.Equal(want) {
		t.Fatalf("step 5: FailureRetryAt = %s, want %s", got, want)
	}
}

func TestUpdateFailureBackoffParksAtThreshold(t *testing.T) {
	t.Parallel()

	finished := time.Date(2026, time.October, 20, 12, 0, 0, 0, time.UTC)
	runErr := errors.New("same failure forever")

	state := JobState{}
	for i := 0; i < failureParkThreshold-1; i++ {
		state = updateFailureBackoff(state, "semantic_memory.extract", runErr, finished)
		if failureParked(state) {
			t.Fatalf("parked after %d failures, want parked only at %d", i+1, failureParkThreshold)
		}
	}
	state = updateFailureBackoff(state, "semantic_memory.extract", runErr, finished)
	if !failureParked(state) {
		t.Fatalf("parked = false after %d identical failures, want true", failureParkThreshold)
	}
	if !state.FailureRetryAt.IsZero() {
		t.Fatalf(
			"parked FailureRetryAt = %s, want zero (no automatic re-fire)",
			state.FailureRetryAt,
		)
	}
	// Parked is terminal for the fingerprint: further identical failures must
	// not arm a retry timer.
	state = updateFailureBackoff(state, "semantic_memory.extract", runErr, finished)
	if !failureParked(state) || !state.FailureRetryAt.IsZero() {
		t.Fatalf(
			"parked state changed: step=%d retryAt=%s",
			state.FailureBackoffStep,
			state.FailureRetryAt,
		)
	}
}

func TestUpdateFailureBackoffResetsOnFingerprintChange(t *testing.T) {
	t.Parallel()

	finished := time.Date(2026, time.October, 20, 12, 0, 0, 0, time.UTC)

	state := JobState{}
	runErr := errors.New("same failure")
	for i := 0; i < 10; i++ {
		state = updateFailureBackoff(state, "semantic_memory.extract", runErr, finished)
	}
	if got := state.FailureBackoffStep; got != 10 {
		t.Fatalf("FailureBackoffStep = %d, want 10", got)
	}

	state = updateFailureBackoff(
		state,
		"semantic_memory.extract",
		errors.New("a different failure"),
		finished,
	)
	if got := state.FailureBackoffStep; got != 1 {
		t.Fatalf("FailureBackoffStep after fingerprint change = %d, want 1", got)
	}
	if got, want := state.FailureRetryAt, finished.Add(7*time.Minute); !got.Equal(want) {
		t.Fatalf("FailureRetryAt after fingerprint change = %s, want %s", got, want)
	}
}

func TestJobStateFailureBackoffJSONCompatibility(t *testing.T) {
	t.Parallel()

	// The production shape written before backoff existed (captured from
	// cognition/triggers/jobs/working_memory_consolidate.json, 2026-10-18) must
	// unmarshal with zero backoff fields.
	legacy := `{
		"repair_retry_at": "0001-01-01T00:00:00Z",
		"last_run_at": "2026-10-18T13:43:59.324273307Z",
		"last_observed_seq": 4622,
		"consecutive_failures": 21
	}`
	var state JobState
	if err := json.Unmarshal([]byte(legacy), &state); err != nil {
		t.Fatalf("unmarshal legacy state: %v", err)
	}
	if state.FailureFingerprint != "" || state.FailureBackoffStep != 0 ||
		!state.FailureRetryAt.IsZero() {
		t.Fatalf("legacy state gained non-zero backoff fields: %+v", state)
	}

	state = updateFailureBackoff(state, "semantic_memory.extract", errors.New("boom"), time.Now())
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var roundTrip JobState
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatalf("unmarshal round trip: %v", err)
	}
	if roundTrip.FailureFingerprint != state.FailureFingerprint ||
		roundTrip.FailureBackoffStep != state.FailureBackoffStep ||
		!roundTrip.FailureRetryAt.Equal(state.FailureRetryAt) {
		t.Fatal("backoff fields did not round trip")
	}
}

func backoffTestRegistration(failPtr *bool) JobRegistration {
	return JobRegistration{
		NewJob: func() JobDefinition {
			return fakeJob{
				jobType: "verification.check",
				build: func(context.Context, ContextLoader) (Spec, error) {
					return Spec{
						Objective:          "Probe.",
						CompletionContract: "Return `status: ok`.",
					}, nil
				},
				apply: func(context.Context, ContextLoader, JobOutput) (ParsedResult, error) {
					if *failPtr {
						return ParsedResult{}, errors.New("apply failed: stable cause")
					}
					return ParsedResult{}, nil
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
	}
}

// The transcript_continuation lane re-fires a job while bounded transcript
// work remains. Before #379 an identical-failure storm re-fired immediately
// per pass; the backoff gate must delay the lane.
func TestFailureBackoffGatesTranscriptContinuationLane(t *testing.T) {
	store := newFakeControllerStore()
	store.setHead(1, time.Now().UTC())
	if err := store.StoreJobState(context.Background(), "verification.check", JobState{
		TranscriptPending: true,
		LastRunInputSeq:   1,
	}); err != nil {
		t.Fatalf("seed job state: %v", err)
	}

	fail := true
	controller := newControllerForTest(t, store, backoffTestRegistration(&fail))
	controller.started = time.Now().UTC()
	ctx := context.Background()

	pending, ok, err := controller.nextPendingRun(ctx, false, false, true)
	if err != nil || !ok {
		t.Fatalf("initial nextPendingRun() = (ok=%v, err=%v), want pending run", ok, err)
	}
	if err := controller.runPending(ctx, pending); err != nil {
		t.Fatalf("runPending() error = %v", err)
	}

	state := store.state("verification.check")
	if state.FailureBackoffStep != 1 {
		t.Fatalf("FailureBackoffStep = %d, want 1", state.FailureBackoffStep)
	}
	if state.FailureFingerprint == "" {
		t.Fatal("FailureFingerprint = empty, want recorded")
	}
	if until := time.Until(state.FailureRetryAt); until < 6*time.Minute || until > 8*time.Minute {
		t.Fatalf("FailureRetryAt = %s from now, want ~7m", until)
	}
	records := store.runRecords()
	if len(records) != 1 {
		t.Fatalf("run records = %d, want 1", len(records))
	}
	if got := records[0].ConsecutiveCount; got != 1 {
		t.Fatalf("record ConsecutiveCount = %d, want 1 (first of streak)", got)
	}

	if _, ok, err := controller.nextPendingRun(ctx, false, false, true); err != nil || ok {
		t.Fatalf("nextPendingRun() during backoff = (ok=%v, err=%v), want no re-fire", ok, err)
	}
}

func TestFailureBackoffExpiryRefiresJob(t *testing.T) {
	store := newFakeControllerStore()
	store.setHead(1, time.Now().UTC())
	if err := store.StoreJobState(context.Background(), "verification.check", JobState{
		TranscriptPending: true,
		LastRunInputSeq:   1,
		FailureFingerprint: failureFingerprint(
			"verification.check",
			errors.New("apply failed: stable cause"),
		),
		FailureBackoffStep: 1,
		FailureRetryAt:     time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatalf("seed job state: %v", err)
	}

	fail := true
	controller := newControllerForTest(t, store, backoffTestRegistration(&fail))
	controller.started = time.Now().UTC()

	if _, ok, err := controller.nextPendingRun(context.Background(), false, false, true); err != nil ||
		!ok {
		t.Fatalf(
			"nextPendingRun() after backoff expiry = (ok=%v, err=%v), want pending run",
			ok,
			err,
		)
	}
}

func TestFailureParkedJobStaysQuietOnAllTriggers(t *testing.T) {
	store := newFakeControllerStore()
	store.setHead(1, time.Now().UTC())
	if err := store.StoreJobState(context.Background(), "verification.check", JobState{
		TranscriptPending: true,
		LastRunInputSeq:   1,
		FailureFingerprint: failureFingerprint(
			"verification.check",
			errors.New("apply failed: stable cause"),
		),
		FailureBackoffStep: failureParkThreshold,
	}); err != nil {
		t.Fatalf("seed job state: %v", err)
	}

	fail := true
	registration := backoffTestRegistration(&fail)
	registration.Policy.Schedule = []ScheduleRule{{ID: "minutely", Spec: "* * * * *"}}
	controller := newControllerForTest(t, store, registration)
	controller.started = time.Now().UTC()
	ctx := context.Background()

	if _, ok, err := controller.nextPendingRun(ctx, true, true, true); err != nil || ok {
		t.Fatalf("nextPendingRun() for parked job = (ok=%v, err=%v), want silence", ok, err)
	}
	nextAt, err := controller.nextScheduledAt(ctx, time.Now().UTC())
	if err != nil {
		t.Fatalf("nextScheduledAt() error = %v", err)
	}
	if !nextAt.IsZero() {
		t.Fatalf("nextScheduledAt() = %s, want zero (parked job contributes no wake timer)", nextAt)
	}
}

func TestFailureBackoffWaitingContributesRetryWake(t *testing.T) {
	store := newFakeControllerStore()
	store.setHead(1, time.Now().UTC())
	retryAt := time.Now().UTC().Add(7 * time.Minute).Truncate(time.Second)
	if err := store.StoreJobState(context.Background(), "verification.check", JobState{
		TranscriptPending: true,
		LastRunInputSeq:   1,
		FailureFingerprint: failureFingerprint(
			"verification.check",
			errors.New("apply failed: stable cause"),
		),
		FailureBackoffStep: 1,
		FailureRetryAt:     retryAt,
	}); err != nil {
		t.Fatalf("seed job state: %v", err)
	}

	fail := true
	registration := backoffTestRegistration(&fail)
	registration.Policy.Schedule = []ScheduleRule{{ID: "daily", Spec: "0 4 * * *"}}
	controller := newControllerForTest(t, store, registration)
	controller.started = time.Now().UTC()

	nextAt, err := controller.nextScheduledAt(context.Background(), time.Now().UTC())
	if err != nil {
		t.Fatalf("nextScheduledAt() error = %v", err)
	}
	if !nextAt.Equal(retryAt) {
		t.Fatalf("nextScheduledAt() = %s, want backoff wake %s", nextAt, retryAt)
	}
}

func TestFailureBackoffResetsOnSuccess(t *testing.T) {
	store := newFakeControllerStore()
	store.setHead(1, time.Now().UTC())
	if err := store.StoreJobState(context.Background(), "verification.check", JobState{
		TranscriptPending: true,
		LastRunInputSeq:   1,
	}); err != nil {
		t.Fatalf("seed job state: %v", err)
	}

	fail := true
	controller := newControllerForTest(t, store, backoffTestRegistration(&fail))
	controller.started = time.Now().UTC()
	ctx := context.Background()

	pending, ok, err := controller.nextPendingRun(ctx, false, false, true)
	if err != nil || !ok {
		t.Fatalf("initial nextPendingRun() = (ok=%v, err=%v)", ok, err)
	}
	if err := controller.runPending(ctx, pending); err != nil {
		t.Fatalf("runPending() error = %v", err)
	}
	if step := store.state("verification.check").FailureBackoffStep; step != 1 {
		t.Fatalf("FailureBackoffStep = %d, want 1 after failure", step)
	}

	// Expire the backoff by hand, then let the job succeed.
	state := store.state("verification.check")
	state.FailureRetryAt = time.Now().Add(-time.Minute)
	if err := store.StoreJobState(ctx, "verification.check", state); err != nil {
		t.Fatalf("expire backoff: %v", err)
	}
	fail = false

	pending, ok, err = controller.nextPendingRun(ctx, false, false, true)
	if err != nil || !ok {
		t.Fatalf("nextPendingRun() after expiry = (ok=%v, err=%v), want pending run", ok, err)
	}
	if err := controller.runPending(ctx, pending); err != nil {
		t.Fatalf("runPending() (success) error = %v", err)
	}
	state = store.state("verification.check")
	if state.FailureFingerprint != "" || state.FailureBackoffStep != 0 ||
		!state.FailureRetryAt.IsZero() {
		t.Fatalf("backoff state survived success: %+v", state)
	}
}

func TestRenderStallAlertParkedWording(t *testing.T) {
	t.Parallel()

	state := JobState{
		ConsecutiveFailures: failureParkThreshold,
		FailureBackoffStep:  failureParkThreshold,
		FailureFingerprint:  failureFingerprint("semantic_memory.extract", errors.New("boom")),
		LastFailureAt:       time.Date(2026, time.October, 20, 12, 0, 0, 0, time.UTC),
	}
	alert := renderStallAlert("semantic_memory.extract", state, errors.New("boom"))
	for _, want := range []string{"parked", "cognition/triggers/jobs/"} {
		if !strings.Contains(alert, want) {
			t.Fatalf("parked stall alert missing %q:\n%s", want, alert)
		}
	}
}

// expireFailureBackoff moves a job past its armed backoff window, simulating
// the retry time having passed so tests driving repeated runs do not wait out
// the #379 backoff ladder.
func expireFailureBackoff(t *testing.T, store *fakeControllerStore, jobType string) {
	t.Helper()
	state := store.state(jobType)
	if state.FailureRetryAt.IsZero() {
		return
	}
	state.FailureRetryAt = time.Now().Add(-time.Minute)
	if err := store.StoreJobState(context.Background(), jobType, state); err != nil {
		t.Fatalf("expire failure backoff: %v", err)
	}
}

// A repair retry owns its own lifecycle: an armed repair retry timer must
// survive the identical-failure park, or a parked extract job that is also
// repairing never wakes to retry the repair (#379 review follow-up).
func TestFailureParkedJobStillHonorsRepairRetryTimer(t *testing.T) {
	store := newFakeControllerStore()
	store.setHead(1, time.Now().UTC())
	repairRetryAt := time.Now().UTC().Add(4 * time.Minute).Truncate(time.Second)
	if err := store.StoreJobState(context.Background(), "verification.check", JobState{
		TranscriptPending:  true,
		LastRunInputSeq:    1,
		FailureFingerprint: failureFingerprint("verification.check", errors.New("boom")),
		FailureBackoffStep: failureParkThreshold,
		RepairRetryAt:      repairRetryAt,
	}); err != nil {
		t.Fatalf("seed job state: %v", err)
	}

	fail := true
	registration := backoffTestRegistration(&fail)
	registration.Policy.Schedule = []ScheduleRule{{ID: "daily", Spec: "0 4 * * *"}}
	controller := newControllerForTest(t, store, registration)
	controller.started = time.Now().UTC()

	nextAt, err := controller.nextScheduledAt(context.Background(), time.Now().UTC())
	if err != nil {
		t.Fatalf("nextScheduledAt() error = %v", err)
	}
	if !nextAt.Equal(repairRetryAt) {
		t.Fatalf("nextScheduledAt() = %s, want repair retry wake %s", nextAt, repairRetryAt)
	}
}

// A failing repair run carries result.Repair and must not inherit the
// failure-streak count of a parallel non-repair streak, or the store would
// suppress the commit of an unrelated repair failure record (#379 review
// follow-up).
func TestRepairFailureRecordIgnoresFailureStreak(t *testing.T) {
	store := newFakeControllerStore()
	store.setHead(1, time.Now().UTC())
	if err := store.StoreJobState(context.Background(), "verification.check", JobState{
		FailureFingerprint: failureFingerprint("verification.check", errors.New("boom")),
		FailureBackoffStep: failureParkThreshold,
	}); err != nil {
		t.Fatalf("seed job state: %v", err)
	}

	controller := newControllerForTest(t, store, JobRegistration{
		NewJob: func() JobDefinition {
			return fakeJob{
				jobType: "verification.check",
				build: func(context.Context, ContextLoader) (Spec, error) {
					return Spec{
						Objective:          "Repair probe.",
						CompletionContract: "Return `status: ok`.",
						repair:             true,
					}, nil
				},
				apply: func(context.Context, ContextLoader, JobOutput) (ParsedResult, error) {
					return ParsedResult{}, errors.New("repair apply failed")
				},
			}
		},
	})

	pending := pendingRun{
		job:     controller.jobs[0],
		state:   store.state("verification.check"),
		headSeq: 1,
		headAt:  time.Now().UTC(),
	}
	if err := controller.runPending(context.Background(), pending); err != nil {
		t.Fatalf("runPending() error = %v", err)
	}

	records := store.runRecords()
	if len(records) != 1 {
		t.Fatalf("run records = %d, want 1", len(records))
	}
	if records[0].Succeeded {
		t.Fatal("record Succeeded = true, want a repair failure")
	}
	if got := records[0].ConsecutiveCount; got != 0 {
		t.Fatalf(
			"repair failure ConsecutiveCount = %d, want 0 (must not inherit the non-repair streak)",
			got,
		)
	}
}
