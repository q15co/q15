package cognition

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	filetools "github.com/q15co/q15/systems/agent/internal/tools/files"
)

const (
	workingMemoryConsolidationJobType       = "working_memory.consolidate"
	workingMemoryRelativePath               = "working/WORKING_MEMORY.md"
	workingMemoryRuntimePath                = "/memory/" + workingMemoryRelativePath
	workingMemoryScheduleSpec               = "0 4 * * *"
	workingMemoryRecentTurns                = 16
	workingMemoryMinDirtyTurns        int64 = 6
	// workingMemoryCeilingBytes is the size the prompt-visible working-memory
	// artifact has to stay at or under. The artifact is injected into every
	// prompt in full, so its size is both a per-turn cost and a per-turn
	// attention cost. The ceiling is where a run starts reclaiming space rather
	// than a write-side limit, because nothing can refuse the model's own
	// whole-file write.
	workingMemoryCeilingBytes = 100 * 1024
	// workingMemoryWarningBytes is where a run is told to trim, before the
	// artifact reaches the ceiling. Both thresholds are measured against the
	// loaded content, which the store trims of surrounding whitespace.
	workingMemoryWarningBytes = 75 * 1024
)

var workingMemoryAllowedTools = []string{
	"read_file",
	"write_file",
	"edit_file",
	"apply_patch",
}

// NewWorkingMemoryConsolidationRegistration returns the built-in working-memory
// consolidation job registration used to exercise the background trigger
// controller end to end.
func NewWorkingMemoryConsolidationRegistration() JobRegistration {
	return JobRegistration{
		NewJob: func() JobDefinition {
			return workingMemoryConsolidationJob{}
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
			State: []StateRule{{
				ID:       "dirty_tail_threshold",
				Evaluate: evaluateWorkingMemoryState,
			}},
		},
	}
}

type workingMemoryConsolidationJob struct{}

func (workingMemoryConsolidationJob) Type() string {
	return workingMemoryConsolidationJobType
}

func (workingMemoryConsolidationJob) Build(
	ctx context.Context,
	loader ContextLoader,
) (Spec, error) {
	if loader == nil {
		return Spec{}, fmt.Errorf("context loader is required")
	}

	working, err := loader.LoadWorkingMemory(ctx)
	if err != nil {
		return Spec{}, fmt.Errorf("load working memory: %w", err)
	}
	verificationArtifact, err := loader.LoadCognitionArtifact(
		ctx,
		VerificationReviewPath,
	)
	if err != nil {
		return Spec{}, fmt.Errorf("load verification review artifact: %w", err)
	}
	recentMessages, err := loader.LoadRecentMessages(ctx, workingMemoryRecentTurns)
	if err != nil {
		return Spec{}, fmt.Errorf("load recent messages: %w", err)
	}

	runtimePath := workingMemoryPath(working)
	workingContent := strings.TrimSpace(working.Content)
	if workingContent == "" {
		workingContent = "# Working Memory\n\n(working memory is currently empty)\n"
	}
	// A run that may shorten the artifact leaves a verbatim copy behind first,
	// so demotion can never be the thing that loses text that was still live.
	workingSize := len(working.Content)
	var (
		archivePath string
		archiveErr  error
	)
	if workingSize > workingMemoryWarningBytes {
		archivePath, archiveErr = loader.ArchiveWorkingMemory(ctx, working.Content)
	}
	verificationContent := strings.TrimSpace(verificationArtifact.Content)
	verificationAttrs := map[string]string{"path": VerificationReviewRuntimePath}
	if verificationContent == "" {
		verificationAttrs["present"] = "false"
		verificationContent = "(verification review artifact does not exist yet)"
	} else {
		verificationAttrs["present"] = "true"
	}

	transcriptStatus := renderTranscriptScope(workingMemoryRecentTurns, len(recentMessages))
	transcriptArtifact := renderTranscriptArtifact(recentMessages)

	return Spec{
		Objective: renderPromptLines(
			"This is a background maintenance job, not a user-facing reply.",
			"Consolidate bounded active state into the canonical working-memory artifact.",
			fmt.Sprintf("The target file is %s.", runtimePath),
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
			"When the working_memory_size section reports the artifact is over the ceiling, it must be at or under that ceiling by the time you finish.",
			"If no change is needed, call read_file on the target and leave the file unchanged.",
			"Apply supported verification corrections in the working-memory artifact; preserve explicit uncertainty when verification downgraded confidence.",
			"Do not copy the verification review artifact verbatim into working memory.",
			"Do not answer the transcript directly in your final response.",
			"After the tool work completes, reply with one or two short sentences summarizing what changed or that the file was already current.",
		),
		PromptSections: []agent.PromptSection{
			{
				Name:       "working_memory_target",
				Attributes: map[string]string{"path": runtimePath},
				Body:       workingContent,
			},
			workingMemorySizeSection(runtimePath, workingSize, archivePath, archiveErr),
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
					"- Keep the artifact at or under the size ceiling reported in the working_memory_size section; when that section reports the artifact is over the ceiling, this run is the run that brings it back down.",
					"- Never truncate the artifact to reach the ceiling: remove whole resolved, superseded or duplicated entries, and never cut a live thread short.",
					"- If a detail is durable identity, long-term biography, or general background, do not keep it here even if it was mentioned recently.",
					"- Do not store durable identity, long-term facts, or generic notebook content here.",
					"- Do not fill Temporary Context with generic process commentary or boilerplate that does not help with the current thread.",
				),
			},
			{
				Name: "working_memory_execution_order",
				Body: renderPromptLines(
					"1. Compare the working-memory target against the transcript artifact, verification review artifact, and the latest unresolved user thread.",
					"2. Identify stale items, resolved items, details that belong to durable memory instead of working memory, and any verification-driven corrections.",
					"3. Use file tools on the target to inspect, edit, or confirm the canonical artifact.",
					"4. Re-check the result against the working_memory_rules before finalizing, including corrections and uncertainty markers from verification.",
					"5. Only then emit the short internal summary.",
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
		ToolCallPolicy: filetools.PathAccessPolicy{
			ReadPaths:  []string{workingMemoryRuntimePath},
			WritePaths: []string{workingMemoryRuntimePath},
		},
	}, nil
}

func (workingMemoryConsolidationJob) ApplyResult(
	_ context.Context,
	_ ContextLoader,
	output JobOutput,
) (ParsedResult, error) {
	if !workingMemoryTargetUsed(output.Messages, workingMemoryRuntimePath) {
		return ParsedResult{}, fmt.Errorf(
			"working-memory run must inspect %s with a file tool (final_text=%q)",
			workingMemoryRuntimePath,
			compactWorkingMemoryText(output.FinalText),
		)
	}

	summary := summarizeWorkingMemoryNotes(output.FinalText)

	return ParsedResult{
		Summary: summary,
		Metadata: map[string]string{
			"path": workingMemoryRuntimePath,
		},
	}, nil
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

// workingMemorySizeState classifies the artifact against its budget so the
// prompt can say what this run has to do about it.
func workingMemorySizeState(size int) string {
	switch {
	case size > workingMemoryCeilingBytes:
		return "over_ceiling"
	case size > workingMemoryWarningBytes:
		return "over_warning"
	default:
		return "within_budget"
	}
}

// workingMemorySizeSection reports the artifact's size, its budget and - when
// a copy was taken - where the pre-run text can be recovered, so the ceiling
// does not depend on the artifact describing its own limit.
func workingMemorySizeSection(
	runtimePath string,
	size int,
	archivePath string,
	archiveErr error,
) agent.PromptSection {
	state := workingMemorySizeState(size)
	attributes := map[string]string{
		"path":          runtimePath,
		"bytes":         strconv.Itoa(size),
		"ceiling_bytes": strconv.Itoa(workingMemoryCeilingBytes),
		"warning_bytes": strconv.Itoa(workingMemoryWarningBytes),
		"state":         state,
	}
	if archivePath != "" {
		attributes["archived_at"] = archivePath
	}

	lines := make([]string, 0, 4)
	switch state {
	case "over_ceiling":
		lines = append(lines, fmt.Sprintf(
			"The artifact is %d bytes against a %d-byte ceiling. This run has to leave it at or under the ceiling.",
			size,
			workingMemoryCeilingBytes,
		))
	case "over_warning":
		lines = append(lines, fmt.Sprintf(
			"The artifact is %d bytes: above the %d-byte warning and still under the %d-byte ceiling. Trim closed material now rather than letting the next runs inherit it.",
			size,
			workingMemoryWarningBytes,
			workingMemoryCeilingBytes,
		))
	default:
		lines = append(lines, fmt.Sprintf(
			"The artifact is %d bytes, within the %d-byte ceiling. Keep it there as you edit.",
			size,
			workingMemoryCeilingBytes,
		))
	}

	switch {
	case archiveErr != nil:
		lines = append(lines, fmt.Sprintf(
			"Archiving the current text failed for this run (%v), so nothing may be deleted: tighten wording only, and expect the ceiling to be re-checked on the next run.",
			archiveErr,
		))
	case archivePath != "":
		lines = append(lines, fmt.Sprintf(
			"A verbatim copy of the artifact as it stands right now is archived at %s, so text removed in this run is not lost. Demote, never truncate.",
			archivePath,
		))
	}

	lines = append(
		lines,
		"Demote by removing whole entries that are closed, superseded or duplicated, and by folding repeated verification detail into one line.",
		"Durable facts, biography and project state are not this artifact's job - the semantic extraction job owns them - so dropping them here does not lose them.",
	)

	return agent.PromptSection{
		Name:       "working_memory_size",
		Attributes: attributes,
		Body:       renderPromptLines(lines...),
	}
}
