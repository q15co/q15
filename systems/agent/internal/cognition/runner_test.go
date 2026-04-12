package cognition

import (
	"context"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/modelselection"
)

type fakeModelClient struct {
	results    []agent.ModelClientResult
	errors     []error
	callMsgs   [][]conversation.Message
	callModels []string
	callTools  [][]agent.ToolDefinition
	complete   func(
		context.Context,
		string,
		[]conversation.Message,
		[]agent.ToolDefinition,
	) (agent.ModelClientResult, error)
}

func (f *fakeModelClient) Complete(
	ctx context.Context,
	model string,
	messages []conversation.Message,
	tools []agent.ToolDefinition,
) (agent.ModelClientResult, error) {
	f.callMsgs = append(f.callMsgs, conversation.CloneMessages(messages))
	f.callModels = append(f.callModels, model)
	if len(tools) > 0 {
		copied := make([]agent.ToolDefinition, len(tools))
		copy(copied, tools)
		f.callTools = append(f.callTools, copied)
	} else {
		f.callTools = append(f.callTools, nil)
	}
	if f.complete != nil {
		return f.complete(ctx, model, messages, tools)
	}
	if len(f.errors) > 0 {
		err := f.errors[0]
		f.errors = f.errors[1:]
		if err != nil {
			return agent.ModelClientResult{}, err
		}
	}
	if len(f.results) == 0 {
		return agent.ModelClientResult{}, nil
	}
	out := f.results[0]
	f.results = f.results[1:]
	return out, nil
}

type fakePlanner struct {
	requirements []modelselection.Requirements
}

func (f *fakePlanner) Plan(
	modelRefs []string,
	requirements modelselection.Requirements,
) (modelselection.Plan, error) {
	f.requirements = append(f.requirements, requirements)
	return modelselection.Plan{
		EligibleRefs: append([]string(nil), modelRefs...),
	}, nil
}

type spyLoader struct {
	appendCalls int
}

func (s *spyLoader) LoadCoreMemory(context.Context) (agent.CoreMemory, error) {
	return agent.CoreMemory{}, nil
}

func (s *spyLoader) LoadWorkingMemory(context.Context) (agent.WorkingMemory, error) {
	return agent.WorkingMemory{}, nil
}

func (s *spyLoader) LoadSkillCatalog(context.Context) (agent.SkillCatalog, error) {
	return agent.SkillCatalog{}, nil
}

func (s *spyLoader) LoadRecentMessages(
	context.Context,
	int,
) ([]conversation.Message, error) {
	return nil, nil
}

func (s *spyLoader) LoadCognitionArtifact(
	context.Context,
	string,
) (Artifact, error) {
	return Artifact{}, nil
}

func (s *spyLoader) StoreCognitionArtifact(
	context.Context,
	Artifact,
) error {
	return nil
}

func (s *spyLoader) AppendTurn(context.Context, []conversation.Message) error {
	s.appendCalls++
	return nil
}

type fakeJob struct {
	jobType string
	build   func(context.Context, ContextLoader) (Spec, error)
	apply   func(context.Context, ContextLoader, JobOutput) (ParsedResult, error)
}

func (f fakeJob) Type() string {
	return f.jobType
}

func (f fakeJob) Build(ctx context.Context, loader ContextLoader) (Spec, error) {
	if f.build != nil {
		return f.build(ctx, loader)
	}
	return Spec{}, nil
}

func (f fakeJob) ApplyResult(
	ctx context.Context,
	loader ContextLoader,
	output JobOutput,
) (ParsedResult, error) {
	if f.apply != nil {
		return f.apply(ctx, loader, output)
	}
	return ParsedResult{}, nil
}

type testTool struct {
	def agent.ToolDefinition
	run func(context.Context, string) (string, error)
}

func (t testTool) Definition() agent.ToolDefinition {
	if t.def.Name != "" {
		return t.def
	}
	return agent.ToolDefinition{Name: "noop"}
}

func (t testTool) Run(ctx context.Context, arguments string) (string, error) {
	if t.run != nil {
		return t.run(ctx, arguments)
	}
	return "ok", nil
}

func assistantResult(text string) agent.ModelClientResult {
	return agent.ModelClientResult{
		Messages: []conversation.Message{
			conversation.AssistantMessage(conversation.Text(text, "")),
		},
	}
}

func TestRunnerRunsWithoutUserTurnAndDoesNotPersistTranscript(t *testing.T) {
	model := &fakeModelClient{
		results: []agent.ModelClientResult{
			assistantResult("status: ok"),
		},
	}
	loader := &spyLoader{}
	runner := NewRunner(model, nil, []string{"m1"}, loader)

	job := fakeJob{
		jobType: "working_memory.consolidate",
		build: func(context.Context, ContextLoader) (Spec, error) {
			return Spec{
				Objective:          "Condense active state into a bounded internal update.",
				CompletionContract: "Return `status: ok` when complete.",
			}, nil
		},
		apply: func(_ context.Context, _ ContextLoader, output JobOutput) (ParsedResult, error) {
			if output.FinalText != "status: ok" {
				t.Fatalf("output.FinalText = %q, want %q", output.FinalText, "status: ok")
			}
			return ParsedResult{
				Summary: "parsed",
				Metadata: map[string]string{
					"status": "ok",
				},
			}, nil
		},
	}

	result, err := runner.Run(context.Background(), job, nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Type != "working_memory.consolidate" {
		t.Fatalf("result.Type = %q, want %q", result.Type, "working_memory.consolidate")
	}
	if result.Metadata["status"] != "ok" {
		t.Fatalf("result.Metadata = %#v", result.Metadata)
	}
	if loader.appendCalls != 0 {
		t.Fatalf("appendCalls = %d, want 0", loader.appendCalls)
	}
	if len(model.callMsgs) != 1 {
		t.Fatalf("model calls = %d, want 1", len(model.callMsgs))
	}
	if len(model.callMsgs[0]) != 1 {
		t.Fatalf("model input len = %d, want 1", len(model.callMsgs[0]))
	}
	if model.callMsgs[0][0].Role != conversation.SystemRole {
		t.Fatalf("model input[0].Role = %q, want system", model.callMsgs[0][0].Role)
	}
}

func TestRunnerRequiresToolCallingWhenJobRequestsIt(t *testing.T) {
	model := &fakeModelClient{
		results: []agent.ModelClientResult{
			assistantResult("status: ok"),
		},
	}
	planner := &fakePlanner{}
	registry, err := agent.NewToolRegistry(testTool{})
	if err != nil {
		t.Fatalf("NewToolRegistry() error = %v", err)
	}

	runner := NewRunnerWithPlanner(
		model,
		planner,
		registry,
		[]string{"m1"},
		&spyLoader{},
	)
	job := fakeJob{
		jobType: "verification.check",
		build: func(context.Context, ContextLoader) (Spec, error) {
			return Spec{
				Objective:          "Inspect state with tool access available.",
				CompletionContract: "Return `status: ok` when complete.",
				ExposeTools:        true,
				RequireToolCalling: true,
			}, nil
		},
		apply: func(context.Context, ContextLoader, JobOutput) (ParsedResult, error) {
			return ParsedResult{Summary: "ok"}, nil
		},
	}

	if _, err := runner.Run(context.Background(), job, nil); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(planner.requirements) != 1 {
		t.Fatalf("planned requirements len = %d, want 1", len(planner.requirements))
	}
	if !planner.requirements[0].ToolCalling {
		t.Fatalf("requirements = %#v, want tool_calling", planner.requirements[0])
	}
	if len(model.callTools) != 1 || len(model.callTools[0]) != 1 {
		t.Fatalf("callTools = %#v, want one exposed tool", model.callTools)
	}
}

func TestRunnerExposesToolsWithoutRequiringToolCallingByDefault(t *testing.T) {
	model := &fakeModelClient{
		results: []agent.ModelClientResult{
			assistantResult("status: ok"),
		},
	}
	planner := &fakePlanner{}
	registry, err := agent.NewToolRegistry(testTool{})
	if err != nil {
		t.Fatalf("NewToolRegistry() error = %v", err)
	}

	runner := NewRunnerWithPlanner(
		model,
		planner,
		registry,
		[]string{"m1"},
		&spyLoader{},
	)
	job := fakeJob{
		jobType: "verification.check",
		build: func(context.Context, ContextLoader) (Spec, error) {
			return Spec{
				Objective:          "Inspect state with tool access available.",
				CompletionContract: "Return `status: ok` when complete.",
				ExposeTools:        true,
			}, nil
		},
		apply: func(context.Context, ContextLoader, JobOutput) (ParsedResult, error) {
			return ParsedResult{Summary: "ok"}, nil
		},
	}

	if _, err := runner.Run(context.Background(), job, nil); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(planner.requirements) != 1 {
		t.Fatalf("planned requirements len = %d, want 1", len(planner.requirements))
	}
	if planner.requirements[0].ToolCalling {
		t.Fatalf("requirements = %#v, want tool_calling=false", planner.requirements[0])
	}
	if len(model.callTools) != 1 || len(model.callTools[0]) != 1 {
		t.Fatalf("callTools = %#v, want one exposed tool", model.callTools)
	}
}

func TestRunnerReportsParseFailures(t *testing.T) {
	model := &fakeModelClient{
		results: []agent.ModelClientResult{
			assistantResult("unexpected"),
		},
	}
	runner := NewRunner(model, nil, []string{"m1"}, &spyLoader{})

	job := fakeJob{
		jobType: "memory.verify",
		build: func(context.Context, ContextLoader) (Spec, error) {
			return Spec{
				Objective:          "Verify that parsing errors surface cleanly.",
				CompletionContract: "Return `status: ok` when complete.",
			}, nil
		},
		apply: func(_ context.Context, _ ContextLoader, output JobOutput) (ParsedResult, error) {
			if !strings.Contains(output.FinalText, "unexpected") {
				t.Fatalf("output.FinalText = %q", output.FinalText)
			}
			return ParsedResult{}, context.DeadlineExceeded
		},
	}

	if _, err := runner.Run(context.Background(), job, nil); err == nil {
		t.Fatal("Run() error = nil, want apply failure")
	}
}

func TestRunnerForwardsAllowedToolsAndLoaderToApplyResult(t *testing.T) {
	model := &fakeModelClient{
		results: []agent.ModelClientResult{
			assistantResult("verification notes"),
		},
	}
	registry, err := agent.NewToolRegistry(
		testTool{def: agent.ToolDefinition{Name: "read_file"}},
		testTool{def: agent.ToolDefinition{Name: "web_fetch"}},
	)
	if err != nil {
		t.Fatalf("NewToolRegistry() error = %v", err)
	}

	loader := &spyLoader{}
	runner := NewRunner(model, registry, []string{"m1"}, loader)

	job := fakeJob{
		jobType: "verification_review",
		build: func(context.Context, ContextLoader) (Spec, error) {
			return Spec{
				Objective:          "Review state with bounded tools.",
				CompletionContract: "Return notes.",
				ExposeTools:        true,
				AllowedTools:       []string{"web_fetch"},
			}, nil
		},
		apply: func(_ context.Context, gotLoader ContextLoader, output JobOutput) (ParsedResult, error) {
			if gotLoader != loader {
				t.Fatalf("loader = %#v, want %#v", gotLoader, loader)
			}
			if output.FinalText != "verification notes" {
				t.Fatalf("output.FinalText = %q, want %q", output.FinalText, "verification notes")
			}
			return ParsedResult{Summary: "ok"}, nil
		},
	}

	if _, err := runner.Run(context.Background(), job, nil); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(model.callTools) != 1 || len(model.callTools[0]) != 1 {
		t.Fatalf("callTools = %#v, want one filtered tool", model.callTools)
	}
	if model.callTools[0][0].Name != "web_fetch" {
		t.Fatalf("tool name = %q, want web_fetch", model.callTools[0][0].Name)
	}
}
