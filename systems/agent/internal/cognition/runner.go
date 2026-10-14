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
	// LoadSemanticFileSizes returns the on-disk byte size of every semantic
	// root and topic file, keyed by memory-relative path (for example
	// "semantic/topics/q15.md").
	LoadSemanticFileSizes(ctx context.Context) (map[string]int64, error)
	// LoadSemanticTopics returns the trimmed content of every
	// semantic/topics/*.md file, keyed by memory-relative slash path. It is
	// separate from LoadSemanticMemory on purpose: the conversion pass treats
	// the three roots as its working set, so topic files must be read
	// explicitly by the jobs that want them.
	LoadSemanticTopics(ctx context.Context) (map[string]string, error)
	LoadWorkingMemory(ctx context.Context) (agent.WorkingMemory, error)
	// LoadOpenLoops returns the trimmed content of working/OPEN_LOOPS.md.
	LoadOpenLoops(ctx context.Context) (string, error)
	// LoadWorkingLedger returns the trimmed content of working/LEDGER.md.
	LoadWorkingLedger(ctx context.Context) (string, error)
	LoadSkillCatalog(ctx context.Context) (agent.SkillCatalog, error)
	LoadRecentMessages(ctx context.Context, turns int) ([]conversation.Message, error)
	LoadLatestMessages(ctx context.Context, turns int) ([]conversation.Message, error)
	LoadMessagesSinceSeq(ctx context.Context, afterSeq int64) ([]conversation.Message, error)
	LoadMessagesSinceSeqWindow(
		ctx context.Context,
		afterSeq int64,
		maxTurns int,
	) (TurnWindow, error)
	LoadHead(ctx context.Context) (int64, time.Time, error)
	LoadConsolidationCheckpoint(ctx context.Context) (ConsolidationCheckpoint, error)
	LoadSemanticExtractionCheckpoint(ctx context.Context) (SemanticExtractionCheckpoint, error)
	// LoadJobState returns the persisted trigger state for one cognition job
	// type, or the zero state when nothing was persisted for it yet. Jobs that
	// adapt their input cost to repeated failures read their own state through
	// it.
	LoadJobState(ctx context.Context, jobType string) (JobState, error)
	LoadCognitionArtifact(ctx context.Context, relativePath string) (Artifact, error)
	StoreCognitionArtifact(ctx context.Context, artifact Artifact) error
	// DeleteCognitionArtifact removes one job-owned cognition artifact from
	// the runtime memory root. A missing artifact counts as already deleted.
	DeleteCognitionArtifact(ctx context.Context, relativePath string) error
	// LoadSemanticChunk reads at most length bytes of one semantic source
	// file starting at offset, cut back to the last line boundary the window
	// contains, so a job can walk an oversized file in bounded whole-line
	// steps without ever carrying the whole file.
	LoadSemanticChunk(
		ctx context.Context,
		relativePath string,
		offset int64,
		length int64,
	) (SemanticChunk, error)
	// RemoveSemanticLines deletes the whole lines of one semantic source file
	// that start in the byte range [start, end) and whose line hash
	// (SemanticLineHash) is listed, keeping every other byte of the file
	// byte-for-byte. It returns the file's size after the rewrite and fails
	// when any listed hash matches no line in range, so a source file that
	// moved under the caller is reported instead of drained wrongly. The
	// migration uses it to remove exactly the source lines its run ledgered.
	RemoveSemanticLines(
		ctx context.Context,
		relativePath string,
		start int64,
		end int64,
		hashes []string,
	) (int64, error)
}

// Artifact is one job-owned persisted artifact stored under
// /memory/cognition/.
type Artifact struct {
	RelativePath string
	Content      string
}

// TurnWindow is a bounded slice of the oldest transcript turns that follow a
// sequence boundary, together with the boundary the window actually reached.
//
// It is the bounded counterpart of an unbounded replay load: a job that keeps a
// durable checkpoint can drain an arbitrarily long backlog in fixed-size steps
// instead of assembling one prompt that grows until every model rejects it.
type TurnWindow struct {
	// Messages is the flattened prompt-visible message slice of the window.
	Messages []conversation.Message
	// LastSeq is the transcript sequence of the last turn included in the
	// window, or zero when the window is empty.
	LastSeq int64
	// RemainingTurns counts the turns after the window that stay unprocessed.
	RemainingTurns int
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
	// CheckpointSeq is the transcript sequence this run actually processed when
	// the job worked through a bounded window instead of the whole backlog. The
	// zero value means the run covered everything up to the run head, which is
	// the default behaviour for every job that does not window its input.
	CheckpointSeq int64
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
	// CheckpointSeq is the transcript sequence this run actually processed when
	// the job worked through a bounded window. Zero means the run covered
	// everything up to the run head.
	CheckpointSeq int64
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
		Type:          jobType,
		Summary:       strings.TrimSpace(parsed.Summary),
		Metadata:      cloneMetadata(parsed.Metadata),
		FinalText:     runResult.FinalText,
		Messages:      conversation.CloneMessages(runResult.Messages),
		ModelRef:      runResult.ModelRef,
		Turn:          runResult.Turn,
		CheckpointSeq: parsed.CheckpointSeq,
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
