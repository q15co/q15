package cognition

import (
	"context"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
)

func TestCognitionRejectsOversizedInputBeforeProviderCall(t *testing.T) {
	model := &fakeModelClient{results: []agent.ModelClientResult{assistantResult("finished")}}
	runner := NewRunner(model, nil, []string{"first", "second", "third"}, &spyLoader{})
	job := fakeJob{
		jobType: "synthetic.check",
		build: func(context.Context, ContextLoader) (Spec, error) {
			return Spec{
				Objective:          strings.Repeat("synthetic evidence ", 400000),
				CompletionContract: "Return a result.",
			}, nil
		},
	}
	if _, err := runner.Run(context.Background(), job, nil); err == nil {
		t.Fatal("oversized request reached completion")
	}
	if len(model.callModels) != 0 {
		t.Fatalf("oversized request reached providers: %v", model.callModels)
	}
}

func TestCognitionCanCompleteAfterOversizedOptionalToolResult(t *testing.T) {
	huge := strings.Repeat("synthetic evidence ", 400000)
	registry, err := agent.NewToolRegistry(
		testTool{run: func(context.Context, string) (string, error) { return huge, nil }},
	)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	model := &fakeModelClient{
		complete: func(_ context.Context, _ string, messages []conversation.Message, _ []agent.ToolDefinition) (agent.ModelClientResult, error) {
			calls++
			if calls == 1 {
				return agent.ModelClientResult{
					Messages: []conversation.Message{
						conversation.AssistantMessage(
							conversation.ToolCall("read-1", "noop", `{}`),
						),
					},
				}, nil
			}
			for _, message := range messages {
				for _, part := range message.Parts {
					if len(part.Content) > 32*1024 {
						t.Fatal("oversized tool output reached the model")
					}
				}
			}
			last := messages[len(messages)-1]
			if len(last.Parts) == 0 || !last.Parts[0].IsError ||
				!strings.Contains(last.Parts[0].Content, "omitted") {
				t.Fatalf("missing explicit omission: %#v", last)
			}
			return assistantResult("finished"), nil
		},
	}
	runner := NewRunner(model, registry, []string{"first"}, &spyLoader{})
	job := fakeJob{
		jobType: "synthetic.check",
		build: func(context.Context, ContextLoader) (Spec, error) {
			return Spec{
				Objective:          "Decide from the supplied synthetic chunk.",
				CompletionContract: "Return a result.",
				ExposeTools:        true,
			}, nil
		},
	}
	result, err := runner.Run(context.Background(), job, nil)
	if err != nil || result.FinalText != "finished" || calls != 2 {
		t.Fatalf(
			"bounded tool failure prevented completion: calls=%d result=%#v error=%v",
			calls,
			result,
			err,
		)
	}
}

func TestCognitionRequestRejectionRecoversJournal(t *testing.T) {
	loader := &boundedAttemptRecoveryLoader{spyLoader: &spyLoader{}, pending: true}
	model := &fakeModelClient{}
	runner := NewRunnerWithEngine(agent.NewEngine(model, nil, []string{"synthetic"}), loader)
	job := fakeJob{
		jobType: "synthetic.check",
		build: func(context.Context, ContextLoader) (Spec, error) {
			return Spec{
				Objective:          strings.Repeat("x", 1024*1024),
				CompletionContract: "Return a result.",
				MemoryRecoveryID:   "synthetic-recovery",
			}, nil
		},
	}
	if _, err := runner.Run(context.Background(), job, nil); err == nil {
		t.Fatal("expected request rejection")
	}
	if len(model.callModels) != 0 || loader.pending || loader.recovered != 1 {
		t.Fatalf(
			"request rejection did not recover: calls=%v pending=%v recovered=%d",
			model.callModels,
			loader.pending,
			loader.recovered,
		)
	}
}
