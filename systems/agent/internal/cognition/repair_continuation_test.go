package cognition

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func repairContinuationController(
	t *testing.T,
	fail bool,
	repairable *bool,
) (*Controller, *fakeControllerStore) {
	t.Helper()
	store := newFakeControllerStore()
	store.setHead(12, time.Now().UTC())
	c := newControllerForTest(t, store, JobRegistration{
		NewJob: func() JobDefinition {
			return fakeJob{jobType: semanticMemoryExtractionJobType,
				build: func(context.Context, ContextLoader) (Spec, error) {
					return Spec{
						Objective:          "Decide synthetic repair outcomes.",
						CompletionContract: "Return decisions.",
						repair:             true,
						repairTarget:       MemorySurfaceTarget{Path: "semantic/synthetic.md"},
						CheckpointSeq:      12,
					}, nil
				},
				apply: func(context.Context, ContextLoader, JobOutput) (ParsedResult, error) {
					if fail {
						return ParsedResult{}, errors.New("synthetic repair failure")
					}
					return ParsedResult{Repair: true, CheckpointSeq: 12}, nil
				},
			}
		}, Policy: TriggerPolicy{State: []StateRule{{ID: "memory_surface_repairable", Evaluate: func(context.Context, Snapshot, JobState) (bool, string, error) {
			return *repairable, "synthetic surface", nil
		}}}},
	})
	loader := &stallProbeLoader{spyLoader: &spyLoader{}, store: store}
	c.loader = loader
	c.runner.loader = loader
	return c, store
}

func TestRepairProgressContinuesWithoutTranscriptAdvancing(t *testing.T) {
	ctx := context.Background()
	repairable := true
	c, store := repairContinuationController(t, false, &repairable)
	pending, ok, err := c.nextPendingRun(ctx, false, false, true)
	if err != nil || !ok {
		t.Fatalf("first pending=%v %v", ok, err)
	}
	if err := c.runPending(ctx, pending); err != nil {
		t.Fatal(err)
	}
	state, _ := store.LoadJobState(ctx, semanticMemoryExtractionJobType)
	if state.LastRunInputSeq != 12 {
		t.Fatal("repair changed the transcript input marker to manufacture eligibility")
	}
	_, ok, err = c.nextPendingRun(ctx, false, false, true)
	if err != nil || !ok {
		t.Fatalf("same-head repair did not continue: %v %v", ok, err)
	}
	if cp := store.semanticCheckpoint(); cp.LastExtractedSeq != 0 {
		t.Fatal("repair advanced extraction checkpoint")
	}
	repairable = false
	_, ok, err = c.nextPendingRun(ctx, false, false, true)
	if err != nil || ok {
		t.Fatalf("clean surface continued: %v %v", ok, err)
	}
}

func TestFailedRepairRetriesOnTimerThenStopsAndAlerts(t *testing.T) {
	ctx := context.Background()
	repairable := true
	c, store := repairContinuationController(t, true, &repairable)
	for attempt := 0; attempt < 4; attempt++ {
		pending, ok, err := c.nextPendingRun(ctx, false, true, true)
		if err != nil || !ok {
			t.Fatalf("attempt %d pending=%v error=%v", attempt, ok, err)
		}
		if err := c.runPending(ctx, pending); err != nil {
			t.Fatal(err)
		}
		state, _ := store.LoadJobState(ctx, semanticMemoryExtractionJobType)
		if attempt < 3 {
			next, err := c.nextScheduledAt(ctx, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			records := store.runRecords()
			want := records[len(records)-1].FinishedAt.Add(time.Minute * time.Duration(1<<attempt))
			if !next.Equal(want) {
				t.Fatalf("retry %d scheduled %v, want %v", attempt, next, want)
			}
			if _, ok, err := c.nextPendingRun(ctx, false, true, true); err != nil || ok {
				t.Fatal("repair retried before its backoff elapsed")
			}
			raw, _ := json.Marshal(state)
			var fields map[string]any
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			fields["repair_retry_at"] = time.Now().UTC().Add(-time.Second)
			raw, _ = json.Marshal(fields)
			if err := json.Unmarshal(raw, &state); err != nil {
				t.Fatal(err)
			}
			if err := store.StoreJobState(ctx, semanticMemoryExtractionJobType, state); err != nil {
				t.Fatal(err)
			}
		} else {
			if _, ok, err := c.nextPendingRun(ctx, false, true, true); err != nil || ok {
				t.Fatal("exhausted repair retry budget continued")
			}
			if _, exists := store.artifact(StallAlertPath(semanticMemoryExtractionJobType)); !exists {
				t.Fatal("exhausted repair did not alert")
			}
		}
	}
}

func TestRepairBackoffPersistsAndResumesWhenTargetChanges(t *testing.T) {
	ctx := context.Background()
	repairable := true
	c, store := repairContinuationController(t, true, &repairable)
	state := JobState{LastRunInputSeq: 12, RepairStopped: true, RepairFailures: 4,
		RepairTarget: "semantic/synthetic.md", RepairDigest: repairInputDigest(nil, false)}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var restored JobState
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if err := store.StoreJobState(ctx, semanticMemoryExtractionJobType, restored); err != nil {
		t.Fatal(err)
	}
	if err := c.bootstrapRepair(ctx); err != nil {
		t.Fatal(err)
	}
	store.setHead(13, time.Now().UTC())
	if _, ok, err := c.nextPendingRun(ctx, true, true, true); err != nil || ok {
		t.Fatalf("restart or conversation progress resumed stopped repair: %v %v", ok, err)
	}
	c.loader = &changedRepairLoader{ContextLoader: c.loader}
	if _, ok, err := c.nextPendingRun(ctx, false, true, true); err != nil || !ok {
		t.Fatalf("changed target did not resume repair: %v %v", ok, err)
	}
}

type changedRepairLoader struct{ ContextLoader }

func (l *changedRepairLoader) ReadFileBytes(context.Context, string) ([]byte, bool, error) {
	return []byte("synthetic replacement\n"), true, nil
}

func TestRepairProgressWakesDeferredConsolidation(t *testing.T) {
	ctx := context.Background()
	repairable := true
	c, store := repairContinuationController(t, false, &repairable)
	if err := store.StoreJobState(ctx, semanticMemoryExtractionJobType, JobState{LastRunInputSeq: 12}); err != nil {
		t.Fatal(err)
	}
	if err := c.queueRepair(ctx); err != nil {
		t.Fatal(err)
	}
	pending, ok, err := c.nextPendingRun(ctx, false, false, true)
	if err != nil || !ok {
		t.Fatalf("queued repair: %v %v", ok, err)
	}
	c.jobs = append(c.jobs, registeredJob{jobType: workingMemoryConsolidationJobType})
	if err := store.StoreJobState(ctx, workingMemoryConsolidationJobType, JobState{WaitingForRepair: true, LastRunNoWork: true, LastRunInputSeq: 12}); err != nil {
		t.Fatal(err)
	}
	if err := c.runPending(ctx, pending); err != nil {
		t.Fatal(err)
	}
	state, err := store.LoadJobState(ctx, workingMemoryConsolidationJobType)
	if err != nil || !state.MaintenancePending {
		t.Fatalf("deferred consolidation was not notified: %#v %v", state, err)
	}
}
