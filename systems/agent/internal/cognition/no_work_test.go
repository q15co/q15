package cognition

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
)

func TestRunnerNoWorkSkipsModelAndApply(t *testing.T) {
	t.Parallel()

	for _, configured := range []bool{false, true} {
		t.Run(fmt.Sprintf("configured=%v", configured), func(t *testing.T) {
			var client agent.ModelClient
			model := &fakeModelClient{}
			if configured {
				client = model
			}
			applied := false
			job := fakeJob{
				jobType: semanticMemoryExtractionJobType,
				build: func(context.Context, ContextLoader) (Spec, error) {
					return Spec{NoWork: &NoWorkResult{
						Summary:  "Deferred memory repair.",
						Metadata: map[string]string{"memory_repair_refusals": "1"},
					}}, nil
				},
				apply: func(context.Context, ContextLoader, JobOutput) (ParsedResult, error) {
					applied = true
					return ParsedResult{}, nil
				},
			}
			result, err := NewRunner(client, nil, nil, nil).Run(context.Background(), job, nil)
			if err != nil || !result.NoWork || result.Summary != "Deferred memory repair." ||
				result.Metadata["memory_repair_refusals"] != "1" {
				t.Fatalf("Run() = %#v, %v, want typed no-work result", result, err)
			}
			if applied || len(model.callMsgs) != 0 {
				t.Fatalf("applied=%v model calls=%d, want neither", applied, len(model.callMsgs))
			}
		})
	}
}

func TestControllerNoWorkPreservesBacklogAndConsumesSchedule(t *testing.T) {
	t.Parallel()

	for _, head := range []int64{0, 100} {
		t.Run(fmt.Sprintf("head=%d", head), func(t *testing.T) {
			ctx := context.Background()
			store := newFakeControllerStore()
			now := time.Now().UTC().Truncate(time.Minute)
			store.setHead(head, now)
			priorSuccess := now.Add(-time.Hour)
			priorFailure := now.Add(-time.Minute)
			initial := JobState{
				DirtySinceSeq: 1, DirtySinceAt: priorSuccess,
				LastSuccessAt: priorSuccess, LastSuccessInputSeq: 0,
				LastFailureAt: priorFailure, ConsecutiveFailures: stallAlertFailures,
			}
			store.states[semanticMemoryExtractionJobType] = initial
			alert := StallAlertPath(semanticMemoryExtractionJobType)
			store.artifacts[alert] = "Existing failure alert."
			model := &fakeModelClient{}
			controller, err := NewController(
				NewRunner(model, nil, []string{"cognition"}, &spyLoader{}), store, &spyLoader{},
				JobRegistration{
					NewJob: func() JobDefinition {
						return fakeJob{
							jobType: semanticMemoryExtractionJobType,
							build: func(context.Context, ContextLoader) (Spec, error) {
								return Spec{
									NoWork: &NoWorkResult{Summary: "Memory repair refused."},
								}, nil
							},
						}
					},
					Policy: TriggerPolicy{
						Schedule: []ScheduleRule{{ID: "scheduled", Spec: "* * * * *"}},
						State: []StateRule{
							{
								ID: "dirty",
								Evaluate: func(context.Context, Snapshot, JobState) (bool, string, error) {
									return true, "dirty", nil
								},
							},
						},
					},
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			controller.started = now.Add(-time.Minute)
			if err := controller.drain(ctx, false, true, true); err != nil {
				t.Fatal(err)
			}
			state, err := store.LoadJobState(ctx, semanticMemoryExtractionJobType)
			if err != nil {
				t.Fatal(err)
			}
			if !state.LastRunNoWork || state.LastRunInputSeq != head ||
				state.DirtySinceSeq != initial.DirtySinceSeq || !state.DirtySinceAt.Equal(initial.DirtySinceAt) ||
				!state.LastSuccessAt.Equal(
					initial.LastSuccessAt,
				) || state.LastSuccessInputSeq != initial.LastSuccessInputSeq ||
				!state.LastFailureAt.Equal(
					initial.LastFailureAt,
				) || state.ConsecutiveFailures != initial.ConsecutiveFailures {
				t.Fatalf(
					"state after no-work = %#v, want unchanged success/failure/backlog accounting",
					state,
				)
			}
			if !state.LastScheduledFor["scheduled"].Equal(now) {
				t.Fatalf("schedule = %#v, want consumed slot %v", state.LastScheduledFor, now)
			}
			if checkpoint := store.semanticCheckpoint(); checkpoint != (SemanticExtractionCheckpoint{}) {
				t.Fatalf("semantic checkpoint = %#v, want untouched zero", checkpoint)
			}
			if len(store.runRecords()) != 1 || len(model.callMsgs) != 0 ||
				store.artifacts[alert] == "" {
				t.Fatalf(
					"records=%d model calls=%d alert=%q, want one no-work record, no calls and retained alert",
					len(store.runRecords()),
					len(model.callMsgs),
					store.artifacts[alert],
				)
			}
			if _, pending, err := controller.nextPendingRun(ctx, false, true, true); err != nil ||
				pending {
				t.Fatalf("same-head pending=%v err=%v, want suppressed", pending, err)
			}
			store.setHead(head+1, now.Add(time.Second))
			if _, pending, err := controller.nextPendingRun(ctx, false, false, true); err != nil ||
				!pending {
				t.Fatalf("new-head pending=%v err=%v, want retry retaining backlog", pending, err)
			}
		})
	}
}
