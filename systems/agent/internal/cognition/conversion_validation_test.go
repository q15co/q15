package cognition

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/memory/claims"
)

type conversionValidationLoader struct {
	semanticMemoryJobLoader
	files     map[string]string
	history   string
	inventory map[string]int64
}

func (l *conversionValidationLoader) ReadFileBytes(
	_ context.Context,
	relative string,
) ([]byte, bool, error) {
	raw, ok := l.files[relative]
	return []byte(raw), ok, nil
}

func (l *conversionValidationLoader) LoadConversionLedgerHistory(context.Context) (string, error) {
	return l.history, nil
}

func (l *conversionValidationLoader) LoadSemanticFileSizes(
	context.Context,
) (map[string]int64, error) {
	if l.inventory != nil {
		return l.inventory, nil
	}
	sizes := make(map[string]int64)
	for relative, raw := range l.files {
		if strings.HasPrefix(relative, "semantic/") {
			sizes[relative] = int64(len(raw))
		}
	}
	return sizes, nil
}

const validConversionClaim = "- [c-2026-0001] The deployment target is stable.\n  {status: active, kind: fact, confidence: confirmed, src: t12, rev: 2026-10-14}\n"

const validConversionLoop = "- [l-2026-0001] Ship the release.\n  {trigger: next-session, owner: agent, src: t12}\n"

func exitRecord(outcome claims.LedgerOutcome, description, id, target, reason string) string {
	return claims.FormatLedgerEntry(claims.LedgerEntry{
		Run: "semantic_memory.extract", SourceSeq: "t12..t13", Outcome: outcome,
		Description: description, TargetID: id, TargetPath: target, Note: reason,
	})
}

func TestConversionExitEvidenceMatchesEveryOutcomeAndTarget(t *testing.T) {
	t.Parallel()
	before := "- Durable detail.\n- Ship the release.\n- Temporary chatter.\n"
	loader := &conversionValidationLoader{files: map[string]string{
		workingMemoryRelativePath: "- None\n",
		"working/LEDGER.md": exitRecord(
			claims.OutcomeConverted,
			"Durable detail.",
			"c-2026-0001",
			semanticFactsRelativePath,
			"",
		) +
			exitRecord(
				claims.OutcomeMoved,
				"Ship the release.",
				"l-2026-0001",
				"working/OPEN_LOOPS.md",
				"",
			) +
			exitRecord(
				claims.OutcomeDropped,
				"Temporary chatter.",
				"",
				"",
				"episode finished",
			),
		semanticFactsRelativePath: validConversionClaim,
		"working/OPEN_LOOPS.md":   validConversionLoop,
	}}
	removed, appended, err := checkLedgerCompleteness(
		context.Background(),
		loader,
		map[string]string{workingMemoryRuntimePath: before, workingLedgerRuntimePath: ""},
	)
	if err != nil || removed != 3 || appended != 3 {
		t.Fatalf("checkLedgerCompleteness() = %d, %d, %v; want 3, 3, nil", removed, appended, err)
	}
}

func TestConversionRejectsInvalidExitEvidence(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ name, records, claim, loop string }{
		{name: "arbitrary prose", records: "one arbitrary sentence\nanother arbitrary sentence\n"},
		{name: "duplicate exit", records: exitRecord(claims.OutcomeDropped, "A.", "", "", "done") + exitRecord(claims.OutcomeDropped, "A.", "", "", "done")},
		{name: "unrelated exit", records: exitRecord(claims.OutcomeDropped, "Unrelated.", "", "", "done")},
		{name: "missing claim", records: exitRecord(claims.OutcomeConverted, "A.", "c-2026-0001", semanticFactsRelativePath, "")},
		{name: "wrong claim ID", records: exitRecord(claims.OutcomeConverted, "A.", "c-2026-0002", semanticFactsRelativePath, ""), claim: validConversionClaim},
		{name: "missing kind", records: exitRecord(claims.OutcomeConverted, "A.", "c-2026-0001", semanticFactsRelativePath, ""), claim: strings.ReplaceAll(validConversionClaim, "kind: fact, ", "")},
		{name: "invalid src", records: exitRecord(claims.OutcomeConverted, "A.", "c-2026-0001", semanticFactsRelativePath, ""), claim: strings.ReplaceAll(validConversionClaim, "src: t12", "src: guessed")},
		{name: "missing status", records: exitRecord(claims.OutcomeConverted, "A.", "c-2026-0001", semanticFactsRelativePath, ""), claim: strings.ReplaceAll(validConversionClaim, "status: active, ", "")},
		{name: "invalid confidence", records: exitRecord(claims.OutcomeConverted, "A.", "c-2026-0001", semanticFactsRelativePath, ""), claim: strings.ReplaceAll(validConversionClaim, "confidence: confirmed", "confidence: certain")},
		{name: "invalid rev", records: exitRecord(claims.OutcomeConverted, "A.", "c-2026-0001", semanticFactsRelativePath, ""), claim: strings.ReplaceAll(validConversionClaim, "rev: 2026-10-14", "rev: 2026-02-31")},
		{name: "missing loop", records: exitRecord(claims.OutcomeMoved, "A.", "l-2026-0001", "working/OPEN_LOOPS.md", "")},
		{name: "invalid loop trigger", records: exitRecord(claims.OutcomeMoved, "A.", "l-2026-0001", "working/OPEN_LOOPS.md", ""), loop: strings.ReplaceAll(validConversionLoop, "next-session", "someday")},
		{name: "empty loop intention", records: exitRecord(claims.OutcomeMoved, "A.", "l-2026-0001", "working/OPEN_LOOPS.md", ""), loop: strings.ReplaceAll(validConversionLoop, "Ship the release.", " ")},
		{name: "empty duplicated metadata", records: exitRecord(claims.OutcomeConverted, "A.", "c-2026-0001", semanticFactsRelativePath, ""), claim: strings.ReplaceAll(validConversionClaim, "kind: fact", "kind: , kind: fact")},
		{name: "invalid loop owner", records: exitRecord(claims.OutcomeMoved, "A.", "l-2026-0001", "working/OPEN_LOOPS.md", ""), loop: strings.ReplaceAll(validConversionLoop, "owner: agent", "owner: anyone")},
		{name: "missing drop reason", records: exitRecord(claims.OutcomeDropped, "A.", "", "", "")},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			loader := &conversionValidationLoader{
				files: map[string]string{
					workingMemoryRelativePath: "",
					"working/LEDGER.md":       test.records,
					semanticFactsRelativePath: test.claim,
					"working/OPEN_LOOPS.md":   test.loop,
				},
			}
			if _, _, err := checkLedgerCompleteness(context.Background(), loader, map[string]string{workingMemoryRuntimePath: "- A.\n", workingLedgerRuntimePath: ""}); err == nil {
				t.Fatal("invalid exit evidence was accepted")
			}
		})
	}
}

func TestConversionLedgerPreservesExactHistoricalBytesAndLineBoundaries(t *testing.T) {
	t.Parallel()
	record := exitRecord(claims.OutcomeDropped, "A.", "", "", "done")
	for _, test := range []struct {
		name, before, after string
		valid               bool
	}{
		{"raw prefix preserved", " \t# Ledger\n", " \t# Ledger\n" + record, true},
		{"raw prefix changed", " \t# Ledger\n", "# Ledger\n" + record, false},
		{"missing separator", "# Ledger", "# Ledger" + record, false},
		{"insert separator", "# Ledger", "# Ledger\n" + record, true},
		{"unterminated record", "", "" + strings.TrimSuffix(record, "\n"), false},
		{"historical trailing spaces", "# Ledger \n", "# Ledger\n" + record, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			loader := &conversionValidationLoader{
				files: map[string]string{"working/LEDGER.md": test.after},
			}
			_, err := appendedWorkingLedgerEntries(
				context.Background(),
				loader,
				map[string]string{workingLedgerRuntimePath: test.before},
				"test run",
			)
			if (err == nil) != test.valid {
				t.Fatalf("valid=%v error=%v", test.valid, err)
			}
		})
	}
}

func TestConversionMultilineRemovalRequiresCompleteDescription(t *testing.T) {
	t.Parallel()
	before := "- Active item.\n  Critical follow-up detail.\n  - Nested requirement.\n"
	after := "- Active item.\n"
	for _, description := range []string{"Active item.", "Active item. Critical follow-up detail. - Nested requirement."} {
		t.Run(description, func(t *testing.T) {
			t.Parallel()
			loader := &conversionValidationLoader{
				files: map[string]string{
					workingMemoryRelativePath: after,
					"working/LEDGER.md": exitRecord(
						claims.OutcomeDropped,
						description,
						"",
						"",
						"obsolete detail",
					),
				},
			}
			removed, _, err := checkLedgerCompleteness(
				context.Background(),
				loader,
				map[string]string{workingMemoryRuntimePath: before, workingLedgerRuntimePath: ""},
			)
			valid := strings.Contains(description, "Critical")
			if (err == nil) != valid || (valid && removed != 1) {
				t.Fatalf("removed=%d error=%v", removed, err)
			}
		})
	}
}

func TestConversionClaimIdentityAndProvenance(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, history, previous, current, other string
		valid                                   bool
	}{
		{name: "new claim", current: validConversionClaim, valid: true},
		{name: "reused archived claim", history: exitRecord(claims.OutcomeRetracted, "False claim.", "c-2026-0001", "", "false"), current: validConversionClaim},
		{name: "duplicate across topic", current: validConversionClaim, other: validConversionClaim},
		{name: "lost provenance", previous: validConversionClaim, current: strings.ReplaceAll(validConversionClaim, "src: t12", "src: t13")},
		{name: "preserved provenance", previous: validConversionClaim, current: strings.ReplaceAll(validConversionClaim, "src: t12", "src: t12, t13"), valid: true},
		{name: "backwards revision", previous: validConversionClaim, current: strings.ReplaceAll(validConversionClaim, "2026-10-14", "2026-10-13")},
		{name: "missing claim metadata", current: "- [c-2026-0001] Unsupported statement.\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			loader := &conversionValidationLoader{
				files:   map[string]string{semanticFactsRelativePath: test.current},
				history: test.history,
			}
			if test.other != "" {
				loader.files["semantic/topics/other.txt"] = test.other
			}
			baseline := map[string]string{
				semanticFactsRuntimePath: test.previous,
				workingLedgerRuntimePath: "",
			}
			err := checkSemanticClaims(
				context.Background(),
				loader,
				baseline,
				[]string{semanticFactsRuntimePath},
			)
			if (err == nil) != test.valid {
				t.Fatalf("valid=%v error=%v", test.valid, err)
			}
		})
	}
}

func TestConversionCapsRejectMissingInventoryForExistingWrittenFile(t *testing.T) {
	t.Parallel()
	loader := &conversionValidationLoader{
		files: map[string]string{
			"semantic/topics/hidden.txt": strings.Repeat("x", semanticTopicCapBytes+1),
		},
		inventory: map[string]int64{},
	}
	err := checkSemanticCaps(
		context.Background(),
		loader,
		conversionRunMessages(semanticTopicsRuntimeDir+"/hidden.txt"),
	)
	if err == nil || !strings.Contains(err.Error(), "missing from the file inventory") {
		t.Fatalf("checkSemanticCaps() error=%v", err)
	}
	delete(loader.files, "semantic/topics/hidden.txt")
	if err := checkSemanticCaps(context.Background(), loader, conversionRunMessages(semanticTopicsRuntimeDir+"/hidden.txt")); err != nil {
		t.Fatalf("deleted file rejected: %v", err)
	}
}

func TestConversionTopicIndexCannotBypassClaimValidation(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, item string
		valid      bool
	}{
		{"canonical topic index", "- [deploy] Deployment details → topics/deploy.md", true},
		{"claim disguised as index", "- [c-2026-0001] Unsupported claim → topics/c-2026-0001.md", false},
		{"unstructured bullet", "- Unsupported claim → topics/deploy.md", false},
		{"missing summary", "- [deploy]  → topics/deploy.md", false},
		{"mismatched slug", "- [deploy] Deployment details → topics/other.md", false},
		{"path traversal", "- [deploy] Deployment details → topics/../deploy.md", false},
		{"multiline index", "- [deploy] Deployment details → topics/deploy.md\n  Unsupported extra claim.", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			loader := &conversionValidationLoader{files: map[string]string{
				semanticProjectsRelativePath: test.item,
			}}
			err := checkSemanticClaims(
				context.Background(),
				loader,
				emptyConversionBaseline(),
				[]string{semanticProjectsRuntimePath},
			)
			if (err == nil) != test.valid {
				t.Fatalf("valid=%v error=%v", test.valid, err)
			}
		})
	}
}

func TestConversionCanonicalLoopTriggers(t *testing.T) {
	t.Parallel()
	for _, trigger := range []string{"date:2026-10-15", `event:"release finished, notify user"`, `event:"release finished, owner: user notified"`, `event:"release {target} finished"`, `event:"release \"{target}\" finished"`, "next-session"} {
		t.Run(trigger, func(t *testing.T) {
			t.Parallel()
			if err := validateLoopItem(strings.ReplaceAll(validConversionLoop, "next-session", trigger)); err != nil {
				t.Fatalf("trigger %s: %v", trigger, err)
			}
		})
	}
}

func TestConversionApplyRejectsProseLedger(t *testing.T) {
	t.Parallel()
	loader := &conversionValidationLoader{
		files: map[string]string{
			workingMemoryRelativePath: "",
			"working/LEDGER.md":       "one arbitrary sentence\ntwo arbitrary sentences\n",
		},
	}
	_, err := NewSemanticMemoryExtractionRegistration().NewJob().
		ApplyResult(context.Background(), loader, JobOutput{Spec: Spec{Baseline: map[string]string{workingMemoryRuntimePath: "- A.\n- B.\n", workingLedgerRuntimePath: ""}}, Messages: conversionRunMessages(workingMemoryRuntimePath, workingLedgerRuntimePath)})
	if err == nil || !strings.Contains(err.Error(), "invalid appended ledger entry") {
		t.Fatalf("ApplyResult() error=%v", err)
	}
}

func TestConversionApplyAccountsForEveryWorkingBlock(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, block, description string
	}{
		{"paragraph", "Keep the deployment warning.", "Keep the deployment warning."},
		{"wrapped paragraph", "Keep the deployment warning.\nIt identifies the only safe target.", "Keep the deployment warning. It identifies the only safe target."},
		{"code block", "```text\n# Working Memory\n\n- None\n```", "```text # Working Memory - None ```"},
		{"list code block", "- ```text\n# Working Memory\n\n- None\n```", "```text # Working Memory - None ```"},
		{"comment", "<!--\n# Working Memory\n\n- None\nRetain the warning.\n-->", "<!-- # Working Memory - None Retain the warning. -->"},
		{"reopened comment", "<!--\nWarning.\n--><!--\n# Working Memory\n- None\n-->", "<!-- Warning. --><!-- # Working Memory - None -->"},
		{"unrecognized heading", "## Deployment warning", "## Deployment warning"},
		{"None-like bullet", "- (None).", "(None)."},
		{"None with detail", "- None\n  A warning remains.", "None A warning remains."},
		{"plain None", "None", "None"},
		{"marker-like prose", "-Keep this warning.", "-Keep this warning."},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			before := "# Working Memory\n\n## Temporary Context\n\n" + test.block + "\n"
			for _, recorded := range []bool{false, true} {
				t.Run(fmt.Sprint(recorded), func(t *testing.T) {
					t.Parallel()
					ledger := ""
					if recorded {
						ledger = exitRecord(
							claims.OutcomeDropped,
							test.description,
							"",
							"",
							"obsolete",
						)
					}
					loader := &conversionValidationLoader{files: map[string]string{
						workingMemoryRelativePath: "# Working Memory\n\n## Temporary Context\n\n- None\n",
						"working/LEDGER.md":       ledger,
					}}
					result, err := NewSemanticMemoryExtractionRegistration().NewJob().ApplyResult(
						context.Background(),
						loader,
						JobOutput{
							Spec: Spec{Baseline: map[string]string{
								workingMemoryRuntimePath: before,
								workingLedgerRuntimePath: "",
							}},
							Messages: conversionRunMessages(
								workingMemoryRuntimePath,
								workingLedgerRuntimePath,
							),
						},
					)
					if !recorded {
						if err == nil ||
							!strings.Contains(err.Error(), "removed 1 working-memory items") {
							t.Fatalf(
								"ApplyResult() error=%v; want unrecorded block removal rejected",
								err,
							)
						}
						return
					}
					if err != nil {
						t.Fatalf("ApplyResult() rejected complete block exit: %v", err)
					}
					if result.Metadata["working_items_exited"] != "1" ||
						result.Metadata["ledger_lines_appended"] != "1" {
						t.Fatalf(
							"ApplyResult() metadata=%v; want one exited block and ledger record",
							result.Metadata,
						)
					}
				})
			}
		})
	}
}

func TestConversionApplyIgnoresOnlyExactWorkingScaffold(t *testing.T) {
	t.Parallel()
	before := "# Working Memory\n\n## Current Priorities\n\n- None\n\n## Active Tasks\n\n- None\n\n## Open Threads\n\n- None\n\n## Recent Progress\n\n- None\n\n## Pending Checks\n\n- None\n\n## Temporary Context\n\n- None\n"
	loader := &conversionValidationLoader{files: map[string]string{
		workingMemoryRelativePath: "",
		"working/LEDGER.md":       "",
	}}
	result, err := NewSemanticMemoryExtractionRegistration().NewJob().ApplyResult(
		context.Background(), loader, JobOutput{
			Spec: Spec{Baseline: map[string]string{
				workingMemoryRuntimePath: before,
				workingLedgerRuntimePath: "",
			}},
			Messages: conversionRunMessages(workingMemoryRuntimePath),
		},
	)
	if err != nil || result.Metadata["working_items_exited"] != "0" {
		t.Fatalf(
			"ApplyResult() metadata=%v error=%v; want scaffold-only edit accepted",
			result.Metadata,
			err,
		)
	}
}

func TestConversionRemovedClaimsRequireLifecycleHistory(t *testing.T) {
	t.Parallel()
	for _, recorded := range []bool{false, true} {
		t.Run(fmt.Sprint(recorded), func(t *testing.T) {
			t.Parallel()
			loader := &conversionValidationLoader{
				files: map[string]string{semanticFactsRelativePath: "- None\n"},
			}
			if recorded {
				loader.files["working/LEDGER.md"] = exitRecord(
					claims.OutcomeRetracted,
					"Unsupported deployment claim.",
					"c-2026-0001",
					"",
					"source disproved",
				)
			}
			baseline := map[string]string{
				semanticFactsRuntimePath: validConversionClaim,
				workingLedgerRuntimePath: "",
			}
			err := checkSemanticClaims(
				context.Background(),
				loader,
				baseline,
				[]string{semanticFactsRuntimePath},
			)
			if (err == nil) != recorded {
				t.Fatalf("recorded=%v error=%v", recorded, err)
			}
		})
	}
}

func TestConversionLoopParserIgnoresSeedDocumentation(t *testing.T) {
	t.Parallel()
	content := "# Open Loops\n\n<!--\n- [l-YYYY-NNNN] Example intention.\n  {trigger: next-session, owner: agent, src: t<seq>}\n- trigger: date or event.\n-->\n\n```markdown\n- [l-YYYY-NNNN] Example.\n```\n\n" + validConversionLoop
	loops, err := parseOpenLoops(content)
	if err != nil || len(loops) != 1 || loops[0].ID != "l-2026-0001" {
		t.Fatalf("parseOpenLoops() = %#v, %v", loops, err)
	}
}
