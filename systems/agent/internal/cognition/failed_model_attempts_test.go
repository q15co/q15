package cognition

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
)

func TestCognitionCapsFailedModelAttemptsAcrossTurns(t *testing.T) {
	for _, prebuilt := range []bool{false, true} {
		for _, withTurns := range []bool{false, true} {
			t.Run(
				fmt.Sprintf("prebuilt=%t/tool_turns=%t", prebuilt, withTurns),
				func(t *testing.T) {
					calls := 0
					model := &fakeModelClient{
						complete: func(context.Context, string, []conversation.Message, []agent.ToolDefinition) (agent.ModelClientResult, error) {
							calls++
							if withTurns && (calls == 2 || calls == 4) {
								return agent.ModelClientResult{
									Messages: []conversation.Message{
										conversation.AssistantMessage(
											conversation.ToolCall(
												fmt.Sprintf("call-%d", calls),
												"noop",
												fmt.Sprintf(`{"n":%d}`, calls),
											),
										),
									},
								}, nil
							}
							return agent.ModelClientResult{}, fmt.Errorf(
								"synthetic rejection %d",
								calls,
							)
						},
					}
					registry, err := agent.NewToolRegistry(testTool{})
					if err != nil {
						t.Fatal(err)
					}
					refs := []string{"first", "second", "third", "fourth"}
					runner := NewRunner(model, registry, refs, &spyLoader{})
					if prebuilt {
						runner = NewRunnerWithEngine(
							agent.NewEngine(model, registry, refs),
							&spyLoader{},
						)
					}
					job := fakeJob{
						jobType: "synthetic.check",
						build: func(context.Context, ContextLoader) (Spec, error) {
							return Spec{
								Objective:          "Check synthetic input.",
								CompletionContract: "Return a result.",
								ExposeTools:        true,
							}, nil
						},
					}
					_, err = runner.Run(context.Background(), job, nil)
					if err == nil {
						t.Fatal("expected model failure")
					}
					want := []string{"first", "second", "third"}
					if withTurns {
						want = []string{"first", "second", "first", "second", "first"}
					}
					if !reflect.DeepEqual(model.callModels, want) {
						t.Fatalf("attempts = %v, want %v", model.callModels, want)
					}
					failures := attemptFailuresFromError(err)
					if len(failures) != 3 {
						t.Fatalf("failure records=%#v, want all three failures", failures)
					}
					if withTurns &&
						(failures[0].Error != "synthetic rejection 1" || failures[1].Error != "synthetic rejection 3" || failures[2].Error != "synthetic rejection 5") {
						t.Fatalf("lost failures across successful turns: %#v", failures)
					}
				},
			)
		}
	}
}

func TestCognitionSuccessfulToolTurnsDoNotConsumeFailureBudget(t *testing.T) {
	calls := 0
	model := &fakeModelClient{
		complete: func(context.Context, string, []conversation.Message, []agent.ToolDefinition) (agent.ModelClientResult, error) {
			calls++
			if calls <= 5 {
				return agent.ModelClientResult{
					Messages: []conversation.Message{
						conversation.AssistantMessage(
							conversation.ToolCall(
								fmt.Sprintf("call-%d", calls),
								"noop",
								fmt.Sprintf(`{"n":%d}`, calls),
							),
						),
					},
				}, nil
			}
			return assistantResult("finished"), nil
		},
	}
	registry, err := agent.NewToolRegistry(testTool{})
	if err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(model, registry, []string{"first"}, &spyLoader{})
	job := fakeJob{
		jobType: "synthetic.check",
		build: func(context.Context, ContextLoader) (Spec, error) {
			return Spec{
				Objective:          "Check synthetic input.",
				CompletionContract: "Return a result.",
				ExposeTools:        true,
			}, nil
		},
	}
	result, err := runner.Run(context.Background(), job, nil)
	if err != nil || calls != 6 || result.FinalText != "finished" {
		t.Fatalf(
			"successful tool turns consumed budget: calls=%d result=%#v error=%v",
			calls,
			result,
			err,
		)
	}
}

func TestCognitionFailureBudgetResetsForEachRun(t *testing.T) {
	model := &fakeModelClient{
		complete: func(context.Context, string, []conversation.Message, []agent.ToolDefinition) (agent.ModelClientResult, error) {
			return agent.ModelClientResult{}, errors.New("synthetic rejection")
		},
	}
	runner := NewRunnerWithEngine(
		agent.NewEngine(model, nil, []string{"first", "second", "third", "fourth"}),
		&spyLoader{},
	)
	job := fakeJob{
		jobType: "synthetic.check",
		build: func(context.Context, ContextLoader) (Spec, error) {
			return Spec{
				Objective:          "Check synthetic input.",
				CompletionContract: "Return a result.",
			}, nil
		},
	}
	for i := 0; i < 2; i++ {
		if _, err := runner.Run(context.Background(), job, nil); err == nil {
			t.Fatal("expected model failure")
		}
	}
	if len(model.callModels) != 6 {
		t.Fatalf("attempts=%v, want three per run", model.callModels)
	}
}

type boundedAttemptRecoveryLoader struct {
	*spyLoader
	pending   bool
	recovered int
}

func (l *boundedAttemptRecoveryLoader) RecoverMemory(context.Context, string) error {
	l.pending = false
	l.recovered++
	return nil
}

func TestCognitionReleasesRecoveryAfterFailureBudget(t *testing.T) {
	loader := &boundedAttemptRecoveryLoader{spyLoader: &spyLoader{}, pending: true}
	model := &fakeModelClient{
		complete: func(context.Context, string, []conversation.Message, []agent.ToolDefinition) (agent.ModelClientResult, error) {
			if !loader.pending {
				t.Fatal("recovery journal released between attempts")
			}
			return agent.ModelClientResult{}, errors.New("synthetic rejection")
		},
	}
	runner := NewRunner(model, nil, []string{"first", "second", "third", "fourth"}, loader)
	job := fakeJob{
		jobType: "synthetic.check",
		build: func(context.Context, ContextLoader) (Spec, error) {
			return Spec{
				Objective:          "Check synthetic input.",
				CompletionContract: "Return a result.",
				MemoryRecoveryID:   "synthetic-recovery",
			}, nil
		},
	}
	if _, err := runner.Run(context.Background(), job, nil); err == nil {
		t.Fatal("expected model failure")
	}
	if len(model.callModels) != 3 || loader.pending || loader.recovered != 1 {
		t.Fatalf(
			"cleanup: attempts=%v pending=%v recoveries=%d",
			model.callModels,
			loader.pending,
			loader.recovered,
		)
	}
}
