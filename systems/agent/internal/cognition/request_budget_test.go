package cognition

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

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
	if _, err := runner.Run(context.Background(), job, nil); err == nil ||
		!strings.Contains(err.Error(), "fixed_prompt_bytes=") ||
		!strings.Contains(err.Error(), "tool_definition_bytes=") {
		t.Fatalf("missing budget components: %v", err)
	}
	if len(model.callModels) != 0 {
		t.Fatalf("oversized request reached providers: %v", model.callModels)
	}
}

func TestVerificationReviewBoundsWholeConflictGroups(t *testing.T) {
	var content strings.Builder
	for i := 1; i <= 400; i += 2 {
		for _, pair := range [][2]int{{i, i + 1}, {i + 1, i}} {
			fmt.Fprintf(
				&content,
				"- [c-2026-%04d] %s contradicts c-2026-%04d.\n  {status: conflicted, kind: fact, confidence: uncertain, src: t1, rev: 2026-10-17}\n",
				pair[0],
				strings.Repeat("x", 1024),
				pair[1],
			)
		}
	}
	loader := &verificationJobLoader{
		semantic: agent.SemanticMemory{
			Files: []agent.SemanticMemoryFile{
				{RelativePath: "semantic/facts.md", Content: content.String()},
			},
		},
	}
	spec, err := NewVerificationReviewRegistration().NewJob().Build(context.Background(), loader)
	if err != nil {
		t.Fatal(err)
	}
	section, ok := findPromptSection(spec.PromptSections, "conflicted_claims")
	if !ok {
		t.Fatal("missing conflict evidence")
	}
	encoded, _ := json.Marshal(section.Body)
	if len(encoded) > 64*1024 || len(spec.conflictGroups) == 0 || len(spec.conflictGroups) >= 200 ||
		!strings.Contains(section.Body, "omitted") {
		t.Fatalf("unbounded conflicts: bytes=%d groups=%d", len(encoded), len(spec.conflictGroups))
	}
	for _, group := range spec.conflictGroups {
		if len(group) != 2 {
			t.Fatalf("split conflict group: %v", conflictedClaimIDs(group))
		}
		for _, entry := range group {
			if !strings.Contains(section.Body, entry.claim.Text) {
				t.Fatalf("resolution required for omitted claim %s", entry.claim.ID)
			}
		}
	}
}

func TestVerificationReviewBoundsLargeEvidenceBeforeProviderCall(t *testing.T) {
	large := strings.Repeat("<synthetic 東京>\x01 ", 60000)
	loader := &verificationJobLoader{
		core: agent.CoreMemory{
			Files: []agent.CoreMemoryFile{{RelativePath: "core/AGENT.md", Content: large}},
		},
		working:   agent.WorkingMemory{Content: large},
		recent:    []conversation.Message{conversation.UserMessage(large)},
		artifacts: map[string]Artifact{VerificationReviewPath: {Content: large}},
	}
	model := &fakeModelClient{
		complete: func(_ context.Context, _ string, messages []conversation.Message, tools []agent.ToolDefinition) (agent.ModelClientResult, error) {
			encoded, err := json.Marshal(struct {
				Messages []conversation.Message `json:"messages"`
				Tools    []agent.ToolDefinition `json:"tools"`
			}{messages, tools})
			if err != nil || len(encoded) > defaultCognitionMaxRequestBytes {
				t.Fatalf("unbounded verification request: bytes=%d error=%v", len(encoded), err)
			}
			if !strings.Contains(conversation.TextValue(messages[0]), "omitted") {
				t.Fatal("partial evidence was not disclosed")
			}
			return assistantResult(
				"# Review\n\nPartial evidence; further verification remains open.",
			), nil
		},
	}
	_, err := NewRunner(
		model,
		nil,
		[]string{"first"},
		loader,
	).Run(context.Background(), NewVerificationReviewRegistration().NewJob(), nil)
	if err != nil || len(loader.storeCalls) != 1 || len(model.callModels) != 1 {
		t.Fatalf(
			"verification did not run: stored=%d calls=%d error=%v",
			len(loader.storeCalls),
			len(model.callModels),
			err,
		)
	}
}

func TestVerificationReviewOmitsIndivisibleConflictAndPreservesBaseline(t *testing.T) {
	prior := strings.Repeat("prior review 東京\x01 ", 20000)
	loader := &verificationJobLoader{
		semantic: agent.SemanticMemory{Files: []agent.SemanticMemoryFile{{
			RelativePath: "semantic/facts.md",
			Content: "- [c-2026-0001] " + strings.Repeat("x", 100000) + ".\n" +
				"  {status: conflicted, kind: fact, confidence: uncertain, src: t1, rev: 2026-10-17}\n",
		}}},
		artifacts: map[string]Artifact{VerificationReviewPath: {Content: prior}},
	}
	job := NewVerificationReviewRegistration().NewJob()
	spec, err := job.Build(context.Background(), loader)
	if err != nil {
		t.Fatal(err)
	}
	section, ok := findPromptSection(spec.PromptSections, "conflicted_claims")
	if !ok || !strings.Contains(section.Body, "1 conflict groups omitted") ||
		len(spec.conflictGroups) != 0 {
		t.Fatal("indivisible conflict was partially supplied or required for completion")
	}
	previous, present := verificationReviewBaseline(spec)
	if !present || previous != strings.TrimSpace(prior) {
		t.Fatal("bounded prompt replaced the authoritative artifact baseline")
	}
	for _, section := range spec.PromptSections {
		if !utf8.ValidString(section.Body) {
			t.Fatalf("invalid UTF-8 in %s", section.Name)
		}
	}
	_, err = job.ApplyResult(
		context.Background(),
		loader,
		JobOutput{
			Spec:      spec,
			FinalText: "# Review\n\nConflict evidence was omitted; resolution remains open.",
		},
	)
	if err != nil || len(loader.storeCalls) != 1 {
		t.Fatalf("omitted conflict prevented review completion: %v", err)
	}
}

func TestCognitionBudgetFailureNamesSourceChunkWithoutLeakingContent(t *testing.T) {
	secret := strings.Repeat("private-synthetic-evidence", 20000)
	model := &fakeModelClient{}
	job := fakeJob{
		jobType: "synthetic.check",
		build: func(context.Context, ContextLoader) (Spec, error) {
			return Spec{Objective: "Check the chunk.", CompletionContract: "Return a result.",
				PromptSections: []agent.PromptSection{{Name: "repair_chunk", Body: secret}},
			}, nil
		},
	}
	_, err := NewRunner(
		model,
		nil,
		[]string{"first"},
		&spyLoader{},
	).Run(context.Background(), job, nil)
	if err == nil || !strings.Contains(err.Error(), "repair_chunk_bytes=") ||
		!strings.Contains(
			err.Error(),
			"prior_turn_bytes=",
		) || !strings.Contains(err.Error(), "evidence_bytes=") ||
		strings.Contains(err.Error(), "private-synthetic-evidence") || len(model.callModels) != 0 {
		t.Fatalf("incorrect budget diagnostics: %v", err)
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
