package cognition

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/modelselection"
)

// ContextLoader provides explicit access to state a cognition job may opt into.
type ContextLoader interface {
	LoadCoreMemory(ctx context.Context) (agent.CoreMemory, error)
	LoadSemanticMemory(ctx context.Context) (agent.SemanticMemory, error)
	LoadWorkingMemory(ctx context.Context) (agent.WorkingMemory, error)
	LoadSkillCatalog(ctx context.Context) (agent.SkillCatalog, error)
	LoadRecentMessages(ctx context.Context, turns int) ([]conversation.Message, error)
	LoadLatestMessages(ctx context.Context, turns int) ([]conversation.Message, error)
	LoadHead(ctx context.Context) (int64, time.Time, error)
	LoadConsolidationCheckpoint(ctx context.Context) (ConsolidationCheckpoint, error)
	LoadCognitionArtifact(ctx context.Context, relativePath string) (Artifact, error)
	StoreCognitionArtifact(ctx context.Context, artifact Artifact) error
}

// Artifact is one job-owned persisted artifact stored under
// /memory/cognition/.
type Artifact struct {
	RelativePath string
	Content      string
}

// JobDefinition describes one typed cognition job.
type JobDefinition interface {
	Type() string
	Build(ctx context.Context, loader ContextLoader) (Spec, error)
	ApplyResult(ctx context.Context, loader ContextLoader, output JobOutput) (ParsedResult, error)
}

// JobOutput is the validated raw engine output for one cognition run.
type JobOutput struct {
	Type      string
	Spec      Spec
	FinalText string
	Messages  []conversation.Message
	ModelRef  string
	Turn      int
}

// ParsedResult is the job-specific structured interpretation of the raw output.
type ParsedResult struct {
	Summary  string
	Metadata map[string]string
}

// Result is the completed structured outcome for one cognition job.
type Result struct {
	Type      string
	Summary   string
	Metadata  map[string]string
	FinalText string
	Messages  []conversation.Message
	ModelRef  string
	Turn      int
}

// Runner executes typed cognition jobs on the shared model/tool engine.
type Runner struct {
	engine *agent.Engine
	loader ContextLoader
}

// NewRunner constructs a Runner with default planner behavior.
func NewRunner(
	modelClient agent.ModelClient,
	tools agent.ToolRegistry,
	modelRefs []string,
	loader ContextLoader,
) *Runner {
	return NewRunnerWithPlanner(modelClient, nil, tools, modelRefs, loader)
}

// NewRunnerWithPlanner constructs a Runner with an explicit model-selection
// planner.
func NewRunnerWithPlanner(
	modelClient agent.ModelClient,
	planner modelselection.Planner,
	tools agent.ToolRegistry,
	modelRefs []string,
	loader ContextLoader,
) *Runner {
	return NewRunnerWithEngine(
		agent.NewEngineWithPlanner(modelClient, planner, tools, modelRefs),
		loader,
	)
}

// NewRunnerWithEngine constructs a Runner from an already assembled shared
// engine.
func NewRunnerWithEngine(engine *agent.Engine, loader ContextLoader) *Runner {
	return &Runner{
		engine: engine,
		loader: loader,
	}
}

// Run executes one typed cognition job.
func (r *Runner) Run(
	ctx context.Context,
	job JobDefinition,
	observer agent.RunObserver,
) (Result, error) {
	if r == nil || r.engine == nil {
		err := fmt.Errorf("cognition runner is not configured")
		notifyRunEvent(ctx, observer, agent.RunEvent{
			Type: agent.RunEventRunFailed,
			Err:  err,
		})
		return Result{}, err
	}
	if job == nil {
		err := fmt.Errorf("cognition job is required")
		notifyRunEvent(ctx, observer, agent.RunEvent{
			Type: agent.RunEventRunFailed,
			Err:  err,
		})
		return Result{}, err
	}

	jobType := strings.TrimSpace(job.Type())
	if jobType == "" {
		err := fmt.Errorf("cognition job type is required")
		notifyRunEvent(ctx, observer, agent.RunEvent{
			Type: agent.RunEventRunFailed,
			Err:  err,
		})
		return Result{}, err
	}

	notifyRunEvent(ctx, observer, agent.RunEvent{
		Type: agent.RunEventRunStarted,
	})

	spec, err := job.Build(ctx, r.loader)
	if err != nil {
		notifyRunEvent(ctx, observer, agent.RunEvent{
			Type: agent.RunEventRunFailed,
			Err:  err,
		})
		return Result{}, fmt.Errorf("build cognition job %q: %w", jobType, err)
	}

	systemPrompt, err := renderPrompt(jobType, spec)
	if err != nil {
		notifyRunEvent(ctx, observer, agent.RunEvent{
			Type: agent.RunEventRunFailed,
			Err:  err,
		})
		return Result{}, fmt.Errorf("render cognition prompt for job %q: %w", jobType, err)
	}

	inputMessages := make([]conversation.Message, 0, 1+len(spec.InputMessages))
	inputMessages = append(inputMessages, conversation.SystemMessage(systemPrompt))
	inputMessages = append(inputMessages, conversation.CloneMessages(spec.InputMessages)...)

	runResult, err := r.engine.Run(ctx, agent.EngineRequest{
		Messages:           inputMessages,
		UseTools:           spec.ExposeTools,
		AllowedTools:       append([]string(nil), spec.AllowedTools...),
		ToolCallPolicy:     spec.ToolCallPolicy,
		RequireToolCalling: spec.RequireToolCalling,
		Observer:           observer,
	})
	if err != nil {
		notifyRunEvent(ctx, observer, agent.RunEvent{
			Type:      agent.RunEventRunFailed,
			Turn:      runResult.Turn,
			ModelRef:  runResult.ModelRef,
			FinalText: runResult.FinalText,
			Err:       err,
		})
		return Result{}, err
	}

	output := JobOutput{
		Type:      jobType,
		Spec:      spec,
		FinalText: runResult.FinalText,
		Messages:  conversation.CloneMessages(runResult.Messages),
		ModelRef:  runResult.ModelRef,
		Turn:      runResult.Turn,
	}
	parsed, err := job.ApplyResult(ctx, r.loader, output)
	if err != nil {
		notifyRunEvent(ctx, observer, agent.RunEvent{
			Type:      agent.RunEventRunFailed,
			Turn:      runResult.Turn,
			ModelRef:  runResult.ModelRef,
			FinalText: runResult.FinalText,
			Err:       err,
		})
		return Result{}, fmt.Errorf("apply cognition result for job %q: %w", jobType, err)
	}

	result := Result{
		Type:      jobType,
		Summary:   strings.TrimSpace(parsed.Summary),
		Metadata:  cloneMetadata(parsed.Metadata),
		FinalText: runResult.FinalText,
		Messages:  conversation.CloneMessages(runResult.Messages),
		ModelRef:  runResult.ModelRef,
		Turn:      runResult.Turn,
	}
	notifyRunEvent(ctx, observer, agent.RunEvent{
		Type:      agent.RunEventRunFinished,
		Turn:      result.Turn,
		ModelRef:  result.ModelRef,
		FinalText: result.FinalText,
	})
	return result, nil
}

func cloneMetadata(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func notifyRunEvent(ctx context.Context, observer agent.RunObserver, event agent.RunEvent) {
	if observer == nil {
		return
	}
	if event.At.IsZero() {
		event.At = time.Now()
	}
	observer.OnRunEvent(ctx, event)
}
