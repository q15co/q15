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
	semanticMemoryExtractionJobType = "semantic_memory.extract"
	semanticFactsRelativePath       = "semantic/facts.md"
	semanticPreferencesRelativePath = "semantic/preferences.md"
	semanticProjectsRelativePath    = "semantic/projects.md"
	semanticFactsRuntimePath        = "/memory/" + semanticFactsRelativePath
	semanticPreferencesRuntimePath  = "/memory/" + semanticPreferencesRelativePath
	semanticProjectsRuntimePath     = "/memory/" + semanticProjectsRelativePath
	semanticMemoryScheduleSpec      = "0 5 * * *"
	semanticMemoryRecentTurns       = 32
	semanticMemoryMinDirtyTurns     = 12
)

var semanticMemoryAllowedTools = []string{
	"read_file",
	"write_file",
	"edit_file",
	"apply_patch",
}

// NewSemanticMemoryExtractionRegistration returns the built-in semantic-memory
// extraction job registration.
func NewSemanticMemoryExtractionRegistration() JobRegistration {
	return JobRegistration{
		NewJob: func() JobDefinition {
			return semanticMemoryExtractionJob{}
		},
		Policy: TriggerPolicy{
			Schedule: []ScheduleRule{{
				ID:   "daily_refresh",
				Spec: semanticMemoryScheduleSpec,
			}},
			State: []StateRule{{
				ID:       "dirty_tail_threshold",
				Evaluate: evaluateSemanticMemoryState,
			}},
		},
	}
}

type semanticMemoryExtractionJob struct{}

func (semanticMemoryExtractionJob) Type() string {
	return semanticMemoryExtractionJobType
}

func (semanticMemoryExtractionJob) Build(
	ctx context.Context,
	loader ContextLoader,
) (Spec, error) {
	if loader == nil {
		return Spec{}, fmt.Errorf("context loader is required")
	}

	semantic, err := loader.LoadSemanticMemory(ctx)
	if err != nil {
		return Spec{}, fmt.Errorf("load semantic memory: %w", err)
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
	recentMessages, err := loader.LoadLatestMessages(ctx, semanticMemoryRecentTurns)
	if err != nil {
		return Spec{}, fmt.Errorf("load latest messages: %w", err)
	}

	semanticFiles := semanticMemoryContentMap(semantic)
	factsContent, err := semanticContentForPath(semanticFiles, semanticFactsRelativePath)
	if err != nil {
		return Spec{}, fmt.Errorf("semantic facts target: %w", err)
	}
	preferencesContent, err := semanticContentForPath(
		semanticFiles,
		semanticPreferencesRelativePath,
	)
	if err != nil {
		return Spec{}, fmt.Errorf("semantic preferences target: %w", err)
	}
	projectsContent, err := semanticContentForPath(semanticFiles, semanticProjectsRelativePath)
	if err != nil {
		return Spec{}, fmt.Errorf("semantic projects target: %w", err)
	}
	workingContent := strings.TrimSpace(working.Content)
	if workingContent == "" {
		workingContent = "# Working Memory\n\n(working memory is currently empty)\n"
	}
	verificationContent := strings.TrimSpace(verificationArtifact.Content)
	verificationAttrs := map[string]string{"path": VerificationReviewRuntimePath}
	if verificationContent == "" {
		verificationAttrs["present"] = "false"
		verificationContent = "(verification review artifact does not exist yet)"
	} else {
		verificationAttrs["present"] = "true"
	}

	transcriptStatus := renderTranscriptScopeWithPolicy(
		"selected by a semantic replay window independent of the working-memory consolidation checkpoint",
		semanticMemoryRecentTurns,
		len(recentMessages),
	)
	transcriptArtifact := renderTranscriptArtifact(recentMessages)

	return Spec{
		Objective: renderPromptLines(
			"This is a background maintenance job, not a user-facing reply.",
			"Extract conservative durable knowledge into the canonical semantic-memory files.",
			fmt.Sprintf(
				"The target files are %s, %s, and %s.",
				semanticFactsRuntimePath,
				semanticPreferencesRuntimePath,
				semanticProjectsRuntimePath,
			),
			"Use current semantic memory, working memory, recent transcript context, and the latest verification review artifact as evidence.",
			"Treat durable user-profile knowledge as first-class semantic memory, not just active project context.",
			"Do not answer questions from the transcript or continue the conversation.",
		),
		CompletionContract: renderPromptLines(
			"Always inspect all three semantic-memory target files with file tools during this run.",
			fmt.Sprintf(
				"Call read_file, write_file, edit_file, or apply_patch on each of %s, %s, and %s before your final response. A response without target-file tool coverage for all three semantic files is invalid.",
				semanticFactsRuntimePath,
				semanticPreferencesRuntimePath,
				semanticProjectsRuntimePath,
			),
			"If a target file needs changes, update only that file with write_file, edit_file, or apply_patch while preserving the existing section structure.",
			"If a target file does not need changes, call read_file on it and leave it unchanged.",
			"Keep the semantic-memory update bounded, structured, and auditable.",
			"Do not copy transcript detail or verification-review prose verbatim into semantic memory.",
			"Do not answer the transcript directly in your final response.",
			"If canonical semantic files have drifted to non-canonical headings, normalize them back to the fixed schema during this run.",
			"After editing, be precise about which target files changed versus which were only inspected; do not claim a file was updated unless you wrote, edited, or patched it.",
			"After the tool work completes, reply with one or two short sentences summarizing what changed or that semantic memory was already current.",
		),
		PromptSections: []agent.PromptSection{
			{
				Name:       "semantic_facts_target",
				Attributes: map[string]string{"path": semanticFactsRuntimePath},
				Body:       factsContent,
			},
			{
				Name:       "semantic_preferences_target",
				Attributes: map[string]string{"path": semanticPreferencesRuntimePath},
				Body:       preferencesContent,
			},
			{
				Name:       "semantic_projects_target",
				Attributes: map[string]string{"path": semanticProjectsRuntimePath},
				Body:       projectsContent,
			},
			{
				Name:       "working_memory_snapshot",
				Attributes: map[string]string{"path": workingMemoryPath(working)},
				Body:       workingContent,
			},
			{
				Name:       "verification_review_input",
				Attributes: verificationAttrs,
				Body:       verificationContent,
			},
			{
				Name: "semantic_memory_rules",
				Body: renderPromptLines(
					"- Preserve the fixed section structure in each target file.",
					"- facts.md headings must remain Confirmed Facts and Grounded Inferences.",
					"- preferences.md headings must remain User Preferences and Collaboration Preferences.",
					"- projects.md headings must remain Active Projects and Durable Project Knowledge.",
					"- Promote only explicit durable user statements or claims corroborated by repeated evidence in the provided context.",
					"- Repeated protocol, questionnaire, or profile-building answers count as repeated evidence when they consistently describe stable personal facts or preferences.",
					"- Prefer verified or explicitly grounded claims over speculative inferences.",
					"- If verification flags a semantic entry as stale, unsupported, or contradicted, remove it, rewrite it, or downgrade it with explicit uncertainty.",
					"- If current semantic files contain drifted headings such as Personal Facts or Technical Facts, fold those entries back into the canonical headings and remove the drift.",
					"- Never promote single-turn temporary tasks, transient session context, speculative tool-output guesses, or generic process chatter.",
					"- Maintain strict category separation: do not duplicate the same claim across facts.md and preferences.md.",
					"- Use facts.md -> Confirmed Facts only for objective durable facts: biography, stable routines, language abilities, owned pets, capabilities, past events, project facts, and tool/runtime facts.",
					"- Do not put subjective choices, tastes, favorites, likes, dislikes, or preferred styles in facts.md just because the user stated them explicitly.",
					"- Keep uncertain but useful durable claims only under facts.md -> Grounded Inferences, with uncertainty labeled explicitly.",
					"- Use preferences.md -> User Preferences for durable subjective preferences: likes, dislikes, favorite categories, tastes, lifestyle choices, entertainment choices, learning style, work style, social style, and decision style.",
					"- Do not use preferences.md to restate objective facts, biography, capabilities, past events, or tool/runtime facts; keep those in facts.md unless the entry is explicitly about a preference.",
					"- When a preference needs context, phrase it as the preference itself and omit factual evidence side clauses that duplicate facts.md.",
					"- For stable routines or identity-like descriptors, choose one canonical home based on meaning: observable routines/capabilities belong in facts.md, while desired schedules, styles, or choices belong in preferences.md.",
					"- If a claim is phrased as prefers, likes, enjoys, favorite, taste, style, wants, or chooses, its canonical home is preferences.md unless it is purely a factual event or capability.",
					"- Keep conflicts between user preferences in preferences.md as unresolved tensions; do not duplicate the conflict in facts.md unless there is a separate objective fact to record.",
					"- If current semantic files already duplicate a preference-like claim in facts.md and preferences.md, remove it from facts.md and keep or reconcile it in preferences.md.",
					"- Use projects.md only for stable project knowledge or multi-turn project context that belongs beyond the current working-memory window.",
					"- Do not bias toward active project context when equally durable personal facts or user preferences are supported by the evidence.",
					"- If a durable fact or preference is explicit and useful across future sessions, prefer promoting it rather than leaving it only in working memory.",
					"- Do not copy raw transcript detail into semantic memory.",
					"- Keep entries concise, operator-auditable, and easy to edit by hand.",
				),
			},
			{
				Name: "semantic_memory_execution_order",
				Body: renderPromptLines(
					"1. Inspect all three semantic-memory targets, the working-memory snapshot, the verification review artifact, and the transcript artifact.",
					"2. Identify durable promotions across both user-profile knowledge and project knowledge, stale entries to remove or rewrite, and uncertainty that must stay explicit.",
					"3. Normalize any non-canonical semantic headings back into the fixed target sections before finalizing content.",
					"4. Use file tools on each semantic target, editing only the files that need updates and reading the rest.",
					"5. Run a cross-file de-duplication pass: remove fact side clauses from preferences.md and preference phrasing from facts.md before finalizing.",
					"6. Re-check every target against the semantic_memory_rules before finalizing.",
					"7. Only then emit the short internal summary.",
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
					"- Use it only to maintain the semantic-memory files for future cognition and replies.",
				),
			},
		},
		ExposeTools:        true,
		RequireToolCalling: true,
		AllowedTools:       append([]string(nil), semanticMemoryAllowedTools...),
		ToolCallPolicy: filetools.PathAccessPolicy{
			ReadPaths:  semanticRuntimePaths(),
			WritePaths: semanticRuntimePaths(),
		},
	}, nil
}

func (semanticMemoryExtractionJob) ApplyResult(
	_ context.Context,
	_ ContextLoader,
	output JobOutput,
) (ParsedResult, error) {
	targets := semanticTargetCoverage(
		output.Messages,
		semanticFactsRuntimePath,
		semanticPreferencesRuntimePath,
		semanticProjectsRuntimePath,
	)
	missing := make([]string, 0, len(targets))
	changed := false
	for _, path := range semanticRuntimePaths() {
		status := targets[path]
		if !status.inspected {
			missing = append(missing, path)
			continue
		}
		if status.mutated {
			changed = true
		}
	}
	if len(missing) > 0 {
		return ParsedResult{}, fmt.Errorf(
			"semantic-memory run must inspect all canonical targets with file tools (missing=%s final_text=%q)",
			strings.Join(missing, ", "),
			compactSemanticMemoryText(output.FinalText),
		)
	}

	metadata := map[string]string{
		"facts_path":          semanticFactsRuntimePath,
		"preferences_path":    semanticPreferencesRuntimePath,
		"projects_path":       semanticProjectsRuntimePath,
		"changed":             strconv.FormatBool(changed),
		"facts_changed":       strconv.FormatBool(targets[semanticFactsRuntimePath].mutated),
		"preferences_changed": strconv.FormatBool(targets[semanticPreferencesRuntimePath].mutated),
		"projects_changed":    strconv.FormatBool(targets[semanticProjectsRuntimePath].mutated),
	}

	return ParsedResult{
		Summary:  summarizeSemanticMemoryNotes(output.FinalText),
		Metadata: metadata,
	}, nil
}

func evaluateSemanticMemoryState(
	_ context.Context,
	snapshot Snapshot,
	state JobState,
) (bool, string, error) {
	shouldRun, reason := shouldRunSemanticMemoryExtraction(snapshot, state)
	return shouldRun, reason, nil
}

func shouldRunSemanticMemoryExtraction(
	snapshot Snapshot,
	state JobState,
) (bool, string) {
	if snapshot.HeadLastSeq <= 0 || state.DirtySinceSeq <= 0 {
		return false, ""
	}

	dirtyTurns := snapshot.HeadLastSeq - state.DirtySinceSeq + 1
	if dirtyTurns < semanticMemoryMinDirtyTurns {
		return false, ""
	}

	return true, fmt.Sprintf(
		"dirty_tail=%d dirty_since_seq=%d head_seq=%d",
		dirtyTurns,
		state.DirtySinceSeq,
		snapshot.HeadLastSeq,
	)
}

type semanticTargetStatus struct {
	inspected bool
	mutated   bool
}

func semanticMemoryContentMap(semantic agent.SemanticMemory) map[string]string {
	out := make(map[string]string, len(semantic.Files))
	for _, file := range semantic.Files {
		path := strings.TrimSpace(file.RelativePath)
		if path == "" {
			continue
		}
		out[path] = strings.TrimSpace(file.Content)
	}
	return out
}

func semanticContentForPath(
	files map[string]string,
	relativePath string,
) (string, error) {
	content := strings.TrimSpace(files[relativePath])
	if content == "" {
		return "", fmt.Errorf("missing canonical semantic memory file %q", relativePath)
	}
	return content, nil
}

func semanticRuntimePaths() []string {
	return []string{
		semanticFactsRuntimePath,
		semanticPreferencesRuntimePath,
		semanticProjectsRuntimePath,
	}
}

func semanticTargetCoverage(
	messages []conversation.Message,
	targetPaths ...string,
) map[string]semanticTargetStatus {
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

	targets := make(map[string]semanticTargetStatus, len(targetPaths))
	for _, path := range targetPaths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		targets[path] = semanticTargetStatus{}
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
			for path, status := range targets {
				if semanticAccessIncludesPath(access.WritePaths, path) {
					status.inspected = true
					status.mutated = true
					targets[path] = status
					continue
				}
				if semanticAccessIncludesPath(access.ReadPaths, path) {
					status.inspected = true
					targets[path] = status
				}
			}
		}
	}
	return targets
}

func semanticAccessIncludesPath(paths []string, target string) bool {
	for _, path := range paths {
		if path == target {
			return true
		}
	}
	return false
}

func summarizeSemanticMemoryNotes(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		return compactSemanticMemoryText(line)
	}
	return "Semantic-memory maintenance completed."
}

func compactSemanticMemoryText(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) <= 200 {
		return text
	}
	return text[:197] + "..."
}
