package cognition

import (
	"context"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/memory/claims"
)

// TestDriftTolerantExitForms pins the matching rule #388 documents at
// checkWorkingMemoryLedgerCompleteness: verbatim first, then the two
// presentation-only drift forms - a quote carrying one leading bullet marker,
// and a removed item scoped to its section content without leading custom
// heading lines.
func TestDriftTolerantExitForms(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		forms map[string]bool
		want  []string
		deny  []string
	}{
		{
			name:  "quote with bullet marker",
			forms: driftedExitForms("- Retire the old dashboard."),
			want:  []string{"- Retire the old dashboard.", "Retire the old dashboard."},
		},
		{
			name:  "plain quote stays single",
			forms: driftedExitForms("Prepare the RC checklist."),
			want:  []string{"Prepare the RC checklist."},
			deny:  []string{"- Prepare the RC checklist."},
		},
		{
			name:  "custom heading framing",
			forms: driftedRemovalForms("## Release notes\nPrepare the RC checklist."),
			want:  []string{"## Release notes Prepare the RC checklist.", "Prepare the RC checklist."},
		},
		{
			name:  "section content under a bullet",
			forms: driftedRemovalForms("## Later\n- File the recap."),
			want:  []string{"## Later - File the recap.", "File the recap."},
		},
		{
			name:  "heading inside the block stays content",
			forms: driftedRemovalForms("Prepare the notes.\n## Later\nFile the recap."),
			want:  []string{"Prepare the notes. ## Later File the recap."},
			deny:  []string{"File the recap."},
		},
		{
			name:  "partial quote is no form",
			forms: driftedRemovalForms("Prepare the RC checklist.\nInclude its changelog."),
			deny:  []string{"Prepare the RC checklist."},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			for _, want := range test.want {
				if !test.forms[want] {
					t.Fatalf("forms missing %q (have %v)", want, test.forms)
				}
			}
			for _, deny := range test.deny {
				if test.forms[deny] {
					t.Fatalf("forms unexpectedly accept %q", deny)
				}
			}
		})
	}
}

// TestWorkingMemoryConsolidationDefersDriftedLedgerExits covers the
// drift-tolerated class: the exit quotes the full removed content but not its
// exact markdown framing. The run applies, the removal is consumed, and the
// deferral is named on the run result instead of failing the lane (#388).
func TestWorkingMemoryConsolidationDefersDriftedLedgerExits(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name          string
		workingBefore string
		workingAfter  string
		loopsBefore   string
		loopsAfter    string
		exit          claims.LedgerOutcome
		description   string
		toolPath      string
	}{
		{
			name: "quote omits the custom section heading",
			workingBefore: "# Working Memory\n\n## Release notes\nPrepare the RC checklist.\n" +
				"- Keep the rollout green.\n",
			workingAfter: "# Working Memory\n\n- Keep the rollout green.\n",
			loopsBefore:  "# Open Loops\n",
			loopsAfter:   "# Open Loops\n",
			exit:         claims.OutcomeDropped,
			description:  "Prepare the RC checklist.",
			toolPath:     workingMemoryRuntimePath,
		},
		{
			name: "quote carries the bullet marker",
			workingBefore: "# Working Memory\n\n## Active Tasks\n\n" +
				"- Retire the old dashboard.\n- Keep the rollout green.\n",
			workingAfter: "# Working Memory\n\n## Active Tasks\n\n- Keep the rollout green.\n",
			loopsBefore:  "# Open Loops\n",
			loopsAfter:   "# Open Loops\n",
			exit:         claims.OutcomeDropped,
			description:  "- Retire the old dashboard.",
			toolPath:     workingMemoryRuntimePath,
		},
		{
			name:          "closed loop quote carries the bullet marker",
			workingBefore: "# Working Memory\n",
			workingAfter:  "# Working Memory\n",
			loopsBefore: "# Open Loops\n\n" +
				"- [l-2026-0007] Verify the rollout.\n" +
				"  {trigger: next-session, owner: agent, src: t42}\n",
			loopsAfter:  "# Open Loops\n",
			exit:        claims.OutcomeClosed,
			description: "- [l-2026-0007] Verify the rollout. {trigger: next-session, owner: agent, src: t42}",
			toolPath:    openLoopsRuntimePath,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ledgerBefore := "# Ledger\n"
			loader := &workingMemoryJobLoader{files: map[string][]byte{
				workingMemoryRelativePath: []byte(test.workingBefore),
				"working/OPEN_LOOPS.md":   []byte(test.loopsBefore),
				"working/LEDGER.md":       []byte(ledgerBefore),
			}}
			spec := buildWorkingMemorySpec(t, loader)
			loader.files[workingMemoryRelativePath] = []byte(test.workingAfter)
			loader.files["working/OPEN_LOOPS.md"] = []byte(test.loopsAfter)
			loader.files["working/LEDGER.md"] = []byte(
				ledgerBefore + workingExitRecord(test.exit, test.description, "", "", "done"),
			)
			result, err := (workingMemoryConsolidationJob{}).ApplyResult(
				context.Background(),
				loader,
				JobOutput{
					Spec:     spec,
					Messages: workingMemoryTargetToolCall("write_file", test.toolPath),
				},
			)
			if err != nil {
				t.Fatalf("ApplyResult rejected a drift-tolerated exit: %v", err)
			}
			if got := result.Metadata["ledger_exits_deferred"]; got != "1" {
				t.Fatalf("ledger_exits_deferred = %q, want 1 (metadata %#v)", got, result.Metadata)
			}
			detail := result.Metadata["ledger_exit_deferrals"]
			for _, want := range []string{"entry[1]", string(test.exit), "only drift-tolerantly"} {
				if !strings.Contains(detail, want) {
					t.Fatalf("ledger_exit_deferrals = %q, want it to name %q", detail, want)
				}
			}
			// A tolerated run keeps its writes: nothing rolls back.
			assertFileBytesRestored(
				t,
				loader,
				workingMemoryRuntimePath,
				[]byte(test.workingAfter),
			)
			assertFileBytesRestored(t, loader, openLoopsRuntimePath, []byte(test.loopsAfter))
		})
	}
}

// TestWorkingMemoryConsolidationDefersUnmatchedLedgerExits covers exits that
// match no removal at all, even drift-tolerantly: recorded but quoting
// nothing this run removed, or a second record for a removal that already has
// one. Under #324 these failed the whole run and let an identical fingerprint
// park the lane; now they defer with a disclosure on the run result.
func TestWorkingMemoryConsolidationDefersUnmatchedLedgerExits(t *testing.T) {
	t.Parallel()

	ledgerBefore := "# Ledger\n"

	t.Run("remove nothing and record an exit", func(t *testing.T) {
		t.Parallel()
		working := "# Working Memory\n\n## Active Tasks\n\n- Keep the rollout green.\n"
		loader := &workingMemoryJobLoader{files: map[string][]byte{
			workingMemoryRelativePath: []byte(working),
			"working/OPEN_LOOPS.md":   []byte("# Open Loops\n"),
			"working/LEDGER.md":       []byte(ledgerBefore),
		}}
		spec := buildWorkingMemorySpec(t, loader)
		exit := workingExitRecord(
			claims.OutcomeDropped,
			"Replan the launch party.",
			"",
			"",
			"stale",
		)
		loader.files["working/LEDGER.md"] = []byte(ledgerBefore + exit)
		result, err := (workingMemoryConsolidationJob{}).ApplyResult(
			context.Background(),
			loader,
			JobOutput{
				Spec:     spec,
				Messages: workingMemoryTargetToolCall("read_file", workingMemoryRuntimePath),
			},
		)
		if err != nil {
			t.Fatalf("ApplyResult rejected an unmatched exit instead of deferring it: %v", err)
		}
		if got := result.Metadata["ledger_exits_deferred"]; got != "1" {
			t.Fatalf("ledger_exits_deferred = %q, want 1", got)
		}
		detail := result.Metadata["ledger_exit_deferrals"]
		for _, want := range []string{"entry[1]", "Replan the launch party.", "records nothing"} {
			if !strings.Contains(detail, want) {
				t.Fatalf("ledger_exit_deferrals = %q, want it to name %q", detail, want)
			}
		}
		assertFileBytesRestored(t, loader, workingLedgerRuntimePath, []byte(ledgerBefore+exit))
	})

	t.Run("duplicate record for one removal", func(t *testing.T) {
		t.Parallel()
		workingBefore := "# Working Memory\n\n- Finish the release.\n  Include its changelog.\n"
		loader := &workingMemoryJobLoader{files: map[string][]byte{
			workingMemoryRelativePath: []byte(workingBefore),
			"working/OPEN_LOOPS.md":   []byte("# Open Loops\n"),
			"working/LEDGER.md":       []byte(ledgerBefore),
		}}
		spec := buildWorkingMemorySpec(t, loader)
		exit := workingExitRecord(
			claims.OutcomeDropped,
			"Finish the release. Include its changelog.",
			"",
			"",
			"completed",
		)
		loader.files[workingMemoryRelativePath] = []byte("# Working Memory\n")
		loader.files["working/LEDGER.md"] = []byte(ledgerBefore + exit + exit)
		result, err := (workingMemoryConsolidationJob{}).ApplyResult(
			context.Background(),
			loader,
			JobOutput{
				Spec:     spec,
				Messages: workingMemoryTargetToolCall("write_file", workingMemoryRuntimePath),
			},
		)
		if err != nil {
			t.Fatalf("ApplyResult rejected a duplicate record instead of deferring it: %v", err)
		}
		if got := result.Metadata["ledger_exits_deferred"]; got != "1" {
			t.Fatalf("ledger_exits_deferred = %q, want 1", got)
		}
		if detail := result.Metadata["ledger_exit_deferrals"]; !strings.Contains(
			detail,
			"entry[2]",
		) {
			t.Fatalf("ledger_exit_deferrals = %q, want the duplicate entry[2] named", detail)
		}
	})
}

// TestWorkingMemoryConsolidationRejectsGenuinelyRemovedContent covers the
// protective direction #388 keeps: a removal whose words no exit records -
// verbatim or drift-tolerantly - is genuinely rewritten content and still
// fails the run, with the diagnostic bounded by the compact summary limit.
func TestWorkingMemoryConsolidationRejectsGenuinelyRemovedContent(t *testing.T) {
	t.Parallel()

	tail := "archive the appendix worksheet"
	longItem := "Review the migration checklist cover page, " +
		strings.Repeat("tick off one worksheet row, ", 12) + tail + "."
	if len(longItem) <= cognitionSummaryLimit {
		t.Fatalf("fixture item is %d bytes, want it longer than the %d-rune compact limit",
			len(longItem), cognitionSummaryLimit)
	}
	workingBefore := "# Working Memory\n\n- " + longItem + "\n"
	ledgerBefore := "# Ledger\n"
	loader := &workingMemoryJobLoader{files: map[string][]byte{
		workingMemoryRelativePath: []byte(workingBefore),
		"working/OPEN_LOOPS.md":   []byte("# Open Loops\n"),
		"working/LEDGER.md":       []byte(ledgerBefore)},
	}
	spec := buildWorkingMemorySpec(t, loader)

	// The run rewrites the item into different words and records the new
	// text, which was never removed, so no exit stands for the old item.
	loader.files[workingMemoryRelativePath] = []byte(
		"# Working Memory\n\n- Filed the migration checklist with the weekly notes.\n",
	)
	loader.files["working/LEDGER.md"] = []byte(ledgerBefore + workingExitRecord(
		claims.OutcomeDropped,
		"Filed the migration checklist with the weekly notes.",
		"",
		"",
		"done",
	))
	_, err := (workingMemoryConsolidationJob{}).ApplyResult(
		context.Background(),
		loader,
		JobOutput{
			Spec:     spec,
			Messages: workingMemoryTargetToolCall("write_file", workingMemoryRuntimePath),
		},
	)
	if err == nil {
		t.Fatal("ApplyResult accepted a genuinely rewritten removal with no faithful record")
	}
	if !strings.Contains(err.Error(), "without a matching ledger exit") {
		t.Fatalf("ApplyResult() error = %v, want the unrecorded removal named", err)
	}
	if !strings.Contains(err.Error(), "Review the migration checklist cover page") {
		t.Fatalf("ApplyResult() error = %v, want the removed item's head named", err)
	}
	if strings.Contains(err.Error(), tail) {
		t.Fatalf(
			"ApplyResult() error = %v, want the diagnostic bounded before the item's tail",
			err,
		)
	}
	assertFileBytesRestored(t, loader, workingMemoryRuntimePath, []byte(workingBefore))
	assertFileBytesRestored(t, loader, workingLedgerRuntimePath, []byte(ledgerBefore))
}
