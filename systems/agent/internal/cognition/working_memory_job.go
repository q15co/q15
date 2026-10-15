package cognition

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/memory/claims"
	"github.com/q15co/q15/systems/agent/internal/memory/memcheck"
	filetools "github.com/q15co/q15/systems/agent/internal/tools/files"
)

const (
	workingMemoryConsolidationJobType       = "working_memory.consolidate"
	workingMemoryRelativePath               = "working/WORKING_MEMORY.md"
	workingMemoryRuntimePath                = "/memory/" + workingMemoryRelativePath
	workingMemoryScheduleSpec               = "0 4 * * *"
	workingMemoryRecentTurns                = 16
	workingMemoryWindowTurns                = 16
	workingMemoryMinDirtyTurns        int64 = 6
)

// WorkingMemoryConsolidationJobType is the job type under which the built-in
// working-memory consolidation job persists its trigger state. The controller
// writes that state and read-only status reporting reads it back by this name,
// so it has exactly one spelling.
const WorkingMemoryConsolidationJobType = workingMemoryConsolidationJobType

var workingMemoryAllowedTools = []string{
	"read_file",
	"write_file",
	"edit_file",
	"apply_patch",
}

// OpenLoopsLineFormat is the open-loop entry shape the working-memory prompt
// shows, quoted verbatim from the format comment in the OPEN_LOOPS seed so the
// prompt and the file the model edits cannot drift apart. The store's seed
// tests fail if that comment and this constant ever disagree.
const OpenLoopsLineFormat = `- [l-YYYY-NNNN] <what to do> once <condition>.
  {trigger: date:YYYY-MM-DD | event:"<description>" | next-session, owner: agent|user, src: t<seq>}`

// NewWorkingMemoryConsolidationRegistration returns the built-in working-memory
// consolidation job registration used to exercise the background trigger
// controller end to end.
func NewWorkingMemoryConsolidationRegistration(
	workingMemoryBudgetBytes, openLoopsBudgetBytes int,
) JobRegistration {
	return JobRegistration{
		NewJob: func() JobDefinition {
			return workingMemoryConsolidationJob{
				budgets: memoryFileBudgets{
					working:   workingMemoryBudgetBytes,
					openLoops: openLoopsBudgetBytes,
				},
			}
		},
		Policy: TriggerPolicy{
			Startup: []StartupRule{{
				ID:       "startup_unconsolidated_tail",
				Evaluate: evaluateWorkingMemoryStartup,
			}},
			Schedule: []ScheduleRule{{
				ID:   "daily_refresh",
				Spec: workingMemoryScheduleSpec,
			}},
			State: []StateRule{
				{
					ID:       "dirty_tail_threshold",
					Evaluate: evaluateWorkingMemoryState,
				},
				{
					ID: "open_loops_overbudget",
					Evaluate: func(
						ctx context.Context,
						snapshot Snapshot,
						_ JobState,
					) (bool, string, error) {
						return evaluateOpenLoopsOverbudget(
							ctx,
							snapshot,
							openLoopsBudgetBytes,
						)
					},
				},
			},
		},
	}
}

type workingMemoryConsolidationJob struct {
	budgets memoryFileBudgets
	now     func() time.Time
}

func (workingMemoryConsolidationJob) Type() string {
	return workingMemoryConsolidationJobType
}

func (j workingMemoryConsolidationJob) Build(
	ctx context.Context,
	loader ContextLoader,
) (Spec, error) {
	if loader == nil {
		return Spec{}, fmt.Errorf("context loader is required")
	}

	now := time.Now().UTC()
	if j.now != nil {
		now = j.now().UTC()
	}
	loopsBefore, _, err := loader.ReadFileBytes(ctx, "working/OPEN_LOOPS.md")
	if err != nil {
		return Spec{}, fmt.Errorf("read pre-run open loops: %w", err)
	}
	working, err := loader.LoadWorkingMemory(ctx)
	if err != nil {
		return Spec{}, fmt.Errorf("load working memory: %w", err)
	}
	openLoops, err := loader.LoadOpenLoops(ctx)
	if err != nil {
		return Spec{}, fmt.Errorf("load open loops: %w", err)
	}
	verificationArtifact, err := loader.LoadCognitionArtifact(
		ctx,
		VerificationReviewPath,
	)
	if err != nil {
		return Spec{}, fmt.Errorf("load verification review artifact: %w", err)
	}
	consolidationCheckpoint, err := loader.LoadConsolidationCheckpoint(ctx)
	if err != nil {
		return Spec{}, fmt.Errorf("load consolidation checkpoint: %w", err)
	}
	windowTurns, err := degradedWindowTurns(
		ctx,
		loader,
		workingMemoryConsolidationJobType,
		workingMemoryWindowTurns,
	)
	if err != nil {
		return Spec{}, fmt.Errorf("load consolidation job state: %w", err)
	}
	var recentMessages []conversation.Message
	var transcriptStatus string
	windowEndSeq := int64(0)
	if consolidationCheckpoint.LastConsolidatedSeq > 0 {
		window, loadErr := loader.LoadMessagesSinceSeqWindow(
			ctx,
			consolidationCheckpoint.LastConsolidatedSeq,
			windowTurns,
		)
		if loadErr != nil {
			return Spec{}, fmt.Errorf(
				"load messages since consolidation checkpoint: %w",
				loadErr,
			)
		}
		recentMessages = window.Messages
		windowEndSeq = window.LastSeq
		transcriptStatus = renderWorkingMemoryTranscriptScope(
			consolidationCheckpoint,
			window,
		)
	} else {
		recentMessages, err = loader.LoadRecentMessages(ctx, windowTurns)
		if err != nil {
			return Spec{}, fmt.Errorf("load recent messages: %w", err)
		}
		transcriptStatus = renderTranscriptScopeWithPolicy(
			"selected by the initial working-memory replay fallback window independent of the consolidation checkpoint",
			windowTurns,
			len(recentMessages),
		)
	}

	runtimePath := workingMemoryPath(working)
	workingContent := strings.TrimSpace(working.Content)
	if workingContent == "" {
		workingContent = "# Working Memory\n\n(working memory is currently empty)\n"
	}
	openLoopsContent := strings.TrimSpace(openLoops)
	openLoopsAttrs := map[string]string{"path": openLoopsRuntimePath}
	if openLoopsContent == "" {
		openLoopsAttrs["present"] = "false"
		openLoopsContent = "(open loops file does not exist yet)"
	} else {
		openLoopsAttrs["present"] = "true"
	}
	verificationContent := strings.TrimSpace(verificationArtifact.Content)
	verificationAttrs := map[string]string{"path": VerificationReviewRuntimePath}
	if verificationContent == "" {
		verificationAttrs["present"] = "false"
		verificationContent = "(verification review artifact does not exist yet)"
	} else {
		verificationAttrs["present"] = "true"
	}

	transcriptArtifact := renderTranscriptArtifact(recentMessages)

	snapshots, err := snapshotWorkingMemoryFiles(ctx, loader)
	if err != nil {
		return Spec{}, fmt.Errorf("snapshot working-memory targets: %w", err)
	}
	recoveryID, err := loader.BeginMemoryRecovery(ctx, snapshots, "")
	if err != nil {
		return Spec{}, fmt.Errorf("prepare working-memory recovery: %w", err)
	}

	return Spec{
		Objective: renderPromptLines(
			"This is a background maintenance job, not a user-facing reply.",
			"Consolidate bounded active state into the canonical working-memory artifact.",
			fmt.Sprintf("The target file is %s.", runtimePath),
			fmt.Sprintf(
				"The open loops file is %s; run the open-loop lifecycle on it in the same pass.",
				openLoopsRuntimePath,
			),
			"Do not answer questions from the transcript or continue the conversation.",
			"Use the recent transcript, current working memory, and latest verification review artifact to keep active state compact, current, and actionable.",
			"Treat verification notes as correction input for stale, unsupported, or inconsistent entries.",
			"Prefer what will matter for the next reply over archival summaries of older context.",
		),
		CompletionContract: renderPromptLines(
			fmt.Sprintf(
				"Always inspect or update %s with the file tools during this run.",
				runtimePath,
			),
			"Finish the full consolidation loop before responding: compare the target against the transcript artifact and verification review artifact, update or confirm it with file tools, then emit the short internal summary.",
			"Call read_file, write_file, edit_file, or apply_patch on the target before your final response. A response without a target-file tool call is invalid.",
			"If the file needs changes, update it with write_file, edit_file, or apply_patch while preserving the existing section structure.",
			"If no change is needed, call read_file on the target and leave the file unchanged.",
			fmt.Sprintf(
				"When a loop's date trigger has passed, move that loop into working memory Active Tasks or Pending Checks and delete its line from %s; a due loop moves, it is not copied.",
				openLoopsRuntimePath,
			),
			fmt.Sprintf(
				"Closing a loop means deleting its line from %s and appending one ledger line to %s naming the loop and the outcome.",
				openLoopsRuntimePath,
				workingLedgerRuntimePath,
			),
			fmt.Sprintf(
				"Every removed working-memory block requires exactly one converted, moved, or dropped ledger record in %s, including prose, comments, fenced text, and None-like content; only the canonical working-memory headings are exempt. Every closed or promoted loop requires exactly one closed record whose description preserves the full old loop, including its ID and metadata, and whose reason explains the outcome.",
				workingLedgerRuntimePath,
			),
			"Ledger format: YYYY-MM-DDTHH:MMZ run=working_memory.consolidate src=t<a>..t<b> <outcome> \"<full removed item without its bullet marker>\" → <claim-or-loop-id> (<file>). For dropped and closed records, omit the target and include an outcome reason in parentheses. Preserve all existing ledger bytes and append complete newline-terminated records.",
			"A closed loop that produced durable knowledge also merits a claim in the semantic store; this job does not write semantic memory, so leave the claim to the conversion pass.",
			"Apply supported verification corrections in the working-memory artifact; preserve explicit uncertainty when verification downgraded confidence.",
			"Do not copy the verification review artifact verbatim into working memory.",
			"Do not answer the transcript directly in your final response.",
			"After the tool work completes, reply with one or two short sentences summarizing what changed or that the file was already current.",
		),
		PromptSections: []agent.PromptSection{
			memoryBudgetPromptSection(j.budgets),
			{Name: "maintenance_time", Body: now.Format(time.RFC3339)},
			{
				Name:       "working_memory_target",
				Attributes: map[string]string{"path": runtimePath},
				Body:       workingContent,
			},
			{
				Name:       "open_loops_target",
				Attributes: openLoopsAttrs,
				Body:       openLoopsContent,
			},
			{
				Name:       "verification_review_input",
				Attributes: verificationAttrs,
				Body:       verificationContent,
			},
			{
				Name: "working_memory_rules",
				Body: renderPromptLines(
					"- Keep the canonical headings stable: Current Priorities, Active Tasks, Open Threads, Recent Progress, Pending Checks, Temporary Context.",
					"- Prefer short bullets and remove resolved or stale items.",
					"- Preserve the current user goal, active debugging thread, and unresolved questions from the latest turns even when they are temporary.",
					"- Prioritize the latest unresolved user request and the constraints that should shape the next reply.",
					"- If recent turns correct an earlier misunderstanding, recommendation, or assumption, keep the corrected state and drop the stale one.",
					"- If the user adds a concrete constraint or preference that matters for the next few turns, keep it in memory.",
					"- Use Active Tasks for the main thing we are working on right now; do not leave it as None when the latest turns still imply ongoing work.",
					"- If the user is still deciding, asking follow-up questions, or waiting on information, Active Tasks must contain that ongoing work; use None only when there is no active task.",
					"- Use Open Threads for unresolved follow-ups or decisions, not for already-understood meta observations, durable biography, or settled background facts.",
					"- Use Temporary Context for short-lived but important session context that should survive the next few turns.",
					"- When the recent transcript is about debugging or validation, keep the current hypothesis, observed behavior, and what still needs to be checked.",
					"- Use the verification review artifact as correction input when it flags stale, unsupported, or inconsistent entries that still apply.",
					"- Remove or rewrite working-memory items that verification identified as stale, unsupported, or contradicted by stronger evidence.",
					"- If verification downgrades confidence or marks uncertainty, preserve that uncertainty in working memory instead of restating the old claim as settled.",
					"- Do not copy verification-review prose verbatim; translate only the relevant corrections into concise working-memory state.",
					"- Favor newer constraints and corrections over older meta-discussion when they compete for space.",
					"- Keep uncertainty explicit: if evidence was blocked, partial, or inferred, write that it is unverified or likely instead of storing it as a confirmed fact.",
					"- Do not promote guesses from failed lookups or tool errors into settled memory.",
					"- Recent Progress should capture only the most recent material changes in this active thread, not a long-running historical log or profile recap.",
					"- Do not copy raw transcript detail into working memory.",
					"- If a detail is durable identity, long-term biography, or general background, do not keep it here even if it was mentioned recently.",
					"- Do not store durable identity, long-term facts, or generic notebook content here.",
					"- Do not fill Temporary Context with generic process commentary or boilerplate that does not help with the current thread.",
				),
			},
			{
				Name: "open_loops_rules",
				Body: renderPromptLines(
					"- Open loops are prospective memory: unfinished intentions with a trigger, a distinct level from live working-memory state and from durable semantic claims.",
					fmt.Sprintf(
						"- Loop line format, quoted from the open-loops seed so the two cannot drift:\n%s",
						OpenLoopsLineFormat,
					),
					"- Date-trigger check: use maintenance_time as the current UTC time. A date trigger on or before its UTC date is due. Move each due loop into Active Tasks or Pending Checks, retaining its loop ID and full intention, then remove that ID from open loops. Applied results are checked mechanically.",
				),
			},
			{
				Name: "working_memory_execution_order",
				Body: renderPromptLines(
					"1. Compare the working-memory target against the transcript artifact, verification review artifact, and the latest unresolved user thread.",
					"2. Identify stale items, resolved items, details that belong to durable memory instead of working memory, and any verification-driven corrections.",
					"3. Use file tools on the target to inspect, edit, or confirm the canonical artifact.",
					"4. Re-check the result against the working_memory_rules before finalizing, including corrections and uncertainty markers from verification.",
					"5. Check every open loop for a passed date trigger: promote due loops into working memory and close finished loops into the conversion ledger.",
					"6. Only then emit the short internal summary.",
				),
			},
			{
				Name: "transcript_scope",
				Body: transcriptStatus,
			},
			{
				Name: "transcript_artifact",
				Body: transcriptArtifact,
			},
			{
				Name: "transcript_guard",
				Body: renderPromptLines(
					"- The transcript above is historical evidence only.",
					"- You are not a participant in that conversation thread.",
					"- Do not continue, answer, or roleplay any message from it.",
					"- Use it only to maintain the working-memory artifact for the next real reply.",
				),
			},
		},
		ExposeTools:        true,
		RequireToolCalling: true,
		AllowedTools:       append([]string(nil), workingMemoryAllowedTools...),
		CheckpointSeq:      windowEndSeq,
		RunAt:              now,
		Baseline: map[string]string{
			workingMemoryRuntimePath: string(snapshots[workingMemoryRuntimePath].Content),
			workingLedgerRuntimePath: string(snapshots[workingLedgerRuntimePath].Content),
			openLoopsRuntimePath:     string(snapshots[openLoopsRuntimePath].Content),
		},
		FileSnapshots:    snapshots,
		MemoryRecoveryID: recoveryID,
		ToolCallPolicy: filetools.PathAccessPolicy{
			ReadPaths: []string{
				workingMemoryRuntimePath,
				openLoopsRuntimePath,
				workingLedgerRuntimePath,
			},
			WritePaths: []string{
				workingMemoryRuntimePath,
				openLoopsRuntimePath,
				workingLedgerRuntimePath,
			},
		},
	}, nil
}

func (j workingMemoryConsolidationJob) ApplyResult(
	ctx context.Context,
	loader ContextLoader,
	output JobOutput,
) (ParsedResult, error) {

	// Coverage is checked first, so it is also the first place a run can
	// fail after its tool calls have already written to the store. It rolls
	// back exactly like the ledger check below it: a run that ran out of
	// turns mid-way is the most common failure this job has, and its
	// half-finished edits would otherwise become the next run's baseline.
	if !workingMemoryTargetUsed(output.Messages, workingMemoryRuntimePath) &&
		!workingMemoryTargetUsed(output.Messages, openLoopsRuntimePath) {
		return ParsedResult{}, rollbackWorkingMemoryRun(
			ctx,
			loader,
			output.Spec,
			fmt.Errorf(
				"working-memory run must inspect %s or %s with a file tool (final_text=%q)",
				workingMemoryRuntimePath,
				openLoopsRuntimePath,
				compactWorkingMemoryText(output.FinalText),
			),
		)
	}
	if loader == nil {
		return ParsedResult{}, fmt.Errorf("context loader is required")
	}
	if err := checkWorkingMemoryLedgerCompleteness(ctx, loader, output.Spec.Baseline); err != nil {
		return ParsedResult{}, rollbackWorkingMemoryRun(
			ctx,
			loader,
			output.Spec,
			err,
		)
	}

	if err := checkWorkingFileBudgets(ctx, loader, j.budgets); err != nil {
		return ParsedResult{}, rollbackWorkingMemoryRun(ctx, loader, output.Spec, err)
	}
	if err := checkOpenLoopLifecycle(ctx, loader, output.Spec); err != nil {
		return ParsedResult{}, rollbackWorkingMemoryRun(ctx, loader, output.Spec, err)
	}

	summary := summarizeWorkingMemoryNotes(output.FinalText)

	return ParsedResult{
		Summary: summary,
		Metadata: map[string]string{
			"path": workingMemoryRuntimePath,
		},
		CheckpointSeq: output.Spec.CheckpointSeq,
	}, nil
}

// Rollback returns the run's writable paths to the state Build snapshotted.
// The runner calls it when a run fails before ApplyResult - an engine error,
// a cancelled context, a turn budget that runs out - so a failure leaves no
// partial writes behind and the next run's baseline is the store as it was.
func (workingMemoryConsolidationJob) Rollback(
	ctx context.Context,
	loader ContextLoader,
	spec Spec,
) error {
	if spec.MemoryRecoveryID != "" {
		return loader.RecoverMemory(ctx, spec.MemoryRecoveryID)
	}
	return restoreSnapshotFiles(ctx, loader, spec.FileSnapshots)
}

// checkWorkingMemoryLedgerCompleteness matches every removed item and complete
// loop against exactly one typed record, then checks its destination or reason.
func checkWorkingMemoryLedgerCompleteness(
	ctx context.Context,
	loader ContextLoader,
	baseline map[string]string,
) error {
	for _, runtimePath := range []string{workingMemoryRuntimePath, openLoopsRuntimePath, workingLedgerRuntimePath} {
		if _, ok := baseline[runtimePath]; !ok {
			return fmt.Errorf("spec baseline is missing pre-run %s", runtimePath)
		}
	}
	working, _, err := loader.ReadFileBytes(ctx, workingMemoryRelativePath)
	if err != nil {
		return fmt.Errorf("load post-run working memory: %w", err)
	}
	afterLoops, _, err := loader.ReadFileBytes(ctx, relativeMemoryPath(openLoopsRuntimePath))
	if err != nil {
		return fmt.Errorf("load post-run open loops: %w", err)
	}
	beforeLoopItems, err := parseOpenLoops(baseline[openLoopsRuntimePath])
	if err != nil {
		return fmt.Errorf("invalid pre-run open loops: %w", err)
	}
	afterLoopItems, err := parseOpenLoops(string(afterLoops))
	if err != nil {
		return fmt.Errorf("invalid post-run open loops: %w", err)
	}
	entries, err := appendedWorkingLedgerEntries(ctx, loader, baseline, "working-memory run")
	if err != nil {
		return err
	}
	workingExits := unmatchedWorkingExits(baseline[workingMemoryRuntimePath], string(working))
	loopExits := make(map[string]int)
	for _, loop := range beforeLoopItems {
		loopExits[workingItemDescription(loop.Item)]++
	}
	for _, loop := range afterLoopItems {
		description := workingItemDescription(loop.Item)
		if loopExits[description] > 0 {
			loopExits[description]--
		}
	}
	for _, entry := range entries {
		if entry.Run != workingMemoryConsolidationJobType {
			return fmt.Errorf("working-memory ledger entry has wrong run %q", entry.Run)
		}
		var unmatched map[string]int
		switch entry.Outcome {
		case claims.OutcomeConverted, claims.OutcomeMoved, claims.OutcomeDropped:
			unmatched = workingExits
		case claims.OutcomeClosed:
			if entry.TargetID != "" || entry.TargetPath != "" ||
				strings.TrimSpace(entry.Note) == "" {
				return fmt.Errorf("closed loop ledger exit requires a reason and no target")
			}
			unmatched = loopExits
		default:
			return fmt.Errorf(
				"working-memory ledger entry has unsupported outcome %q",
				entry.Outcome,
			)
		}
		description := normalizeExitDescription(entry.Description)
		if unmatched[description] == 0 {
			return fmt.Errorf(
				"ledger exit does not match an unrecorded removed working-memory item or complete open loop: %q",
				entry.Description,
			)
		}
		unmatched[description]--
	}
	for _, unmatched := range []map[string]int{workingExits, loopExits} {
		for description, count := range unmatched {
			if count > 0 {
				return fmt.Errorf(
					"working-memory run removed item without a matching ledger exit in %s: %q",
					workingLedgerRuntimePath,
					description,
				)
			}
		}
	}
	return validateExitTargets(ctx, loader, entries)
}

func unmatchedWorkingExits(before, after string) map[string]int {
	unmatched := make(map[string]int)
	for _, item := range removedWorkingItems(before, after) {
		unmatched[workingItemDescription(item)]++
	}
	return unmatched
}

// snapshotWorkingMemoryFiles captures the pre-run on-disk bytes of the three
// files a working-memory run may write, so a failed run can be rolled back
// byte-for-byte. The keys are the runtime-path constants the tool policy and
// coverage check already key on, never the loader-supplied relative path.
func snapshotWorkingMemoryFiles(
	ctx context.Context,
	loader ContextLoader,
) (map[string]FileSnapshot, error) {
	paths := []string{
		workingMemoryRuntimePath,
		openLoopsRuntimePath,
		workingLedgerRuntimePath,
	}
	snapshots := make(map[string]FileSnapshot, len(paths))
	for _, runtimePath := range paths {
		raw, existed, err := loader.ReadFileBytes(ctx, relativeMemoryPath(runtimePath))
		if err != nil {
			return nil, fmt.Errorf("snapshot %s: %w", runtimePath, err)
		}
		snapshots[runtimePath] = FileSnapshot{Existed: existed, Content: raw}
	}
	return snapshots, nil
}

func evaluateWorkingMemoryStartup(
	_ context.Context,
	snapshot Snapshot,
	state JobState,
) (bool, string, error) {
	shouldRun, reason := shouldRunWorkingMemoryConsolidation(snapshot, state)
	return shouldRun, reason, nil
}

func evaluateWorkingMemoryState(
	_ context.Context,
	snapshot Snapshot,
	state JobState,
) (bool, string, error) {
	shouldRun, reason := shouldRunWorkingMemoryConsolidation(snapshot, state)
	return shouldRun, reason, nil
}

func shouldRunWorkingMemoryConsolidation(
	snapshot Snapshot,
	state JobState,
) (bool, string) {
	if snapshot.HeadLastSeq <= 0 || state.DirtySinceSeq <= 0 {
		return false, ""
	}

	dirtyTurns := snapshot.HeadLastSeq - state.DirtySinceSeq + 1
	if dirtyTurns < workingMemoryMinDirtyTurns {
		return false, ""
	}

	return true, fmt.Sprintf(
		"dirty_tail=%d dirty_since_seq=%d head_seq=%d",
		dirtyTurns,
		state.DirtySinceSeq,
		snapshot.HeadLastSeq,
	)
}

// evaluateOpenLoopsOverbudget fires a working-memory run when the open-loops
// file exceeds its budget. Like the working-memory rule it mirrors, it is only
// evaluated once head has advanced past the last run's input
// (shouldEvaluateStateRules), so it fires on the next turn after the file goes
// over budget, never on file size alone. The rule only reads: it never
// truncates or rewrites the file, and the job it triggers owns the file that
// shrinks it.
func evaluateOpenLoopsOverbudget(
	ctx context.Context,
	snapshot Snapshot,
	budget int,
) (bool, string, error) {
	if snapshot.Loader == nil {
		return false, "", nil
	}
	effectiveBudget := effectiveOpenLoopsBudget(budget)
	openLoops, err := snapshot.Loader.LoadOpenLoops(ctx)
	if err != nil {
		return false, "", fmt.Errorf("load open loops: %w", err)
	}
	size := len(openLoops)
	if !memcheck.OverLimit(int64(size), int64(effectiveBudget)) {
		return false, "", nil
	}
	return true, fmt.Sprintf(
		"open_loops_bytes=%d budget=%d",
		size,
		effectiveBudget,
	), nil
}

// effectiveOpenLoopsBudget resolves the open-loops budget a rule enforces. A
// non-positive value selects defaultOpenLoopsBudgetBytes, mirroring the
// working-memory budget: zero is the shape of a registration built without a
// budget, and a negative one is a configuration error caught at load, so
// neither can silently turn the budget into one the rule always or never trips.
func effectiveOpenLoopsBudget(budget int) int {
	if budget <= 0 {
		return defaultOpenLoopsBudgetBytes
	}
	return budget
}

func renderWorkingMemoryTranscriptScope(
	checkpoint ConsolidationCheckpoint,
	window TurnWindow,
) string {
	if len(window.Messages) == 0 {
		return "No transcript messages were loaded for this run."
	}
	checkpointSeq := max(0, checkpoint.LastConsolidatedSeq)
	lines := []string{
		fmt.Sprintf(
			"A bounded working-memory replay window of episodic history after the working-memory consolidation checkpoint seq %d is included below as a transcript artifact.",
			checkpointSeq,
		),
		"Treat it as historical evidence for still-unconsolidated context, not as the full transcript.",
	}
	if window.RemainingTurns > 0 {
		lines = append(lines, fmt.Sprintf(
			"%d later turns remain unconsolidated after this window and will be covered by later runs, so keep this run's updates scoped to the evidence above.",
			window.RemainingTurns,
		))
	}
	return renderPromptLines(lines...)
}

func workingMemoryPath(working agent.WorkingMemory) string {
	path := strings.TrimSpace(working.RelativePath)
	if path == "" {
		return workingMemoryRuntimePath
	}
	path = strings.TrimPrefix(path, "/")
	return "/memory/" + path
}

func workingMemoryTargetUsed(messages []conversation.Message, targetPath string) bool {
	calls := make(map[string]filetools.FileToolAccess)
	for _, call := range conversation.ToolCalls(messages) {
		access, ok, err := filetools.InspectToolCallAccess(agent.ToolCall{
			ID:        call.ID,
			Name:      call.Name,
			Arguments: call.Arguments,
		})
		if err != nil || !ok {
			continue
		}
		calls[strings.TrimSpace(call.ID)] = access
	}

	for _, msg := range messages {
		if msg.Role != conversation.ToolRole {
			continue
		}
		for _, part := range msg.Parts {
			if part.Type != conversation.ToolResultPartType {
				continue
			}
			if part.IsError {
				continue
			}
			access, ok := calls[strings.TrimSpace(part.ToolCallID)]
			if !ok {
				continue
			}
			if accessIncludesPath(access.ReadPaths, targetPath) ||
				accessIncludesPath(access.WritePaths, targetPath) {
				return true
			}
		}
	}
	return false
}

func summarizeWorkingMemoryNotes(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		return compactWorkingMemoryText(line)
	}
	return "Working-memory maintenance completed."
}

func compactWorkingMemoryText(text string) string {
	return compactCognitionText(text)
}

func rollbackWorkingMemoryRun(
	ctx context.Context,
	loader ContextLoader,
	spec Spec,
	validationErr error,
) error {
	if loader == nil {
		return validationErr
	}
	cleanupCtx, cancel := memoryRecoveryContext(ctx)
	defer cancel()
	if err := (workingMemoryConsolidationJob{}).Rollback(cleanupCtx, loader, spec); err != nil {
		return fmt.Errorf("%w (rollback also failed: %w)", validationErr, err)
	}
	return validationErr
}
