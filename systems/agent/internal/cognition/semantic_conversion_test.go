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

const conversionLedgerBefore = "# Conversion Ledger\n\n2026-10-12T05:00Z run=semantic_memory.extract dropped \"old chatter\""

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
	return NewSemanticMemoryExtractionRegistration(0).NewJob().ApplyResult(
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
		ledger: conversionLedgerBefore + "\n2026-10-14T05:00Z run=semantic_memory.extract converted \"Deploy target switched\" → c-2026-0001 (semantic/topics/q15.md)\n",
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
		{name: "one append for two exits", ledger: conversionLedgerBefore + "\n2026-10-14T05:00Z run=semantic_memory.extract dropped \"Ship T8.\""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			loader := &semanticMemoryJobLoader{
				working: agent.WorkingMemory{
					Content: "# Working Memory\n\n## Active Tasks\n\n- None\n",
				},
				ledger: tc.ledger,
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
		working: agent.WorkingMemory{
			Content: strings.Repeat("x", defaultWorkingMemoryBudgetBytes),
		},
	}
	if run, _, err := evaluateWorkingMemoryOverbudget(
		context.Background(),
		Snapshot{Loader: under},
		defaultWorkingMemoryBudgetBytes,
	); err != nil || run {
		t.Fatalf("at budget: run=%v err=%v, want false nil", run, err)
	}
	over := &semanticMemoryJobLoader{
		working: agent.WorkingMemory{
			Content: strings.Repeat("x", defaultWorkingMemoryBudgetBytes+1),
		},
	}
	run, reason, err := evaluateWorkingMemoryOverbudget(
		context.Background(),
		Snapshot{Loader: over},
		defaultWorkingMemoryBudgetBytes,
	)
	if err != nil || !run || !strings.Contains(
		reason,
		fmt.Sprintf("budget=%d", defaultWorkingMemoryBudgetBytes),
	) {
		t.Fatalf("over budget: run=%v reason=%q err=%v", run, reason, err)
	}
	if run, _, err := evaluateWorkingMemoryOverbudget(
		context.Background(),
		Snapshot{},
		defaultWorkingMemoryBudgetBytes,
	); err != nil || run {
		t.Fatalf("nil loader: run=%v err=%v, want false nil", run, err)
	}
}

func TestSemanticConversionBuildStashesBaseline(t *testing.T) {
	t.Parallel()

	loader := goldenSemanticLoader()
	spec, err := NewSemanticMemoryExtractionRegistration(0).NewJob().
		Build(context.Background(), loader)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got := spec.Baseline[workingMemoryRuntimePath]; got != strings.TrimSpace(
		loader.working.Content,
	) {
		t.Fatalf("Baseline[working] = %q", got)
	}
	if got := spec.Baseline[workingLedgerRuntimePath]; got != strings.TrimSpace(loader.ledger) {
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

	spec, err := NewSemanticMemoryExtractionRegistration(0).NewJob().Build(
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

// semanticConversionOverbudgetRule returns the working_memory_overbudget state
// rule from a registration, failing the test when the registration lost it.
func semanticConversionOverbudgetRule(t *testing.T, registration JobRegistration) StateRule {
	t.Helper()
	for _, rule := range registration.Policy.State {
		if rule.ID == "working_memory_overbudget" {
			return rule
		}
	}
	t.Fatal("registration has no working_memory_overbudget state rule")
	return StateRule{}
}

func TestSemanticConversionOverbudgetRuleUsesConfiguredBudget(t *testing.T) {
	t.Parallel()

	evaluate := semanticConversionOverbudgetRule(
		t,
		NewSemanticMemoryExtractionRegistration(128),
	).Evaluate

	atBudget := &semanticMemoryJobLoader{
		working: agent.WorkingMemory{Content: strings.Repeat("x", 128)},
	}
	if run, _, err := evaluate(context.Background(), Snapshot{Loader: atBudget}, JobState{}); err != nil ||
		run {
		t.Fatalf("at configured budget: run=%v err=%v, want false nil", run, err)
	}

	oneOver := &semanticMemoryJobLoader{
		working: agent.WorkingMemory{Content: strings.Repeat("x", 129)},
	}
	run, reason, err := evaluate(context.Background(), Snapshot{Loader: oneOver}, JobState{})
	if err != nil || !run {
		t.Fatalf("one byte over configured budget: run=%v err=%v, want true nil", run, err)
	}
	if !strings.Contains(reason, "budget=128") {
		t.Fatalf("reason %q does not name the configured budget", reason)
	}
	if strings.Contains(reason, fmt.Sprintf("budget=%d", defaultWorkingMemoryBudgetBytes)) {
		t.Fatalf("reason %q names the built-in default, not the configured budget", reason)
	}
}

func TestSemanticConversionOverbudgetRuleDefaultsBudgetWhenUnconfigured(t *testing.T) {
	t.Parallel()

	// A zero-value construction must keep today's threshold. Content one byte
	// over the built-in default fires and names 8192; anything at or under it
	// does not fire even though the configured value was zero.
	evaluate := semanticConversionOverbudgetRule(
		t,
		NewSemanticMemoryExtractionRegistration(0),
	).Evaluate

	atDefault := &semanticMemoryJobLoader{
		working: agent.WorkingMemory{
			Content: strings.Repeat("x", defaultWorkingMemoryBudgetBytes),
		},
	}
	if run, _, err := evaluate(context.Background(), Snapshot{Loader: atDefault}, JobState{}); err != nil ||
		run {
		t.Fatalf("at built-in default: run=%v err=%v, want false nil", run, err)
	}

	oneOver := &semanticMemoryJobLoader{
		working: agent.WorkingMemory{
			Content: strings.Repeat("x", defaultWorkingMemoryBudgetBytes+1),
		},
	}
	run, reason, err := evaluate(context.Background(), Snapshot{Loader: oneOver}, JobState{})
	if err != nil || !run {
		t.Fatalf("one over built-in default: run=%v err=%v, want true nil", run, err)
	}
	if !strings.Contains(reason, fmt.Sprintf("budget=%d", defaultWorkingMemoryBudgetBytes)) {
		t.Fatalf("reason %q does not name the built-in default", reason)
	}
}

// conversionPreRunFiles returns the pre-run on-disk bytes of every fixed
// writable path the conversion pass snapshots, so a rollback test can snapshot
// from it and then simulate the run mutating the same map.
func conversionPreRunFiles() map[string][]byte {
	return map[string][]byte{
		workingMemoryRelativePath:       []byte(conversionWorkingBefore),
		"working/LEDGER.md":             []byte(conversionLedgerBefore),
		"working/OPEN_LOOPS.md":         []byte("# Open Loops\n\n- None\n"),
		semanticFactsRelativePath:       []byte("# Semantic Facts\n"),
		semanticPreferencesRelativePath: []byte("# Semantic Preferences\n"),
		semanticProjectsRelativePath:    []byte("# Semantic Projects\n"),
	}
}

// conversionSnapshots captures the pre-run snapshot from a loader whose files
// map is already populated.
func conversionSnapshots(
	t *testing.T,
	loader *semanticMemoryJobLoader,
) map[string]FileSnapshot {
	t.Helper()
	snapshots, err := snapshotConversionFiles(context.Background(), loader)
	if err != nil {
		t.Fatalf("snapshotConversionFiles() error = %v", err)
	}
	return snapshots
}

func TestSemanticConversionRollsBackOnLedgerCompletenessFailure(t *testing.T) {
	t.Parallel()

	loader := &semanticMemoryJobLoader{
		files: conversionPreRunFiles(),
		// Post-run state the checks read: two items removed, no ledger append.
		working: agent.WorkingMemory{
			Content: "# Working Memory\n\n## Active Tasks\n\n- None\n",
		},
		ledger: conversionLedgerBefore,
	}
	snapshots := conversionSnapshots(t, loader)

	// Simulate the run's on-disk writes: working memory shrunk on disk.
	loader.files[workingMemoryRelativePath] = []byte(
		"# Working Memory\n\n## Active Tasks\n\n- None\n",
	)

	_, err := NewSemanticMemoryExtractionRegistration(0).NewJob().ApplyResult(
		context.Background(),
		loader,
		JobOutput{
			Type:      semanticMemoryExtractionJobType,
			Spec:      Spec{Baseline: conversionBaseline(), FileSnapshots: snapshots},
			FinalText: "Converted working memory.",
			Messages:  conversionRunMessages(workingMemoryRuntimePath),
		},
	)
	if err == nil || !strings.Contains(err.Error(), "removed 2 working-memory items") {
		t.Fatalf("ApplyResult() error = %v, want ledger-completeness failure", err)
	}
	if got := string(loader.files[workingMemoryRelativePath]); got != conversionWorkingBefore {
		t.Fatalf("working memory not restored byte-for-byte:\n%s", got)
	}
	if got := string(loader.files["working/LEDGER.md"]); got != conversionLedgerBefore {
		t.Fatalf("ledger not restored byte-for-byte:\n%s", got)
	}
}

func TestSemanticConversionRollsBackOnOverCapFailure(t *testing.T) {
	t.Parallel()

	loader := &semanticMemoryJobLoader{
		files:   conversionPreRunFiles(),
		working: agent.WorkingMemory{Content: conversionWorkingBefore},
		ledger:  conversionLedgerBefore,
		semanticSizes: map[string]int64{
			semanticProjectsRelativePath: semanticRootCapBytes + 1,
		},
	}
	snapshots := conversionSnapshots(t, loader)

	// Simulate the run blowing the projects root past its cap on disk.
	loader.files[semanticProjectsRelativePath] = []byte(strings.Repeat("x", semanticRootCapBytes+1))

	_, err := NewSemanticMemoryExtractionRegistration(0).NewJob().ApplyResult(
		context.Background(),
		loader,
		JobOutput{
			Type:      semanticMemoryExtractionJobType,
			Spec:      Spec{Baseline: conversionBaseline(), FileSnapshots: snapshots},
			FinalText: "Updated semantic memory.",
			Messages:  conversionRunMessages(semanticProjectsRuntimePath),
		},
	)
	if err == nil || !strings.Contains(err.Error(), "over cap") {
		t.Fatalf("ApplyResult() error = %v, want over-cap failure", err)
	}
	if got := string(loader.files[semanticProjectsRelativePath]); got != "# Semantic Projects\n" {
		t.Fatalf("projects root not restored byte-for-byte:\n%q", got)
	}
}

func TestSemanticConversionRollsBackNewTopicFile(t *testing.T) {
	t.Parallel()

	loader := &semanticMemoryJobLoader{
		files: conversionPreRunFiles(),
		working: agent.WorkingMemory{
			Content: "# Working Memory\n\n## Active Tasks\n\n- None\n",
		},
		ledger: conversionLedgerBefore,
	}
	snapshots := conversionSnapshots(t, loader)

	// Simulate the run creating a new topic file while also removing working
	// memory items without a matching ledger append.
	newTopic := "semantic/topics/q15.md"
	loader.files[workingMemoryRelativePath] = []byte(
		"# Working Memory\n\n## Active Tasks\n\n- None\n",
	)
	loader.files[newTopic] = []byte("- [c-2026-0001] new claim\n")
	loader.semanticSizes = map[string]int64{newTopic: 40}

	_, err := NewSemanticMemoryExtractionRegistration(0).NewJob().ApplyResult(
		context.Background(),
		loader,
		JobOutput{
			Type:      semanticMemoryExtractionJobType,
			Spec:      Spec{Baseline: conversionBaseline(), FileSnapshots: snapshots},
			FinalText: "Converted working memory.",
			Messages:  conversionRunMessages(workingMemoryRuntimePath),
		},
	)
	if err == nil || !strings.Contains(err.Error(), "removed 2 working-memory items") {
		t.Fatalf("ApplyResult() error = %v, want ledger-completeness failure", err)
	}
	if _, ok := loader.files[newTopic]; ok {
		t.Fatalf("run-created topic file %s was not deleted on rollback", newTopic)
	}
	if got := string(loader.files[workingMemoryRelativePath]); got != conversionWorkingBefore {
		t.Fatalf("working memory not restored byte-for-byte:\n%s", got)
	}
}

func TestSemanticConversionHappyPathKeepsWrites(t *testing.T) {
	t.Parallel()

	postRunWorking := "# Working Memory\n\n## Active Tasks\n\n- Ship T8.\n\n## Open Threads\n\n- None\n"
	postRunLedger := conversionLedgerBefore +
		"\n2026-10-14T05:00Z run=semantic_memory.extract converted \"Deploy target switched\" → c-2026-0001 (semantic/topics/q15.md)\n"

	loader := &semanticMemoryJobLoader{
		files:   conversionPreRunFiles(),
		working: agent.WorkingMemory{Content: postRunWorking},
		ledger:  postRunLedger,
	}
	snapshots := conversionSnapshots(t, loader)

	// Simulate the run's successful on-disk writes.
	loader.files[workingMemoryRelativePath] = []byte(postRunWorking)
	loader.files["working/LEDGER.md"] = []byte(postRunLedger)

	result, err := NewSemanticMemoryExtractionRegistration(0).NewJob().ApplyResult(
		context.Background(),
		loader,
		JobOutput{
			Type:      semanticMemoryExtractionJobType,
			Spec:      Spec{Baseline: conversionBaseline(), FileSnapshots: snapshots},
			FinalText: "Converted working memory.",
			Messages:  conversionRunMessages(workingMemoryRuntimePath, workingLedgerRuntimePath),
		},
	)
	if err != nil {
		t.Fatalf("ApplyResult() error = %v, want success", err)
	}
	if got := result.Metadata["working_items_exited"]; got != "1" {
		t.Fatalf("working_items_exited = %q, want 1", got)
	}
	// The successful run must keep its writes, not roll them back.
	if got := string(loader.files[workingMemoryRelativePath]); got != postRunWorking {
		t.Fatalf("working memory was rolled back on success:\n%s", got)
	}
	if got := string(loader.files["working/LEDGER.md"]); got != postRunLedger {
		t.Fatalf("ledger was rolled back on success:\n%s", got)
	}
	if len(loader.deleted) != 0 {
		t.Fatalf("successful run deleted files: %v", loader.deleted)
	}
}

func TestSemanticConversionRestoreFailureNamesPathAndKeepsValidation(t *testing.T) {
	t.Parallel()

	loader := &semanticMemoryJobLoader{
		files: conversionPreRunFiles(),
		working: agent.WorkingMemory{
			Content: "# Working Memory\n\n## Active Tasks\n\n- None\n",
		},
		ledger:        conversionLedgerBefore,
		failWritePath: workingMemoryRelativePath,
	}
	snapshots := conversionSnapshots(t, loader)
	loader.files[workingMemoryRelativePath] = []byte(
		"# Working Memory\n\n## Active Tasks\n\n- None\n",
	)

	_, err := NewSemanticMemoryExtractionRegistration(0).NewJob().ApplyResult(
		context.Background(),
		loader,
		JobOutput{
			Type:      semanticMemoryExtractionJobType,
			Spec:      Spec{Baseline: conversionBaseline(), FileSnapshots: snapshots},
			FinalText: "Converted working memory.",
			Messages:  conversionRunMessages(workingMemoryRuntimePath),
		},
	)
	if err == nil {
		t.Fatal("ApplyResult() error = nil, want validation failure")
	}
	if !strings.Contains(err.Error(), "removed 2 working-memory items") {
		t.Fatalf("ApplyResult() error lost the validation failure: %v", err)
	}
	if !strings.Contains(err.Error(), workingMemoryRuntimePath) {
		t.Fatalf("ApplyResult() error did not name the un-restorable path: %v", err)
	}
}

func TestSemanticConversionSnapshotCoversPolicyWritableSet(t *testing.T) {
	t.Parallel()

	topic := "semantic/topics/q15.md"
	loader := &semanticMemoryJobLoader{
		files: conversionPreRunFiles(),
		semanticSizes: map[string]int64{
			semanticFactsRelativePath:       5,
			semanticPreferencesRelativePath: 5,
			semanticProjectsRelativePath:    5,
			topic:                           40,
		},
	}
	loader.files[topic] = []byte("- [c-2026-0001] claim\n")

	snapshots := conversionSnapshots(t, loader)

	policy := semanticConversionPolicy()
	for _, runtimePath := range policy.WritePaths {
		if _, ok := snapshots[runtimePath]; !ok {
			t.Fatalf("snapshot missing policy write path %s", runtimePath)
		}
	}
	if _, ok := snapshots[semanticTopicsRuntimeDir+"/q15.md"]; !ok {
		t.Fatal("snapshot missing topic file under the policy write dir")
	}
}

// TestSemanticCapBytesMatchesTheConversionRule pins the exported cap lookup
// against the same rule the conversion pass enforces after a run, so a report
// built on it cannot drift from what a run is judged by.
func TestSemanticCapBytesMatchesTheConversionRule(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		relativePath string
		want         int
	}{
		{semanticFactsRelativePath, semanticRootCapBytes},
		{semanticPreferencesRelativePath, semanticRootCapBytes},
		{semanticProjectsRelativePath, semanticRootCapBytes},
		{"semantic/topics/q15.md", semanticTopicCapBytes},
		{"cognition/state/semantic_extraction_checkpoint.json", 0},
		{"semantic/topics", 0},
	} {
		if got := SemanticCapBytes(tc.relativePath); got != tc.want {
			t.Fatalf(
				"SemanticCapBytes(%q) = %d, want %d",
				tc.relativePath,
				got,
				tc.want,
			)
		}
	}
}
