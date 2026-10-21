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
	defaultCognitionMaxTurns               = 32
	defaultCognitionMaxFailedModelAttempts = 3
	memoryRecoveryTimeout                  = 30 * time.Second
)

// ContextLoader provides explicit access to state a cognition job may opt into.
type ContextLoader interface {
	LoadCoreMemory(ctx context.Context) (agent.CoreMemory, error)
	LoadSemanticMemory(ctx context.Context) (agent.SemanticMemory, error)
	// LoadSemanticFileSizes returns the on-disk byte size of every semantic
	// root and topic file, keyed by memory-relative path (for example
	// "semantic/topics/q15.md").
	LoadSemanticFileSizes(ctx context.Context) (map[string]int64, error)
	// LoadSemanticTopics returns the exact content of every direct file in
	// semantic/topics/, keyed by memory-relative slash path. It is
	// separate from LoadSemanticMemory on purpose: the conversion pass treats
	// the three roots as its working set, so topic files must be read
	// explicitly by the jobs that want them.
	LoadSemanticTopics(ctx context.Context) (map[string]string, error)
	// ListMemoryFiles returns every regular file under the two memory content
	// roots with its on-disk byte size, sorted by repository-relative path.
	// The enumeration is stat-only: it reads no file contents, so a state
	// rule can afford it on every evaluation.
	ListMemoryFiles(ctx context.Context) ([]MemoryFile, error)
	LoadWorkingMemory(ctx context.Context) (agent.WorkingMemory, error)
	// LoadOpenLoops returns the trimmed content of working/OPEN_LOOPS.md.
	LoadOpenLoops(ctx context.Context) (string, error)
	// LoadWorkingLedger returns the trimmed content of working/LEDGER.md.
	LoadWorkingLedger(ctx context.Context) (string, error)
	ReadFileBytes(ctx context.Context, relativePath string) ([]byte, bool, error)
	LoadConversionLedgerHistory(ctx context.Context) (string, error)
	WriteFileBytes(ctx context.Context, relativePath string, data []byte) error
	DeleteFile(ctx context.Context, relativePath string) error
	// BeginMemoryRecovery durably records the pre-run writable set. A pending
	// journal must be recovered before another run can begin.
	BeginMemoryRecovery(
		ctx context.Context,
		snapshots map[string]FileSnapshot,
		topicDir string,
	) (string, error)
	CompleteMemoryRecovery(ctx context.Context, id string) error
	RecoverMemory(ctx context.Context, id string) error
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
	Messages        []conversation.Message
	LastIncludedSeq int64
	PartialTurn     *TranscriptCursor
	// LastSeq is the last completely covered turn. It stays zero while a
	// PartialTurn cursor covers fragments of the first turn.
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

// Rollbacker is implemented by a job whose Spec carries the pre-run state of
// every path its run can write. The runner asks it to restore that state when
// the run fails, because partial writes from a failed run would otherwise
// survive on disk and become the next run's baseline.
type Rollbacker interface {
	Rollback(ctx context.Context, loader ContextLoader, spec Spec) error
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
	// Repair marks the run as the semantic job's bounded memory-repair mode.
	// A repair run consumes no transcript, so the controller reads this bit
	// and never stores a semantic extraction checkpoint for it - not even the
	// run head, which a zero CheckpointSeq would otherwise select.
	Repair bool
}

// Result is the completed structured outcome for one cognition job.
type Result struct {
	// NoWork means no transcript or model work was performed by this attempt.
	NoWork           bool
	WaitingForRepair bool
	Type             string
	Summary          string
	Metadata         map[string]string
	FinalText        string
	Messages         []conversation.Message
	ModelRef         string
	Turn             int
	// CheckpointSeq is the transcript sequence this run actually processed when
	// the job worked through a bounded window. Zero means the run covered
	// everything up to the run head.
	CheckpointSeq int64
	// Repair marks a bounded memory-repair run of the semantic job; the
	// controller uses it to skip the semantic extraction checkpoint store.
	Repair             bool
	repairTarget       string
	repairDigest       string
	transcriptProgress *transcriptProgress
}

// Runner executes typed cognition jobs on the shared model/tool engine.
type Runner struct {
	modelClient  agent.ModelClient
	planner      modelselection.Planner
	tools        agent.ToolRegistry
	resolver     ModelRefResolver
	modelContext ModelContextResolver
	engineRef    *agent.Engine // optional prebuilt engine (NewRunnerWithEngine)
	loader       ContextLoader
	maxTurns     int
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

// ModelContextResolver returns the known context and output token limits of
// the model pinned to one cognition job type. Zeros mean unknown, in which
// case the run's request byte budget falls back to the floor.
type ModelContextResolver func(jobType string) (contextTokens, outputTokens int)

// SetModelContextResolver installs the optional per-job model metadata lookup
// used to size the request byte budget of each run. A nil resolver pins the
// floor budget.
func (r *Runner) SetModelContextResolver(resolver ModelContextResolver) {
	if r == nil {
		return
	}
	r.modelContext = resolver
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
	if spec.NoWork != nil {
		result := Result{
			Type:             jobType,
			NoWork:           true,
			WaitingForRepair: spec.NoWork.WaitingForRepair,
			Summary:          strings.TrimSpace(spec.NoWork.Summary),
			Metadata:         cloneMetadata(spec.NoWork.Metadata),
		}
		notifyRunEvent(ctx, observer, agent.RunEvent{Type: agent.RunEventRunFinished})
		return result, nil
	}

	failureResult := repairFailureResult(jobType, spec)
	engine, err := r.engineForJob(jobType)
	if err != nil {
		err = r.withRollbackError(ctx, job, spec, err)
		notifyRunEvent(ctx, observer, agent.RunEvent{
			Type: agent.RunEventRunFailed,
			Err:  err,
		})
		return failureResult, fmt.Errorf("configure cognition engine for job %q: %w", jobType, err)
	}

	systemPrompt, err := renderPrompt(jobType, spec)
	if err != nil {
		err = r.withRollbackError(ctx, job, spec, err)
		notifyRunEvent(ctx, observer, agent.RunEvent{
			Type: agent.RunEventRunFailed,
			Err:  err,
		})
		return failureResult, fmt.Errorf("render cognition prompt for job %q: %w", jobType, err)
	}

	inputMessages := make([]conversation.Message, 0, 1+len(spec.InputMessages))
	inputMessages = append(inputMessages, conversation.SystemMessage(systemPrompt))
	inputMessages = append(inputMessages, conversation.CloneMessages(spec.InputMessages)...)

	contextTokens, outputTokens := 0, 0
	if r.modelContext != nil {
		contextTokens, outputTokens = r.modelContext(jobType)
	}

	runCtx := ctx
	if spec.MemoryRecoveryID != "" {
		runCtx = withMemoryRecoveryOwner(ctx, spec.MemoryRecoveryID)
	}
	runResult, err := engine.Run(runCtx, agent.EngineRequest{
		Messages:               inputMessages,
		UseTools:               spec.ExposeTools,
		AllowedTools:           append([]string(nil), spec.AllowedTools...),
		ToolCallPolicy:         spec.ToolCallPolicy,
		RequireToolCalling:     spec.RequireToolCalling,
		Observer:               observer,
		MaxFailedModelAttempts: defaultCognitionMaxFailedModelAttempts,
		MaxRequestBytes: cognitionRequestLimit(
			spec,
			systemPrompt,
			contextTokens,
			outputTokens,
		),
	})
	if err != nil {
		err = describeRequestBudget(jobType, spec, systemPrompt, err)
		err = r.withRollbackError(ctx, job, spec, err)
		notifyRunEvent(ctx, observer, agent.RunEvent{
			Type:      agent.RunEventRunFailed,
			Turn:      runResult.Turn,
			ModelRef:  runResult.ModelRef,
			FinalText: runResult.FinalText,
			Err:       err,
		})
		return failureResult, err
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
	if err == nil && spec.MemoryRecoveryID != "" {
		err = r.loader.CompleteMemoryRecovery(ctx, spec.MemoryRecoveryID)
	}
	if err != nil {
		err = r.withRollbackError(ctx, job, spec, err)
		notifyRunEvent(ctx, observer, agent.RunEvent{
			Type:      agent.RunEventRunFailed,
			Turn:      runResult.Turn,
			ModelRef:  runResult.ModelRef,
			FinalText: runResult.FinalText,
			Err:       err,
		})
		return failureResult, fmt.Errorf("apply cognition result for job %q: %w", jobType, err)
	}

	result := Result{
		Type:               jobType,
		Summary:            strings.TrimSpace(parsed.Summary),
		Metadata:           cloneMetadata(parsed.Metadata),
		FinalText:          runResult.FinalText,
		Messages:           conversation.CloneMessages(runResult.Messages),
		ModelRef:           runResult.ModelRef,
		Turn:               runResult.Turn,
		CheckpointSeq:      parsed.CheckpointSeq,
		Repair:             parsed.Repair,
		repairTarget:       failureResult.repairTarget,
		repairDigest:       failureResult.repairDigest,
		transcriptProgress: spec.transcriptProgress,
	}
	notifyRunEvent(ctx, observer, agent.RunEvent{
		Type:      agent.RunEventRunFinished,
		Turn:      result.Turn,
		ModelRef:  result.ModelRef,
		FinalText: result.FinalText,
	})
	return result, nil
}

// rollbackFailedRun restores the pre-run state of a failed run when the job
// and the spec make that possible: the spec must carry snapshots, there must be
// a loader to write them back through, and the job must implement Rollbacker.
// A job that carries no snapshots is left exactly as it is.
func (r *Runner) rollbackFailedRun(
	ctx context.Context,
	job JobDefinition,
	spec Spec,
) error {
	if r.loader == nil {
		return nil
	}
	cleanupCtx, cancel := memoryRecoveryContext(ctx)
	defer cancel()
	if spec.MemoryRecoveryID != "" {
		return r.loader.RecoverMemory(cleanupCtx, spec.MemoryRecoveryID)
	}
	if len(spec.FileSnapshots) == 0 {
		return nil
	}
	rollbacker, ok := job.(Rollbacker)
	if !ok {
		return nil
	}
	return rollbacker.Rollback(cleanupCtx, r.loader, spec)
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

func memoryRecoveryContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), memoryRecoveryTimeout)
}

func (r *Runner) withRollbackError(
	ctx context.Context,
	job JobDefinition,
	spec Spec,
	runErr error,
) error {
	if err := r.rollbackFailedRun(ctx, job, spec); err != nil {
		return fmt.Errorf("%w (rollback also failed: %w)", runErr, err)
	}
	return runErr
}
