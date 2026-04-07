package cognition

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/fileops"
	"github.com/q15co/q15/systems/agent/internal/tools"
)

type workingMemoryJobLoader struct {
	working         agent.WorkingMemory
	recent          []conversation.Message
	loadRecentTurns int
}

func (l *workingMemoryJobLoader) LoadCoreMemory(context.Context) (agent.CoreMemory, error) {
	return agent.CoreMemory{}, nil
}

func (l *workingMemoryJobLoader) LoadWorkingMemory(context.Context) (agent.WorkingMemory, error) {
	return l.working, nil
}

func (l *workingMemoryJobLoader) LoadSkillCatalog(context.Context) (agent.SkillCatalog, error) {
	return agent.SkillCatalog{}, nil
}

func (l *workingMemoryJobLoader) LoadRecentMessages(
	_ context.Context,
	turns int,
) ([]conversation.Message, error) {
	l.loadRecentTurns = turns
	return conversation.CloneMessages(l.recent), nil
}

func toolCallResult(id, name, arguments string) agent.ModelClientResult {
	return agent.ModelClientResult{
		Messages: []conversation.Message{
			conversation.AssistantMessage(conversation.ToolCall(id, name, arguments)),
		},
		FinishReason: "tool_calls",
	}
}

func TestWorkingMemoryConsolidationRegistration(t *testing.T) {
	t.Parallel()

	registration := NewWorkingMemoryConsolidationRegistration()
	job := registration.NewJob()
	if job == nil {
		t.Fatal("NewJob() = nil")
	}
	if got, want := job.Type(), workingMemoryConsolidationJobType; got != want {
		t.Fatalf("Type() = %q, want %q", got, want)
	}
	if len(registration.Policy.Startup) != 1 {
		t.Fatalf("startup rules = %d, want 1", len(registration.Policy.Startup))
	}
	if len(registration.Policy.State) != 1 {
		t.Fatalf("state rules = %d, want 1", len(registration.Policy.State))
	}
	if len(registration.Policy.Schedule) != 1 {
		t.Fatalf("schedule rules = %d, want 1", len(registration.Policy.Schedule))
	}
	if got, want := registration.Policy.Schedule[0].Spec, workingMemoryScheduleSpec; got != want {
		t.Fatalf("schedule spec = %q, want %q", got, want)
	}
}

func TestWorkingMemoryConsolidationBuildLoadsWorkingMemoryAndTranscript(t *testing.T) {
	t.Parallel()

	loader := &workingMemoryJobLoader{
		working: agent.WorkingMemory{
			RelativePath: workingMemoryRelativePath,
			Content:      "# Working Memory\n\n## Active Tasks\n\n- Fix bot startup\n",
		},
		recent: []conversation.Message{
			conversation.UserMessage("Please stabilize startup."),
			conversation.AssistantMessage(conversation.Text("I updated bot.go", "")),
		},
	}

	spec, err := NewWorkingMemoryConsolidationRegistration().NewJob().Build(
		context.Background(),
		loader,
	)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if !spec.ExposeTools {
		t.Fatal("ExposeTools = false, want true")
	}
	if got, want := loader.loadRecentTurns, workingMemoryRecentTurns; got != want {
		t.Fatalf("LoadRecentMessages turns = %d, want %d", got, want)
	}
	if len(spec.InputMessages) != len(loader.recent) {
		t.Fatalf("InputMessages len = %d, want %d", len(spec.InputMessages), len(loader.recent))
	}

	prompt, err := renderPrompt(workingMemoryConsolidationJobType, spec)
	if err != nil {
		t.Fatalf("renderPrompt() error = %v", err)
	}
	for _, want := range []string{
		workingMemoryRuntimePath,
		"<working_memory_target",
		"<working_memory_rules>",
		"This is a background maintenance job, not a user-facing reply.",
		"Do not answer questions from the transcript or continue the conversation.",
		"Prefer what will matter for the next reply over archival summaries of older context.",
		"Call read_file, write_file, edit_file, or apply_patch on the target before your final response. A response without a target-file tool call is invalid.",
		"Current Priorities, Active Tasks, Open Threads, Recent Progress, Pending Checks, Temporary Context.",
		"Preserve the current user goal, active debugging thread, and unresolved questions from the latest turns even when they are temporary.",
		"Prioritize the latest unresolved user request and the constraints that should shape the next reply.",
		"If recent turns correct an earlier misunderstanding, recommendation, or assumption, keep the corrected state and drop the stale one.",
		"If the user adds a concrete constraint or preference that matters for the next few turns, keep it in memory.",
		"If the user is still deciding, asking follow-up questions, or waiting on information, Active Tasks must contain that ongoing work; use None only when there is no active task.",
		"When the recent transcript is about debugging or validation, keep the current hypothesis, observed behavior, and what still needs to be checked.",
		"Favor newer constraints and corrections over older meta-discussion when they compete for space.",
		"Keep uncertainty explicit: if evidence was blocked, partial, or inferred, write that it is unverified or likely instead of storing it as a confirmed fact.",
		"Do not promote guesses from failed lookups or tool errors into settled memory.",
	} {
		if !contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestWorkingMemoryConsolidationParseResultRejectsMissingToolUse(t *testing.T) {
	t.Parallel()

	_, err := NewWorkingMemoryConsolidationRegistration().NewJob().ParseResult(
		context.Background(),
		JobOutput{
			Type:      workingMemoryConsolidationJobType,
			FinalText: "Working memory looks current.",
		},
	)
	if err == nil {
		t.Fatal("ParseResult() error = nil, want non-nil")
	}
}

func TestWorkingMemoryConsolidationParseResultAcceptsAnyNotesAfterTargetInspection(t *testing.T) {
	t.Parallel()

	result, err := NewWorkingMemoryConsolidationRegistration().NewJob().ParseResult(
		context.Background(),
		JobOutput{
			Type:      workingMemoryConsolidationJobType,
			FinalText: "Updated working memory to reflect the latest active tasks.",
			Messages: []conversation.Message{
				conversation.AssistantMessage(
					conversation.ToolCall(
						"call-1",
						"edit_file",
						fmt.Sprintf(`{"path":%q}`, workingMemoryRuntimePath),
					),
				),
				conversation.ToolResultMessage(
					"call-1",
					"Path: /memory/working/WORKING_MEMORY.md\nFirst-Changed-Line: 5\n--- DIFF ---\n...",
					false,
				),
			},
		},
	)
	if err != nil {
		t.Fatalf("ParseResult() error = %v", err)
	}
	if got, want := result.Metadata["path"], workingMemoryRuntimePath; got != want {
		t.Fatalf("result.Metadata[path] = %q, want %q", got, want)
	}
	if got := result.Summary; !contains(got, "Updated working memory") {
		t.Fatalf("result.Summary = %q, want inferred summary", got)
	}
}

func TestWorkingMemoryConsolidationParseResultTreatsFinalTextAsNotesOnly(t *testing.T) {
	t.Parallel()

	result, err := NewWorkingMemoryConsolidationRegistration().NewJob().ParseResult(
		context.Background(),
		JobOutput{
			Type:      workingMemoryConsolidationJobType,
			FinalText: "Left it unchanged.",
			Messages: []conversation.Message{
				conversation.AssistantMessage(
					conversation.ToolCall(
						"call-1",
						"edit_file",
						fmt.Sprintf(`{"path":%q}`, workingMemoryRuntimePath),
					),
				),
				conversation.ToolResultMessage(
					"call-1",
					"Path: /memory/working/WORKING_MEMORY.md\nFirst-Changed-Line: 5\n--- DIFF ---\n...",
					false,
				),
			},
		},
	)
	if err != nil {
		t.Fatalf("ParseResult() error = %v", err)
	}
	if got, want := result.Metadata["path"], workingMemoryRuntimePath; got != want {
		t.Fatalf("result.Metadata[path] = %q, want %q", got, want)
	}
	if got, want := result.Summary, "Left it unchanged."; got != want {
		t.Fatalf("result.Summary = %q, want %q", got, want)
	}
}

func TestWorkingMemoryConsolidationParseResultUsesDefaultSummaryWhenNotesMissing(t *testing.T) {
	t.Parallel()

	result, err := NewWorkingMemoryConsolidationRegistration().NewJob().ParseResult(
		context.Background(),
		JobOutput{
			Type: workingMemoryConsolidationJobType,
			Messages: []conversation.Message{
				conversation.AssistantMessage(
					conversation.ToolCall(
						"call-1",
						"read_file",
						fmt.Sprintf(`{"path":%q}`, workingMemoryRuntimePath),
					),
				),
				conversation.ToolResultMessage(
					"call-1",
					"Path: /memory/working/WORKING_MEMORY.md\n# Working Memory\n...",
					false,
				),
			},
		},
	)
	if err != nil {
		t.Fatalf("ParseResult() error = %v", err)
	}
	if got, want := result.Summary, "Working-memory maintenance completed."; got != want {
		t.Fatalf("result.Summary = %q, want %q", got, want)
	}
}

func TestRunnerRunsWorkingMemoryConsolidationAndUpdatesMemory(t *testing.T) {
	memoryDir := t.TempDir()
	targetDir := filepath.Join(memoryDir, "working")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	oldContent := "# Working Memory\n\n## Current Priorities\n\n- None\n\n## Active Tasks\n\n- None\n\n## Open Threads\n\n- None\n\n## Recent Progress\n\n- None\n\n## Pending Checks\n\n- None\n\n## Temporary Context\n\n- None\n"
	newContent := "# Working Memory\n\n## Current Priorities\n\n- Stabilize startup cognition wiring.\n\n## Active Tasks\n\n- Validate background controller registration.\n\n## Open Threads\n\n- None\n\n## Recent Progress\n\n- Wired the working-memory consolidation job into runtime startup.\n\n## Pending Checks\n\n- Confirm startup and schedule triggers fire as expected.\n\n## Temporary Context\n\n- Background cognition now writes through file tools.\n"

	targetPath := filepath.Join(targetDir, "WORKING_MEMORY.md")
	if err := os.WriteFile(targetPath, []byte(oldContent), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	registry, err := agent.NewToolRegistry(
		tools.NewReadFile(fileops.NewExecutor(fileops.Settings{
			MemoryLocalDir:   memoryDir,
			MemoryRuntimeDir: "/memory",
		})),
		tools.NewEditFile(fileops.NewExecutor(fileops.Settings{
			MemoryLocalDir:   memoryDir,
			MemoryRuntimeDir: "/memory",
		})),
		tools.NewApplyPatch(fileops.NewExecutor(fileops.Settings{
			MemoryLocalDir:   memoryDir,
			MemoryRuntimeDir: "/memory",
		})),
		tools.NewWriteFile(fileops.NewExecutor(fileops.Settings{
			MemoryLocalDir:   memoryDir,
			MemoryRuntimeDir: "/memory",
		})),
	)
	if err != nil {
		t.Fatalf("NewToolRegistry() error = %v", err)
	}

	model := &fakeModelClient{
		results: []agent.ModelClientResult{
			toolCallResult(
				"call-1",
				"edit_file",
				fmt.Sprintf(
					`{"path":%q,"old_text":%q,"new_text":%q}`,
					workingMemoryRuntimePath,
					oldContent,
					newContent,
				),
			),
			assistantResult("Consolidated recent activity into working memory."),
		},
	}
	loader := &workingMemoryJobLoader{
		working: agent.WorkingMemory{
			RelativePath: workingMemoryRelativePath,
			Content:      oldContent,
		},
		recent: []conversation.Message{
			conversation.UserMessage("Please stabilize startup."),
			conversation.AssistantMessage(
				conversation.Text("I added the trigger controller wiring.", ""),
			),
		},
	}
	runner := NewRunner(model, registry, []string{"m1"}, loader)

	result, err := runner.Run(
		context.Background(),
		NewWorkingMemoryConsolidationRegistration().NewJob(),
		nil,
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := result.Type, workingMemoryConsolidationJobType; got != want {
		t.Fatalf("result.Type = %q, want %q", got, want)
	}
	if got, want := result.Metadata["path"], workingMemoryRuntimePath; got != want {
		t.Fatalf("result.Metadata[path] = %q, want %q", got, want)
	}
	if got, want := result.Summary, "Consolidated recent activity into working memory."; got != want {
		t.Fatalf("result.Summary = %q, want %q", got, want)
	}

	gotContent, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(gotContent) != newContent {
		t.Fatalf("working memory content = %q, want %q", string(gotContent), newContent)
	}
}

func TestWorkingMemoryConsolidationStateRuleUsesDirtyTailThreshold(t *testing.T) {
	t.Parallel()

	registration := NewWorkingMemoryConsolidationRegistration()
	evaluate := registration.Policy.State[0].Evaluate
	belowThresholdHead := workingMemoryMinDirtyTurns - 1
	atThresholdHead := workingMemoryMinDirtyTurns

	shouldRun, reason, err := evaluate(context.Background(), Snapshot{
		HeadLastSeq: belowThresholdHead,
	}, JobState{
		DirtySinceSeq: 1,
	})
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if shouldRun {
		t.Fatalf("shouldRun = %t, want false", shouldRun)
	}
	if reason != "" {
		t.Fatalf("reason = %q, want empty", reason)
	}

	shouldRun, reason, err = evaluate(context.Background(), Snapshot{
		HeadLastSeq: atThresholdHead,
	}, JobState{
		DirtySinceSeq: 1,
	})
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if !shouldRun {
		t.Fatal("shouldRun = false, want true")
	}
	if reason == "" {
		t.Fatal("reason = empty, want non-empty")
	}
}

func contains(text, want string) bool {
	return strings.Contains(text, want)
}
