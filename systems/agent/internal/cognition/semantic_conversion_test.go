package cognition

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
)

var updateGolden = flag.Bool("update", false, "rewrite golden files under testdata/")

func emptyConversionBaseline() map[string]string {
	return map[string]string{
		workingMemoryRuntimePath: "",
		workingLedgerRuntimePath: "",
	}
}

// conversionRunMessages returns a run transcript that reads every semantic
// root and then performs one successful file-tool write per extra path.
func conversionRunMessages(writes ...string) []conversation.Message {
	var messages []conversation.Message
	for i, target := range semanticRuntimePaths() {
		id := fmt.Sprintf("read-%d", i)
		messages = append(messages,
			conversation.AssistantMessage(
				conversation.ToolCall(id, "read_file", fmt.Sprintf(`{"path":%q}`, target)),
			),
			conversation.ToolResultMessage(id, "Path: "+target, false),
		)
	}
	for i, target := range writes {
		id := fmt.Sprintf("write-%d", i)
		messages = append(messages,
			conversation.AssistantMessage(
				conversation.ToolCall(
					id,
					"write_file",
					fmt.Sprintf(`{"path":%q,"content":"x"}`, target),
				),
			),
			conversation.ToolResultMessage(id, "Path: "+target, false),
		)
	}
	return messages
}

const conversionWorkingBefore = "# Working Memory\n\n## Active Tasks\n\n- Ship T8.\n- Deploy target switched after staging issue.\n\n## Open Threads\n\n- None\n"

const conversionLedgerBefore = "# Conversion Ledger\n\n2026-10-12T05:00Z run=semantic_memory.extract src=t1 dropped \"old chatter\" (irrelevant)"

func conversionBaseline() map[string]string {
	return map[string]string{
		workingMemoryRuntimePath: conversionWorkingBefore,
		workingLedgerRuntimePath: conversionLedgerBefore,
	}
}

func applyConversion(
	t *testing.T,
	loader *semanticMemoryJobLoader,
	baseline map[string]string,
	writes ...string,
) (ParsedResult, error) {
	t.Helper()
	return NewSemanticMemoryExtractionRegistration().NewJob().ApplyResult(
		context.Background(),
		loader,
		JobOutput{
			Type:      semanticMemoryExtractionJobType,
			Spec:      Spec{Baseline: baseline},
			FinalText: "Converted working memory.",
			Messages:  conversionRunMessages(writes...),
		},
	)
}

func TestSemanticConversionApplyResultHappyPath(t *testing.T) {
	t.Parallel()

	topic := semanticTopicsRuntimeDir + "/q15.md"
	loader := &semanticMemoryJobLoader{
		working: agent.WorkingMemory{
			RelativePath: workingMemoryRelativePath,
			Content:      "# Working Memory\n\n## Active Tasks\n\n- Ship T8.\n\n## Open Threads\n\n- None\n",
		},
		rawFiles: map[string]string{"semantic/topics/q15.md": validConversionClaim},
		ledger:   conversionLedgerBefore + "\n2026-10-14T05:00Z run=semantic_memory.extract src=t12 converted \"Deploy target switched after staging issue.\" → c-2026-0001 (semantic/topics/q15.md)\n",
		semanticSizes: map[string]int64{
			semanticFactsRelativePath: semanticRootCapBytes,
			"semantic/topics/q15.md":  semanticTopicCapBytes,
		},
	}
	result, err := applyConversion(t, loader, conversionBaseline(),
		semanticFactsRuntimePath, topic, workingMemoryRuntimePath, workingLedgerRuntimePath)
	if err != nil {
		t.Fatalf("ApplyResult() error = %v", err)
	}
	if got, want := result.Metadata["working_items_exited"], "1"; got != want {
		t.Fatalf("working_items_exited = %q, want %q", got, want)
	}
	if got, want := result.Metadata["ledger_lines_appended"], "1"; got != want {
		t.Fatalf("ledger_lines_appended = %q, want %q", got, want)
	}
}

func TestSemanticConversionApplyResultFailsOnRemovedItemsWithoutLedger(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		ledger string
	}{
		{name: "no append", ledger: conversionLedgerBefore},
		{name: "one append for two exits", ledger: conversionLedgerBefore + "\n2026-10-14T05:00Z run=semantic_memory.extract src=t12 dropped \"Ship T8.\" (done)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			loader := &semanticMemoryJobLoader{
				working: agent.WorkingMemory{
					Content: "# Working Memory\n\n## Active Tasks\n\n- None\n",
				},
				ledger: tc.ledger + "\n",
			}
			_, err := applyConversion(t, loader, conversionBaseline(), workingMemoryRuntimePath)
			if err == nil || !strings.Contains(err.Error(), "removed 2 working-memory items") {
				t.Fatalf("ApplyResult() error = %v, want ledger-completeness failure", err)
			}
		})
	}
}

func TestSemanticConversionApplyResultCountsRewrittenItemAsExit(t *testing.T) {
	t.Parallel()

	loader := &semanticMemoryJobLoader{
		working: agent.WorkingMemory{
			Content: "# Working Memory\n\n## Active Tasks\n\n- Ship T8 today.\n- Deploy target switched after staging issue.\n",
		},
		ledger: conversionLedgerBefore,
	}
	_, err := applyConversion(t, loader, conversionBaseline(), workingMemoryRuntimePath)
	if err == nil || !strings.Contains(err.Error(), "Ship T8.") {
		t.Fatalf("ApplyResult() error = %v, want rewritten bullet flagged", err)
	}
}

func TestSemanticConversionApplyResultFailsOnRewrittenLedger(t *testing.T) {
	t.Parallel()

	loader := &semanticMemoryJobLoader{
		working: agent.WorkingMemory{Content: conversionWorkingBefore},
		ledger:  "# Conversion Ledger\n\n2026-10-14T05:00Z run=semantic_memory.extract dropped \"a\"\n2026-10-14T05:00Z run=semantic_memory.extract dropped \"b\"",
	}
	_, err := applyConversion(t, loader, conversionBaseline(), workingLedgerRuntimePath)
	if err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("ApplyResult() error = %v, want append-only failure", err)
	}
}

func TestSemanticConversionApplyResultFailsOnOverCapWrittenFile(t *testing.T) {
	t.Parallel()

	topic := semanticTopicsRuntimeDir + "/q15.md"
	for _, tc := range []struct {
		name    string
		written string
		sizes   map[string]int64
	}{
		{
			name:    "root",
			written: semanticProjectsRuntimePath,
			sizes:   map[string]int64{semanticProjectsRelativePath: semanticRootCapBytes + 1},
		},
		{
			name:    "topic",
			written: topic,
			sizes:   map[string]int64{"semantic/topics/q15.md": semanticTopicCapBytes + 1},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			loader := &semanticMemoryJobLoader{
				working:       agent.WorkingMemory{Content: conversionWorkingBefore},
				ledger:        conversionLedgerBefore,
				semanticSizes: tc.sizes,
			}
			_, err := applyConversion(t, loader, conversionBaseline(), tc.written)
			if err == nil || !strings.Contains(err.Error(), "over cap") ||
				!strings.Contains(err.Error(), tc.written) {
				t.Fatalf("ApplyResult() error = %v, want over-cap failure for %s", err, tc.written)
			}
		})
	}
}

func TestSemanticConversionApplyResultIgnoresOverCapFileNotWritten(t *testing.T) {
	t.Parallel()

	loader := &semanticMemoryJobLoader{
		working: agent.WorkingMemory{Content: conversionWorkingBefore},
		ledger:  conversionLedgerBefore,
		semanticSizes: map[string]int64{
			semanticProjectsRelativePath: semanticRootCapBytes * 4,
		},
	}
	if _, err := applyConversion(t, loader, conversionBaseline(), semanticFactsRuntimePath); err != nil {
		t.Fatalf("ApplyResult() error = %v, want pre-existing oversize ignored", err)
	}
}

func TestSemanticConversionApplyResultRequiresBaseline(t *testing.T) {
	t.Parallel()

	_, err := applyConversion(t, &semanticMemoryJobLoader{}, nil)
	if err == nil || !strings.Contains(err.Error(), "baseline") {
		t.Fatalf("ApplyResult() error = %v, want missing baseline failure", err)
	}
}

func TestSemanticConversionPolicyScopesFileTools(t *testing.T) {
	t.Parallel()

	policy := semanticConversionPolicy()
	for _, target := range []string{
		semanticFactsRuntimePath,
		semanticTopicsRuntimeDir + "/new-topic.md",
		workingMemoryRuntimePath,
		openLoopsRuntimePath,
		workingLedgerRuntimePath,
	} {
		for _, name := range []string{"read_file", "write_file"} {
			call := agent.ToolCall{
				Name:      name,
				Arguments: fmt.Sprintf(`{"path":%q,"content":"x"}`, target),
			}
			if err := policy.CheckToolCall(call); err != nil {
				t.Fatalf("CheckToolCall(%s %s) error = %v", name, target, err)
			}
		}
	}
	for _, target := range []string{
		semanticTopicsRuntimeDir + "/nested/a.md",
		semanticTopicsRuntimeDir + "x.md",
		memoryRuntimePrefix + "core/AGENT.md",
		memoryRuntimePrefix + "working/other.md",
		memoryRuntimePrefix + "history/turns/1.json",
	} {
		call := agent.ToolCall{
			Name:      "write_file",
			Arguments: fmt.Sprintf(`{"path":%q,"content":"x"}`, target),
		}
		if err := policy.CheckToolCall(call); err == nil {
			t.Fatalf("CheckToolCall(write %s) error = nil, want rejection", target)
		}
	}
}

func TestSemanticConversionOverbudgetStateRule(t *testing.T) {
	t.Parallel()

	under := &semanticMemoryJobLoader{
		working: agent.WorkingMemory{Content: strings.Repeat("x", workingMemoryBudgetBytes)},
	}
	if run, _, err := evaluateWorkingMemoryOverbudget(context.Background(), Snapshot{Loader: under}, JobState{}); err != nil ||
		run {
		t.Fatalf("at budget: run=%v err=%v, want false nil", run, err)
	}
	over := &semanticMemoryJobLoader{
		working: agent.WorkingMemory{Content: strings.Repeat("x", workingMemoryBudgetBytes+1)},
	}
	run, reason, err := evaluateWorkingMemoryOverbudget(
		context.Background(),
		Snapshot{Loader: over},
		JobState{},
	)
	if err != nil || !run || !strings.Contains(reason, "budget=8192") {
		t.Fatalf("over budget: run=%v reason=%q err=%v", run, reason, err)
	}
	if run, _, err := evaluateWorkingMemoryOverbudget(context.Background(), Snapshot{}, JobState{}); err != nil ||
		run {
		t.Fatalf("nil loader: run=%v err=%v, want false nil", run, err)
	}
}

func TestSemanticConversionBuildStashesBaseline(t *testing.T) {
	t.Parallel()

	loader := goldenSemanticLoader()
	spec, err := NewSemanticMemoryExtractionRegistration().NewJob().
		Build(context.Background(), loader)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got := spec.Baseline[workingMemoryRuntimePath]; got != loader.working.Content {
		t.Fatalf("Baseline[working] = %q", got)
	}
	if got := spec.Baseline[workingLedgerRuntimePath]; got != loader.ledger {
		t.Fatalf("Baseline[ledger] = %q", got)
	}
}

func goldenSemanticLoader() *semanticMemoryJobLoader {
	return &semanticMemoryJobLoader{
		checkpoint:      SemanticExtractionCheckpoint{LastExtractedSeq: 100},
		windowLastSeq:   116,
		windowRemaining: 3,
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
			Content:      conversionWorkingBefore,
		},
		ledger: conversionLedgerBefore,
		recent: []conversation.Message{
			conversation.UserMessage("We moved the deploy target after the staging issue."),
			conversation.AssistantMessage(conversation.Text("Noted.", "")),
		},
	}
}

// TestSemanticConversionPromptGolden pins the full rendered conversion-pass
// prompt so contract drift shows up as a reviewable testdata diff. Regenerate
// with: go test ./internal/cognition -run PromptGolden -update
func TestSemanticConversionPromptGolden(t *testing.T) {
	t.Parallel()

	spec, err := NewSemanticMemoryExtractionRegistration().NewJob().Build(
		context.Background(),
		goldenSemanticLoader(),
	)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	prompt, err := renderPrompt(semanticMemoryExtractionJobType, spec)
	if err != nil {
		t.Fatalf("renderPrompt() error = %v", err)
	}
	golden := filepath.Join("testdata", "semantic_memory_prompt.golden")
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatalf("MkdirAll(testdata) error = %v", err)
		}
		if err := os.WriteFile(golden, []byte(prompt+"\n"), 0o644); err != nil {
			t.Fatalf("WriteFile(golden) error = %v", err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("ReadFile(golden) error = %v (run with -update to create)", err)
	}
	if string(want) != prompt+"\n" {
		t.Fatalf(
			"prompt drifted from %s; rerun with -update and review the diff\n--- got ---\n%s",
			golden,
			prompt,
		)
	}
}
