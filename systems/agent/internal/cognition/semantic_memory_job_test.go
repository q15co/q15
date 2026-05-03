package cognition

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
)

type semanticMemoryJobLoader struct {
	semantic                agent.SemanticMemory
	working                 agent.WorkingMemory
	artifacts               map[string]Artifact
	recent                  []conversation.Message
	checkpoint              SemanticExtractionCheckpoint
	loadSemanticCalls       int
	loadLatestTurns         int
	loadRecentTurns         int
	loadMessagesSinceSeqSeq int64
	loadCheckpointCalls     int
	loadArtifacts           []string
}

func (l *semanticMemoryJobLoader) LoadCoreMemory(context.Context) (agent.CoreMemory, error) {
	return agent.CoreMemory{}, nil
}

func (l *semanticMemoryJobLoader) LoadSemanticMemory(
	context.Context,
) (agent.SemanticMemory, error) {
	l.loadSemanticCalls++
	return l.semantic, nil
}

func (l *semanticMemoryJobLoader) LoadWorkingMemory(context.Context) (agent.WorkingMemory, error) {
	return l.working, nil
}

func (l *semanticMemoryJobLoader) LoadSkillCatalog(context.Context) (agent.SkillCatalog, error) {
	return agent.SkillCatalog{}, nil
}

func (l *semanticMemoryJobLoader) LoadRecentMessages(
	_ context.Context,
	turns int,
) ([]conversation.Message, error) {
	l.loadRecentTurns = turns
	return conversation.CloneMessages(l.recent), nil
}

func (l *semanticMemoryJobLoader) LoadLatestMessages(
	_ context.Context,
	turns int,
) ([]conversation.Message, error) {
	l.loadLatestTurns = turns
	return conversation.CloneMessages(l.recent), nil
}

func (l *semanticMemoryJobLoader) LoadMessagesSinceSeq(
	_ context.Context,
	afterSeq int64,
) ([]conversation.Message, error) {
	l.loadMessagesSinceSeqSeq = afterSeq
	return conversation.CloneMessages(l.recent), nil
}

func (l *semanticMemoryJobLoader) LoadHead(context.Context) (int64, time.Time, error) {
	return 0, time.Time{}, nil
}

func (l *semanticMemoryJobLoader) LoadConsolidationCheckpoint(
	context.Context,
) (ConsolidationCheckpoint, error) {
	return ConsolidationCheckpoint{}, nil
}

func (l *semanticMemoryJobLoader) LoadSemanticExtractionCheckpoint(
	context.Context,
) (SemanticExtractionCheckpoint, error) {
	l.loadCheckpointCalls++
	return l.checkpoint, nil
}

func (l *semanticMemoryJobLoader) LoadCognitionArtifact(
	_ context.Context,
	relativePath string,
) (Artifact, error) {
	l.loadArtifacts = append(l.loadArtifacts, relativePath)
	if l.artifacts == nil {
		return Artifact{}, nil
	}
	return l.artifacts[relativePath], nil
}

func (l *semanticMemoryJobLoader) StoreCognitionArtifact(
	context.Context,
	Artifact,
) error {
	return nil
}

func TestSemanticMemoryExtractionRegistration(t *testing.T) {
	t.Parallel()

	registration := NewSemanticMemoryExtractionRegistration()
	job := registration.NewJob()
	if job == nil {
		t.Fatal("NewJob() = nil")
	}
	if got, want := job.Type(), semanticMemoryExtractionJobType; got != want {
		t.Fatalf("Type() = %q, want %q", got, want)
	}
	if len(registration.Policy.Startup) != 0 {
		t.Fatalf("startup rules = %d, want 0", len(registration.Policy.Startup))
	}
	if len(registration.Policy.Schedule) != 1 {
		t.Fatalf("schedule rules = %d, want 1", len(registration.Policy.Schedule))
	}
	if got, want := registration.Policy.Schedule[0].Spec, semanticMemoryScheduleSpec; got != want {
		t.Fatalf("schedule spec = %q, want %q", got, want)
	}
	if len(registration.Policy.State) != 1 {
		t.Fatalf("state rules = %d, want 1", len(registration.Policy.State))
	}
}

func TestSemanticMemoryExtractionBuildLoadsSemanticWorkingVerificationAndTranscript(t *testing.T) {
	t.Parallel()

	userTimestamp := time.Date(
		2026,
		time.April,
		12,
		10,
		11,
		12,
		0,
		time.FixedZone("UTC+2", 2*60*60),
	)
	loader := &semanticMemoryJobLoader{
		semantic: agent.SemanticMemory{
			Files: []agent.SemanticMemoryFile{
				{
					RelativePath: semanticFactsRelativePath,
					Content:      "# Semantic Facts\n\n## Confirmed Facts\n\n- The user maintains q15.\n\n## Grounded Inferences\n\n- None\n",
				},
				{
					RelativePath: semanticPreferencesRelativePath,
					Content:      "# Semantic Preferences\n\n## User Preferences\n\n- Prefer conservative memory updates.\n\n## Collaboration Preferences\n\n- None\n",
				},
				{
					RelativePath: semanticProjectsRelativePath,
					Content:      "# Semantic Projects\n\n## Active Projects\n\n- Build semantic memory extraction.\n\n## Durable Project Knowledge\n\n- None\n",
				},
			},
		},
		working: agent.WorkingMemory{
			RelativePath: workingMemoryRelativePath,
			Content:      "# Working Memory\n\n## Active Tasks\n\n- Add semantic extraction.\n",
		},
		artifacts: map[string]Artifact{
			VerificationReviewPath: {
				RelativePath: VerificationReviewPath,
				Content:      "# Verification Review\n\n## Issues Identified\n\n- Remove unsupported durable claims.\n",
			},
		},
		recent: []conversation.Message{
			{
				Role: conversation.UserRole,
				Parts: []conversation.Part{
					conversation.Text("I prefer conservative promotion rules.", ""),
				},
				UserTemporal: &conversation.UserTemporalMetadata{
					TimeLocal:            userTimestamp,
					SincePrevUserMessage: conversation.NewDuration(3*time.Minute + 42*time.Second),
				},
			},
			conversation.AssistantMessage(
				conversation.Text("I will keep promotions conservative.", ""),
			),
		},
	}

	spec, err := NewSemanticMemoryExtractionRegistration().NewJob().Build(
		context.Background(),
		loader,
	)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if !spec.ExposeTools {
		t.Fatal("ExposeTools = false, want true")
	}
	if !spec.RequireToolCalling {
		t.Fatal("RequireToolCalling = false, want true")
	}
	if !reflect.DeepEqual(spec.AllowedTools, semanticMemoryAllowedTools) {
		t.Fatalf("AllowedTools = %#v, want %#v", spec.AllowedTools, semanticMemoryAllowedTools)
	}
	if spec.ToolCallPolicy == nil {
		t.Fatal("ToolCallPolicy = nil, want semantic path policy")
	}
	if err := spec.ToolCallPolicy.CheckToolCall(agent.ToolCall{
		ID:        "call-allowed",
		Name:      "write_file",
		Arguments: fmt.Sprintf(`{"path":%q,"content":"# Semantic Facts"}`, semanticFactsRuntimePath),
	}); err != nil {
		t.Fatalf("ToolCallPolicy allowed write error = %v", err)
	}
	if err := spec.ToolCallPolicy.CheckToolCall(agent.ToolCall{
		ID:        "call-denied",
		Name:      "write_file",
		Arguments: `{"path":"/memory/core/AGENT.md","content":"x"}`,
	}); err == nil || !contains(err.Error(), "outside allowed write paths") {
		t.Fatalf("ToolCallPolicy denied write error = %v, want path policy rejection", err)
	}
	if got, want := loader.loadSemanticCalls, 1; got != want {
		t.Fatalf("LoadSemanticMemory calls = %d, want %d", got, want)
	}
	if got, want := loader.loadLatestTurns, semanticMemoryRecentTurns; got != want {
		t.Fatalf("LoadLatestMessages turns = %d, want %d", got, want)
	}
	if got, want := loader.loadRecentTurns, 0; got != want {
		t.Fatalf("LoadRecentMessages turns = %d, want %d", got, want)
	}
	if got, want := loader.loadMessagesSinceSeqSeq, int64(0); got != want {
		t.Fatalf("LoadMessagesSinceSeq seq = %d, want %d", got, want)
	}
	if got, want := loader.loadCheckpointCalls, 1; got != want {
		t.Fatalf("LoadSemanticExtractionCheckpoint calls = %d, want %d", got, want)
	}
	if got, want := len(loader.loadArtifacts), 1; got != want {
		t.Fatalf("LoadCognitionArtifact calls = %d, want %d", got, want)
	}
	if got, want := loader.loadArtifacts[0], VerificationReviewPath; got != want {
		t.Fatalf("LoadCognitionArtifact path = %q, want %q", got, want)
	}

	prompt, err := renderPrompt(semanticMemoryExtractionJobType, spec)
	if err != nil {
		t.Fatalf("renderPrompt() error = %v", err)
	}
	for _, want := range []string{
		semanticFactsRuntimePath,
		semanticPreferencesRuntimePath,
		semanticProjectsRuntimePath,
		"<semantic_facts_target",
		"<semantic_preferences_target",
		"<semantic_projects_target",
		"<working_memory_snapshot",
		"<verification_review_input",
		"<semantic_memory_rules>",
		"<semantic_memory_execution_order>",
		"This is a background maintenance job, not a user-facing reply.",
		"Extract conservative durable knowledge into the canonical semantic-memory files.",
		"Treat durable user-profile knowledge as first-class semantic memory, not just active project context.",
		"Always inspect all three semantic-memory target files with file tools during this run.",
		"Call read_file, write_file, edit_file, or apply_patch on each of /memory/semantic/facts.md, /memory/semantic/preferences.md, and /memory/semantic/projects.md before your final response.",
		"If canonical semantic files have drifted to non-canonical headings, normalize them back to the fixed schema during this run.",
		"After editing, be precise about which target files changed versus which were only inspected; do not claim a file was updated unless you wrote, edited, or patched it.",
		"Promote only explicit durable user statements or claims corroborated by repeated evidence in the provided context.",
		"Repeated protocol, questionnaire, or profile-building answers count as repeated evidence when they consistently describe stable personal facts or preferences.",
		"Prefer verified or explicitly grounded claims over speculative inferences.",
		"If verification flags a semantic entry as stale, unsupported, or contradicted, remove it, rewrite it, or downgrade it with explicit uncertainty.",
		"If current semantic files contain drifted headings such as Personal Facts or Technical Facts, fold those entries back into the canonical headings and remove the drift.",
		"Never promote single-turn temporary tasks, transient session context, speculative tool-output guesses, or generic process chatter.",
		"Maintain strict category separation: do not duplicate the same claim across facts.md and preferences.md.",
		"Use facts.md -> Confirmed Facts only for objective durable facts: biography, stable routines, language abilities, owned pets, capabilities, past events, project facts, and tool/runtime facts.",
		"Do not put subjective choices, tastes, favorites, likes, dislikes, or preferred styles in facts.md just because the user stated them explicitly.",
		"Keep uncertain but useful durable claims only under facts.md -> Grounded Inferences, with uncertainty labeled explicitly.",
		"Use preferences.md -> User Preferences for durable subjective preferences: likes, dislikes, favorite categories, tastes, lifestyle choices, entertainment choices, learning style, work style, social style, and decision style.",
		"Do not use preferences.md to restate objective facts, biography, capabilities, past events, or tool/runtime facts; keep those in facts.md unless the entry is explicitly about a preference.",
		"When a preference needs context, phrase it as the preference itself and omit factual evidence side clauses that duplicate facts.md.",
		"For stable routines or identity-like descriptors, choose one canonical home based on meaning: observable routines/capabilities belong in facts.md, while desired schedules, styles, or choices belong in preferences.md.",
		"If a claim is phrased as prefers, likes, enjoys, favorite, taste, style, wants, or chooses, its canonical home is preferences.md unless it is purely a factual event or capability.",
		"Keep conflicts between user preferences in preferences.md as unresolved tensions; do not duplicate the conflict in facts.md unless there is a separate objective fact to record.",
		"If current semantic files already duplicate a preference-like claim in facts.md and preferences.md, remove it from facts.md and keep or reconcile it in preferences.md.",
		"Use projects.md only for stable project knowledge or multi-turn project context that belongs beyond the current working-memory window.",
		"Do not bias toward active project context when equally durable personal facts or user preferences are supported by the evidence.",
		"If a durable fact or preference is explicit and useful across future sessions, prefer promoting it rather than leaving it only in working memory.",
		"Do not copy transcript detail or verification-review prose verbatim into semantic memory.",
		"Run a cross-file de-duplication pass: remove fact side clauses from preferences.md and preference phrasing from facts.md before finalizing.",
		"A bounded replay slice of episodic history, selected by the initial semantic replay fallback window independent of the working-memory consolidation checkpoint and capped at 32 turns, is included below as a transcript artifact.",
		`<message_meta day_of_week_local="Sunday" timestamp_local="20260412T101112+0200" since_prev_user_message="3m42s"/>`,
		"Remove unsupported durable claims.",
	} {
		if !contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestSemanticMemoryExtractionBuildUsesSemanticCheckpointReplay(t *testing.T) {
	t.Parallel()

	loader := &semanticMemoryJobLoader{
		checkpoint: SemanticExtractionCheckpoint{LastExtractedSeq: 7},
		semantic: agent.SemanticMemory{
			Files: []agent.SemanticMemoryFile{
				{
					RelativePath: semanticFactsRelativePath,
					Content:      "# Semantic Facts\n\n## Confirmed Facts\n\n- None\n\n## Grounded Inferences\n\n- None\n",
				},
				{
					RelativePath: semanticPreferencesRelativePath,
					Content:      "# Semantic Preferences\n\n## User Preferences\n\n- None\n\n## Collaboration Preferences\n\n- None\n",
				},
				{
					RelativePath: semanticProjectsRelativePath,
					Content:      "# Semantic Projects\n\n## Active Projects\n\n- None\n\n## Durable Project Knowledge\n\n- None\n",
				},
			},
		},
		working: agent.WorkingMemory{
			RelativePath: workingMemoryRelativePath,
			Content:      "# Working Memory\n\n## Active Tasks\n\n- None\n",
		},
		recent: []conversation.Message{
			conversation.UserMessage("New durable preference after checkpoint."),
			conversation.AssistantMessage(conversation.Text("Noted.", "")),
		},
	}

	spec, err := NewSemanticMemoryExtractionRegistration().NewJob().Build(
		context.Background(),
		loader,
	)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got, want := loader.loadCheckpointCalls, 1; got != want {
		t.Fatalf("LoadSemanticExtractionCheckpoint calls = %d, want %d", got, want)
	}
	if got, want := loader.loadMessagesSinceSeqSeq, int64(7); got != want {
		t.Fatalf("LoadMessagesSinceSeq seq = %d, want %d", got, want)
	}
	if got, want := loader.loadLatestTurns, 0; got != want {
		t.Fatalf("LoadLatestMessages turns = %d, want %d", got, want)
	}

	prompt, err := renderPrompt(semanticMemoryExtractionJobType, spec)
	if err != nil {
		t.Fatalf("renderPrompt() error = %v", err)
	}
	if !contains(
		prompt,
		"A semantic extraction replay slice of episodic history after semantic extraction checkpoint seq 7 is included below as a transcript artifact.",
	) {
		t.Fatalf("prompt missing semantic checkpoint replay scope:\n%s", prompt)
	}
	if contains(prompt, "capped at 32 turns") {
		t.Fatalf("prompt unexpectedly described checkpoint replay as capped:\n%s", prompt)
	}
}

func TestSemanticMemoryExtractionBuildRejectsIncompleteCanonicalSemanticMemory(t *testing.T) {
	t.Parallel()

	loader := &semanticMemoryJobLoader{
		semantic: agent.SemanticMemory{
			Files: []agent.SemanticMemoryFile{
				{
					RelativePath: semanticFactsRelativePath,
					Content:      "# Semantic Facts\n\n## Confirmed Facts\n\n- None\n\n## Grounded Inferences\n\n- None\n",
				},
				{
					RelativePath: semanticPreferencesRelativePath,
					Content:      "# Semantic Preferences\n\n## User Preferences\n\n- None\n\n## Collaboration Preferences\n\n- None\n",
				},
			},
		},
	}

	_, err := NewSemanticMemoryExtractionRegistration().NewJob().Build(
		context.Background(),
		loader,
	)
	if err == nil ||
		!contains(err.Error(), `missing canonical semantic memory file "semantic/projects.md"`) {
		t.Fatalf("Build() error = %v, want missing canonical semantic file", err)
	}
}

func TestSemanticMemoryExtractionApplyResultRejectsMissingTargetCoverage(t *testing.T) {
	t.Parallel()

	_, err := NewSemanticMemoryExtractionRegistration().NewJob().ApplyResult(
		context.Background(),
		nil,
		JobOutput{
			Type:      semanticMemoryExtractionJobType,
			FinalText: "Semantic memory is current.",
			Messages: []conversation.Message{
				conversation.AssistantMessage(
					conversation.ToolCall(
						"call-1",
						"read_file",
						fmt.Sprintf(`{"path":%q}`, semanticFactsRuntimePath),
					),
				),
				conversation.ToolResultMessage(
					"call-1",
					"Path: /memory/semantic/facts.md\n# Semantic Facts\n...",
					false,
				),
			},
		},
	)
	if err == nil {
		t.Fatal("ApplyResult() error = nil, want non-nil")
	}
}

func TestSemanticMemoryExtractionApplyResultIgnoresFailedAndMisleadingToolResults(t *testing.T) {
	t.Parallel()

	_, err := NewSemanticMemoryExtractionRegistration().NewJob().ApplyResult(
		context.Background(),
		nil,
		JobOutput{
			Type:      semanticMemoryExtractionJobType,
			FinalText: "Semantic memory is current.",
			Messages: []conversation.Message{
				conversation.AssistantMessage(
					conversation.ToolCall(
						"call-1",
						"read_file",
						fmt.Sprintf(`{"path":%q}`, semanticFactsRuntimePath),
					),
				),
				conversation.ToolResultMessage(
					"call-1",
					"Path: /memory/semantic/facts.md\nSee /memory/semantic/preferences.md and /memory/semantic/projects.md.",
					false,
				),
				conversation.AssistantMessage(
					conversation.ToolCall(
						"call-2",
						"read_file",
						fmt.Sprintf(`{"path":%q}`, semanticPreferencesRuntimePath),
					),
				),
				conversation.ToolResultMessage(
					"call-2",
					"tool error: file not found: /memory/semantic/preferences.md",
					true,
				),
			},
		},
	)
	if err == nil {
		t.Fatal("ApplyResult() error = nil, want missing target coverage")
	}
	if !contains(err.Error(), semanticPreferencesRuntimePath) ||
		!contains(err.Error(), semanticProjectsRuntimePath) {
		t.Fatalf("ApplyResult() error = %v, want preferences and projects missing", err)
	}
}

func TestSemanticMemoryExtractionApplyResultTracksChangedMetadata(t *testing.T) {
	t.Parallel()

	result, err := NewSemanticMemoryExtractionRegistration().NewJob().ApplyResult(
		context.Background(),
		nil,
		JobOutput{
			Type:      semanticMemoryExtractionJobType,
			FinalText: "Updated semantic memory conservatively.",
			Messages: []conversation.Message{
				conversation.AssistantMessage(
					conversation.ToolCall(
						"call-1",
						"edit_file",
						fmt.Sprintf(`{"path":%q}`, semanticFactsRuntimePath),
					),
				),
				conversation.ToolResultMessage(
					"call-1",
					"Path: /memory/semantic/facts.md\nFirst-Changed-Line: 5\n--- DIFF ---\n...",
					false,
				),
				conversation.AssistantMessage(
					conversation.ToolCall(
						"call-2",
						"read_file",
						fmt.Sprintf(`{"path":%q}`, semanticPreferencesRuntimePath),
					),
				),
				conversation.ToolResultMessage(
					"call-2",
					"Path: /memory/semantic/preferences.md\n# Semantic Preferences\n...",
					false,
				),
				conversation.AssistantMessage(
					conversation.ToolCall(
						"call-3",
						"apply_patch",
						fmt.Sprintf(
							`{"patch":"*** Begin Patch\n*** Update File: %s\n@@\n-old\n+new\n*** End Patch"}`,
							semanticProjectsRuntimePath,
						),
					),
				),
				conversation.ToolResultMessage(
					"call-3",
					"Path: /memory/semantic/projects.md\nFirst-Changed-Line: 7\n--- DIFF ---\n...",
					false,
				),
			},
		},
	)
	if err != nil {
		t.Fatalf("ApplyResult() error = %v", err)
	}
	if got, want := result.Metadata["facts_path"], semanticFactsRuntimePath; got != want {
		t.Fatalf("Metadata[facts_path] = %q, want %q", got, want)
	}
	if got, want := result.Metadata["preferences_path"], semanticPreferencesRuntimePath; got != want {
		t.Fatalf("Metadata[preferences_path] = %q, want %q", got, want)
	}
	if got, want := result.Metadata["projects_path"], semanticProjectsRuntimePath; got != want {
		t.Fatalf("Metadata[projects_path] = %q, want %q", got, want)
	}
	if got, want := result.Metadata["changed"], "true"; got != want {
		t.Fatalf("Metadata[changed] = %q, want %q", got, want)
	}
	if got, want := result.Metadata["facts_changed"], "true"; got != want {
		t.Fatalf("Metadata[facts_changed] = %q, want %q", got, want)
	}
	if got, want := result.Metadata["preferences_changed"], "false"; got != want {
		t.Fatalf("Metadata[preferences_changed] = %q, want %q", got, want)
	}
	if got, want := result.Metadata["projects_changed"], "true"; got != want {
		t.Fatalf("Metadata[projects_changed] = %q, want %q", got, want)
	}
	if got := result.Summary; !contains(got, "Updated semantic memory") {
		t.Fatalf("Summary = %q, want inferred summary", got)
	}
}

func TestSemanticMemoryExtractionApplyResultUsesFalseChangedForReadOnlyInspection(t *testing.T) {
	t.Parallel()

	result, err := NewSemanticMemoryExtractionRegistration().NewJob().ApplyResult(
		context.Background(),
		nil,
		JobOutput{
			Type: semanticMemoryExtractionJobType,
			Messages: []conversation.Message{
				conversation.AssistantMessage(
					conversation.ToolCall(
						"call-1",
						"read_file",
						fmt.Sprintf(`{"path":%q}`, semanticFactsRuntimePath),
					),
				),
				conversation.ToolResultMessage(
					"call-1",
					"Path: /memory/semantic/facts.md\n# Semantic Facts\n...",
					false,
				),
				conversation.AssistantMessage(
					conversation.ToolCall(
						"call-2",
						"read_file",
						fmt.Sprintf(`{"path":%q}`, semanticPreferencesRuntimePath),
					),
				),
				conversation.ToolResultMessage(
					"call-2",
					"Path: /memory/semantic/preferences.md\n# Semantic Preferences\n...",
					false,
				),
				conversation.AssistantMessage(
					conversation.ToolCall(
						"call-3",
						"read_file",
						fmt.Sprintf(`{"path":%q}`, semanticProjectsRuntimePath),
					),
				),
				conversation.ToolResultMessage(
					"call-3",
					"Path: /memory/semantic/projects.md\n# Semantic Projects\n...",
					false,
				),
			},
		},
	)
	if err != nil {
		t.Fatalf("ApplyResult() error = %v", err)
	}
	if got, want := result.Metadata["changed"], "false"; got != want {
		t.Fatalf("Metadata[changed] = %q, want %q", got, want)
	}
	if got, want := result.Metadata["facts_changed"], "false"; got != want {
		t.Fatalf("Metadata[facts_changed] = %q, want %q", got, want)
	}
	if got, want := result.Metadata["preferences_changed"], "false"; got != want {
		t.Fatalf("Metadata[preferences_changed] = %q, want %q", got, want)
	}
	if got, want := result.Metadata["projects_changed"], "false"; got != want {
		t.Fatalf("Metadata[projects_changed] = %q, want %q", got, want)
	}
	if got, want := result.Summary, "Semantic-memory maintenance completed."; got != want {
		t.Fatalf("Summary = %q, want %q", got, want)
	}
}
