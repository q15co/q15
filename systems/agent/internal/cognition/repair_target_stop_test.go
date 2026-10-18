package cognition

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestStoppedRepairTargetDoesNotParkOtherTargets(t *testing.T) {
	ctx := context.Background()
	loader, _ := typedRepairFixture(t, "Synthetic blocked content with an unchanged obstacle.\n")
	const other = "working/second.md"
	loader.files[other] = []byte("Synthetic other content.\n")
	loader.memoryFiles = append(
		loader.memoryFiles,
		MemoryFile{Path: other, Bytes: int64(len(loader.files[other]))},
	)
	state := JobState{RepairStopped: true, RepairFailures: 4, RepairTarget: repairNotesPath,
		RepairDigest: repairInputDigest(loader.files[repairNotesPath], true)}
	store := newFakeControllerStore()
	c := newControllerForTest(t, store, NewSemanticMemoryExtractionRegistration(0, 0))
	c.started = time.Now().UTC()
	c.loader = loader
	job := c.jobs[0]
	ready, blocked, _, _, err := c.repairEligibility(
		ctx,
		job,
		Snapshot{Loader: loader, NowUTC: time.Now().UTC()},
		&state,
	)
	if err != nil || !ready || blocked {
		t.Fatalf(
			"one stopped target parked the surface: ready=%v blocked=%v error=%v",
			ready,
			blocked,
			err,
		)
	}
	loader.jobState = state
	spec, err := job.newJob().Build(ctx, loader)
	if err != nil || !spec.repair || spec.repairTarget.Path != other {
		t.Fatalf(
			"selected stopped target instead of other target: target=%s error=%v",
			spec.repairTarget.Path,
			err,
		)
	}
	updateRepairSchedule(&state, Result{Repair: true, repairTarget: other,
		repairDigest: repairInputDigest(loader.files[other], true)}, nil, time.Now().UTC())
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var restored JobState
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	loader.jobState = restored
	for i, file := range loader.memoryFiles {
		if file.Path == other {
			loader.memoryFiles = append(loader.memoryFiles[:i], loader.memoryFiles[i+1:]...)
			break
		}
	}
	spec, err = job.newJob().Build(ctx, loader)
	if err != nil || spec.NoWork == nil {
		t.Fatalf(
			"success on another target or restart released unchanged stop: target=%s error=%v",
			spec.repairTarget.Path,
			err,
		)
	}
	state = loader.jobState
	ready, blocked, _, _, err = c.repairEligibility(
		ctx,
		job,
		Snapshot{Loader: loader, NowUTC: time.Now().UTC()},
		&state,
	)
	if err != nil || ready || !blocked {
		t.Fatalf(
			"fully stopped surface did not stay bounded: ready=%v blocked=%v error=%v",
			ready,
			blocked,
			err,
		)
	}
	if err := store.StoreJobState(ctx, semanticMemoryExtractionJobType, state); err != nil {
		t.Fatal(err)
	}
	if next, err := c.nextScheduledAt(ctx, time.Now().UTC()); err != nil || !next.IsZero() {
		t.Fatalf(
			"fully stopped surface retained a recurring schedule timer: next=%v error=%v",
			next,
			err,
		)
	}
	loader.files[repairNotesPath] = []byte("Synthetic replacement content.\n")
	loader.jobState = state
	spec, err = job.newJob().Build(ctx, loader)
	if err != nil || !spec.repair || spec.repairTarget.Path != repairNotesPath {
		t.Fatalf("changed target did not resume: target=%s error=%v", spec.repairTarget.Path, err)
	}
}

func TestRepairRetryTimerSurvivesAnotherStoppedTarget(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	state := JobState{RepairStopped: true, RepairFailures: 4,
		RepairTarget: "semantic/facts.md", RepairDigest: "facts-digest"}
	updateRepairSchedule(&state, Result{Repair: true,
		repairTarget: "semantic/preferences.md", repairDigest: "preferences-digest"},
		errors.New("synthetic obstacle"), now)
	store := newFakeControllerStore()
	c := newControllerForTest(t, store, NewSemanticMemoryExtractionRegistration(0, 0))
	if err := store.StoreJobState(ctx, semanticMemoryExtractionJobType, state); err != nil {
		t.Fatal(err)
	}
	if next, err := c.nextScheduledAt(ctx, now); err != nil || !next.Equal(state.RepairRetryAt) {
		t.Fatalf(
			"another stopped target suppressed retry timer: next=%v want=%v error=%v",
			next,
			state.RepairRetryAt,
			err,
		)
	}
}

func TestEachRepairTargetRetainsItsOwnRetryBound(t *testing.T) {
	var state JobState
	now := time.Now().UTC()
	for _, target := range []string{"semantic/facts.md", "semantic/preferences.md"} {
		result := Result{Repair: true, repairTarget: target, repairDigest: target + "-digest"}
		for attempt := 1; attempt <= 4; attempt++ {
			updateRepairSchedule(&state, result, errors.New("synthetic obstacle"), now)
			if state.RepairFailures != attempt || state.RepairStopped != (attempt == 4) {
				t.Fatalf(
					"%s attempt %d inherited another target's failures: %#v",
					target,
					attempt,
					state,
				)
			}
		}
	}
	cloned := cloneJobState(state)
	updateRepairSchedule(
		&cloned,
		Result{Repair: true, repairTarget: "semantic/projects.md", repairDigest: "projects-digest"},
		nil,
		now,
	)
	if len(cloned.RepairStops) != 2 || len(state.RepairStops) != 2 {
		t.Fatalf("success on a third target lost stops: original=%#v copy=%#v", state, cloned)
	}
	delete(cloned.RepairStops, "semantic/facts.md")
	if len(state.RepairStops) != 2 {
		t.Fatal("cloned job state shares its repair stops")
	}
}
