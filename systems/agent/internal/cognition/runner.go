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

const (
	// defaultCognitionMaxTurns caps the model/tool turns for one cognition run.
	defaultCognitionMaxTurns = 32
)

// ContextLoader provides explicit access to state a cognition job may opt into.
type ContextLoader interface {
	LoadCoreMemory(ctx context.Context) (agent.CoreMemory, error)
	LoadSemanticMemory(ctx context.Context) (agent.SemanticMemory, error)
	LoadWorkingMemory(ctx context.Context) (agent.WorkingMemory, error)
	LoadSkillCatalog(ctx context.Context) (agent.SkillCatalog, error)
	LoadRecentMessages(ctx context.Context, turns int) ([]conversation.Message, error)
	LoadLatestMessages(ctx context.Context, turns int) ([]conversation.Message, error)
	LoadMessagesSinceSeq(ctx context.Context, afterSeq int64) ([]conversation.Message, error)
	LoadHead(ctx context.Context) (int64, time.Time, error)
	LoadConsolidationCheckpoint(ctx context.Context) (ConsolidationCheckpoint, error)
	LoadSemanticExtractionCheckpoint(ctx context.Context) (SemanticExtractionCheckpoint, error)
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
	modelClient agent.ModelClient
	planner     modelselection.Planner
	tools       agent.ToolRegistry
	resolver    ModelRefResolver
	engineRef   *agent.Engine // optional prebuilt engine (NewRunnerWithEngine)
	loader      ContextLoader
	maxTurns    int
}

// ModelRefResolver returns the ordered model refs to consider for one
// cognition job type. Returning nil/empty falls back to the caller's default
// behavior. A nil resolver uses a constant ref list for every job.
type ModelRefResolver func(jobType string) []string

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
	return NewRunnerWithResolver(
		modelClient,
		planner,
		tools,
		ConstantModelRefResolver(modelRefs),
		loader,
	)
}

// NewRunnerWithEngine constructs a Runner from an already assembled shared
// engine. The engine's model refs are used unchanged for every job.
func NewRunnerWithEngine(engine *agent.Engine, loader ContextLoader) *Runner {
	if engine == nil {
		return &Runner{loader: loader}
	}
	return &Runner{
		modelClient: nil,
		engineRef:   engine,
		loader:      loader,
	}
}

// NewRunnerWithResolver constructs a Runner that resolves model refs per job
// type. This lets switch_cognition_model target a specific cognition job.
func NewRunnerWithResolver(
	modelClient agent.ModelClient,
	planner modelselection.Planner,
	tools agent.ToolRegistry,
	resolver ModelRefResolver,
	loader ContextLoader,
) *Runner {
	return &Runner{
		modelClient: modelClient,
		planner:     planner,
		tools:       tools,
		resolver:    resolver,
		loader:      loader,
		maxTurns:    defaultCognitionMaxTurns,
	}
}

// SetMaxTurns overrides the maximum model/tool turns for a cognition run.
func (r *Runner) SetMaxTurns(maxTurns int) {
	if r == nil || maxTurns <= 0 {
		return
	}
	r.maxTurns = maxTurns
}

// engineForJob selects the engine for one cognition job. A prebuilt engine
// (NewRunnerWithEngine) is used as-is; otherwise the per-job model ref resolver
// chooses the refs for this job type.
func (r *Runner) engineForJob(jobType string) (*agent.Engine, error) {
	if r.engineRef != nil {
		return r.engineRef, nil
	}
	if r.modelClient == nil {
		return nil, fmt.Errorf("cognition model client is not configured")
	}

	refs := r.modelRefsForJob(jobType)
	engine := agent.NewEngineWithPlanner(r.modelClient, r.planner, r.tools, refs)
	if r.maxTurns > 0 {
		engine.SetMaxTurns(r.maxTurns)
	}
	return engine, nil
}

func (r *Runner) modelRefsForJob(jobType string) []string {
	if r.resolver != nil {
		if refs := r.resolver(jobType); len(refs) > 0 {
			return refs
		}
	}
	return nil
}

// ConstantModelRefResolver returns a resolver that ignores the job type and
// always yields the provided refs.
func ConstantModelRefResolver(modelRefs []string) ModelRefResolver {
	refs := agent.StaticModelRefSource(modelRefs)
	return func(_ string) []string {
		return refs()
	}
}

// Run executes one typed cognition job.
func (r *Runner) Run(
	ctx context.Context,
	job JobDefinition,
	observer agent.RunObserver,
) (Result, error) {
	if r == nil {
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

	engine, err := r.engineForJob(jobType)
	if err != nil {
		notifyRunEvent(ctx, observer, agent.RunEvent{
			Type: agent.RunEventRunFailed,
			Err:  err,
		})
		return Result{}, fmt.Errorf("configure cognition engine for job %q: %w", jobType, err)
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

	runResult, err := engine.Run(ctx, agent.EngineRequest{
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
