package cognition

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestControllerFailedContinuationRetriesWithoutNewAppendsAndDrainsTail(t *testing.T) {
	for _, jobType := range []string{workingMemoryConsolidationJobType, semanticMemoryExtractionJobType} {
		for _, failurePhase := range []string{"build", "apply"} {
			t.Run(jobType+"/"+failurePhase, func(t *testing.T) {
				ctx := context.Background()
				store := newFakeControllerStore()
				store.setHead(20, time.Now().UTC())
				store.states[jobType] = JobState{
					TranscriptPending: true, LastRunInputSeq: 8, LastSuccessInputSeq: 8,
					DirtySinceSeq: 9, DirtySinceAt: store.headAt,
				}
				fail := true
				applies := 0
				controller := newControllerForTest(t, store, JobRegistration{
					NewJob: func() JobDefinition {
						return fakeJob{
							jobType: jobType,
							build: func(context.Context, ContextLoader) (Spec, error) {
								if fail && failurePhase == "build" {
									return Spec{}, errors.New("build failed")
								}
								return Spec{
									Objective:          "Drain transcript.",
									CompletionContract: "Return status: ok.",
									transcriptProgress: &transcriptProgress{CompletedSeq: 20},
								}, nil
							},
							apply: func(context.Context, ContextLoader, JobOutput) (ParsedResult, error) {
								applies++
								if fail {
									return ParsedResult{}, errors.New("apply failed")
								}
								return ParsedResult{CheckpointSeq: 20}, nil
							},
						}
					},
				})
				pending, ok, err := controller.nextPendingRun(ctx, false, false, true)
				if err != nil || !ok || pending.cause.RuleID != "transcript_continuation" {
					t.Fatalf("initial continuation = %#v, %v, %v", pending, ok, err)
				}
				if err := controller.runPending(ctx, pending); err != nil {
					t.Fatal(err)
				}
				state := store.state(jobType)
				if !state.TranscriptPending || state.ConsecutiveFailures != 1 ||
					state.DirtySinceSeq != 9 {
					t.Fatalf("failed continuation lost pending work: %#v", state)
				}
				if state.LastRunInputSeq != 8 || state.LastSuccessInputSeq != 8 {
					t.Fatalf("failed run advanced the covered input boundary: %#v", state)
				}
				if store.checkpoint.LastConsolidatedSeq != 0 ||
					store.semanticExtractionCheckpoint.LastExtractedSeq != 0 {
					t.Fatal("failed run advanced a checkpoint")
				}
				// #379: the identical failure armed a backoff window; move
				// past it so the continuation lane re-fires.
				expireFailureBackoff(t, store, jobType)
				pending, ok, err = controller.nextPendingRun(ctx, false, false, true)
				if err != nil || !ok || pending.cause.RuleID != "transcript_continuation" ||
					pending.headSeq != 20 {
					t.Fatalf("retry without appends = %#v, %v, %v", pending, ok, err)
				}
				fail = false
				if err := controller.drain(ctx, false, false, true); err != nil {
					t.Fatal(err)
				}
				state = store.state(jobType)
				if state.TranscriptPending || state.DirtySinceSeq != 0 ||
					state.ConsecutiveFailures != 0 ||
					state.LastRunInputSeq != 20 ||
					state.LastSuccessInputSeq != 20 {
					t.Fatalf("successful retry did not drain the tail: %#v", state)
				}
				if jobType == workingMemoryConsolidationJobType &&
					store.checkpoint.LastConsolidatedSeq != 20 {
					t.Fatalf("consolidation checkpoint = %#v", store.checkpoint)
				}
				if jobType == semanticMemoryExtractionJobType &&
					store.semanticExtractionCheckpoint.LastExtractedSeq != 20 {
					t.Fatalf("semantic checkpoint = %#v", store.semanticExtractionCheckpoint)
				}
				records := store.runRecords()
				if len(records) != 2 || records[0].Succeeded || !records[1].Succeeded ||
					applies == 0 {
					t.Fatalf("retry records = %#v, apply calls = %d", records, applies)
				}
			})
		}
	}
}
