package cognition

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/memory/claims"
)

const (
	conflictFirstID   = "c-2026-0001"
	conflictSecondID  = "c-2026-0002"
	conflictTopicPath = "semantic/topics/deployment.md"
	conflictReason    = "The newer direct statement identifies production."
)

func conflictFixture() (agent.SemanticMemory, map[string]string) {
	return agent.SemanticMemory{Files: []agent.SemanticMemoryFile{{
			RelativePath: semanticFactsRelativePath,
			Content: "# Facts\n\n- [c-2026-0001] Deploy to production; conflicts with c-2026-0002.\n" +
				"  {status: conflicted, kind: fact, confidence: confirmed, src: t11, rev: 2026-10-14}\n",
		}}}, map[string]string{
			conflictTopicPath: "# Deployment\n\n- [c-2026-0002] Deploy to staging; conflicts with c-2026-0001.\n" +
				"  {status: conflicted, kind: fact, confidence: likely, src: t8, rev: 2026-10-13}\n",
		}
}

func conflictDecision(winner string) string {
	return fmt.Sprintf(
		"resolution claims=%s,%s winner=%s reason=%q",
		conflictFirstID,
		conflictSecondID,
		winner,
		conflictReason,
	)
}

func buildConflictReview(t *testing.T) (*verificationJobLoader, Spec) {
	t.Helper()
	semantic, topics := conflictFixture()
	loader := &verificationJobLoader{semantic: semantic, topics: topics}
	spec, err := (verificationReviewJob{}).Build(context.Background(), loader)
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.conflictGroups) != 1 || len(spec.conflictGroups[0]) != 2 {
		t.Fatalf("conflict groups = %#v, want the root/topic pair", spec.conflictGroups)
	}
	return loader, spec
}

func TestVerificationReviewRejectsIncompleteOrInvalidConflictResolutionsBeforeStorage(
	t *testing.T,
) {
	t.Parallel()

	valid := conflictDecision(conflictFirstID)
	cases := map[string]string{
		"missing":          "# Review\n\nThe first claim wins.",
		"duplicate group":  valid + "\n" + valid,
		"unknown group":    strings.Replace(valid, conflictSecondID, "c-2026-0099", 1),
		"duplicate member": strings.Replace(valid, conflictSecondID, conflictFirstID, 1),
		"nonmember winner": conflictDecision("c-2026-0099"),
		"empty reason":     strings.Replace(valid, fmt.Sprintf("%q", conflictReason), `" "`, 1),
		"multiline reason": strings.Replace(
			valid,
			fmt.Sprintf("%q", conflictReason),
			`"line one\nline two"`,
			1,
		),
		"malformed":            "resolution claims=wrong",
		"model supplied basis": valid + " basis=" + strings.Repeat("a", 64),
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			loader, spec := buildConflictReview(t)
			_, err := (verificationReviewJob{}).ApplyResult(context.Background(), loader, JobOutput{
				Spec: spec, FinalText: content,
			})
			if err == nil {
				t.Fatal("ApplyResult accepted an invalid or missing conflict decision")
			}
			if len(loader.storeCalls) != 0 {
				t.Fatalf("invalid review persisted %d artifacts", len(loader.storeCalls))
			}
		})
	}
}

func TestVerificationReviewRequiresEveryConflictGroupIncludingSingletons(t *testing.T) {
	t.Parallel()

	loader, _ := buildConflictReview(t)
	loader.semantic.Files[0].Content += "\n- [c-2026-0003] A separate unresolved claim.\n" +
		"  {status: conflicted, kind: fact, confidence: uncertain, src: t15, rev: 2026-10-14}\n"
	spec, err := (verificationReviewJob{}).Build(context.Background(), loader)
	if err != nil {
		t.Fatal(err)
	}
	_, err = (verificationReviewJob{}).ApplyResult(context.Background(), loader, JobOutput{
		Spec: spec, FinalText: conflictDecision(conflictFirstID),
	})
	if err == nil || len(loader.storeCalls) != 0 {
		t.Fatalf("missing singleton decision: error=%v stored=%d", err, len(loader.storeCalls))
	}
	_, err = (verificationReviewJob{}).ApplyResult(context.Background(), loader, JobOutput{
		Spec: spec, FinalText: conflictDecision(conflictFirstID) + "\n" +
			`resolution claims=c-2026-0003 winner=insufficient reason="No corroborating evidence."`,
	})
	if err != nil || len(loader.storeCalls) != 1 {
		t.Fatalf("complete group decisions: error=%v stored=%d", err, len(loader.storeCalls))
	}
}

func TestConflictGroupsMatchWholeClaimIDs(t *testing.T) {
	t.Parallel()

	entries := []conflictedClaimEntry{
		{claim: claims.Claim{ID: "c-2026-0001", Text: "Contradicts c-2026-00020."}},
		{claim: claims.Claim{ID: "c-2026-0002", Text: "A separate conflict."}},
		{claim: claims.Claim{ID: "c-2026-00020", Text: "Contradicts c-2026-0001."}},
	}
	groups := groupConflictedClaims(entries)
	if len(groups) != 2 || conflictGroupKey(groups[0]) != "c-2026-0001,c-2026-00020" ||
		conflictGroupKey(groups[1]) != "c-2026-0002" {
		t.Fatalf("groups = %#v, want exact cross-references without prefix collisions", groups)
	}
}

func TestVerificationReviewIncludesConflictedClaimsInNonMarkdownTopics(t *testing.T) {
	t.Parallel()

	loader, _ := buildConflictReview(t)
	loader.topics["semantic/topics/deployment.txt"] = loader.topics[conflictTopicPath]
	delete(loader.topics, conflictTopicPath)
	spec, err := (verificationReviewJob{}).Build(context.Background(), loader)
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.conflictGroups) != 1 || len(spec.conflictGroups[0]) != 2 ||
		spec.conflictGroups[0][1].relativePath != "semantic/topics/deployment.txt" {
		t.Fatalf("non-markdown topic escaped the conflict inventory: %#v", spec.conflictGroups)
	}
	if _, err := (verificationReviewJob{}).ApplyResult(context.Background(), loader, JobOutput{
		Spec: spec, FinalText: "# Review\nNo conflicts found.",
	}); err == nil || len(loader.storeCalls) != 0 {
		t.Fatalf(
			"non-markdown conflict accepted without a decision: error=%v stored=%d",
			err,
			len(loader.storeCalls),
		)
	}
}

func TestVerificationReviewBindsDecisionsToItsInputBeforeConversionSelectsThem(t *testing.T) {
	t.Parallel()

	loader, spec := buildConflictReview(t)
	_, err := (verificationReviewJob{}).ApplyResult(context.Background(), loader, JobOutput{
		Spec: spec, FinalText: "# Review\n\n" + conflictDecision(conflictFirstID),
	})
	if err != nil {
		t.Fatal(err)
	}
	artifact := loader.storeCalls[0].Content
	if !strings.Contains(artifact, " basis=") || !strings.HasPrefix(artifact, "# Review\n\n") {
		t.Fatalf("review was not bound while preserving its prose: %q", artifact)
	}
	records, err := currentConflictResolutions(artifact, spec.conflictGroups)
	if err != nil || len(records) != 1 || records[0].winner != conflictFirstID ||
		len(records[0].basis) != 64 {
		t.Fatalf("current decisions = %#v, error=%v", records, err)
	}
	cases := map[string]func(*conflictedClaimEntry){
		"path":       func(entry *conflictedClaimEntry) { entry.relativePath = "semantic/projects.md" },
		"text":       func(entry *conflictedClaimEntry) { entry.claim.Text += " New evidence." },
		"status":     func(entry *conflictedClaimEntry) { entry.claim.Status = claims.StatusActive },
		"kind":       func(entry *conflictedClaimEntry) { entry.claim.Kind = claims.KindInference },
		"confidence": func(entry *conflictedClaimEntry) { entry.claim.Confidence = claims.ConfidenceUncertain },
		"provenance": func(entry *conflictedClaimEntry) { entry.claim.Src = []string{"t11", "t19"} },
		"revision":   func(entry *conflictedClaimEntry) { entry.claim.Rev = "2026-10-15" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			changed := append([]conflictedClaimEntry(nil), spec.conflictGroups[0]...)
			mutate(&changed[0])
			selected, err := currentConflictResolutions(artifact, [][]conflictedClaimEntry{changed})
			if err != nil || len(selected) != 0 {
				t.Fatalf("stale %s decision selected: %#v, error=%v", name, selected, err)
			}
		})
	}
	if selected, err := currentConflictResolutions(conflictDecision(conflictFirstID), spec.conflictGroups); err != nil ||
		len(selected) != 0 {
		t.Fatalf("unbound decision selected: %#v, error=%v", selected, err)
	}
}

func buildConflictConversion(t *testing.T, winner string) (*semanticMemoryJobLoader, Spec) {
	t.Helper()
	reviewLoader, reviewSpec := buildConflictReview(t)
	_, err := (verificationReviewJob{}).ApplyResult(context.Background(), reviewLoader, JobOutput{
		Spec: reviewSpec, FinalText: conflictDecision(winner),
	})
	if err != nil {
		t.Fatal(err)
	}
	semantic, topics := conflictFixture()
	loader := goldenSemanticLoader()
	loader.semantic.Files[0] = semantic.Files[0]
	loader.rawFiles = topics
	loader.ledger = ""
	loader.working.Content = ""
	loader.artifacts = map[string]Artifact{VerificationReviewPath: reviewLoader.storeCalls[0]}
	spec, err := (semanticMemoryExtractionJob{}).Build(context.Background(), loader)
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.conflictResolutions) != 1 || len(spec.conflictGroups) != 1 {
		t.Fatalf("conversion did not select the current review: %#v", spec.conflictResolutions)
	}
	return loader, spec
}

func resolvedConflictLedger() string {
	return claims.FormatLedgerEntry(claims.LedgerEntry{
		Timestamp: time.Date(2026, 10, 15, 5, 0, 0, 0, time.UTC),
		Run:       semanticMemoryExtractionJobType, SourceSeq: "t8..t11",
		Outcome: claims.OutcomeResolved, Description: "Deploy to staging.", TargetID: conflictSecondID,
		Note: "winner=" + conflictFirstID + "; " + conflictReason,
	})
}

func resolveConflictFixture(loader *semanticMemoryJobLoader) {
	loader.semantic.Files[0].Content = strings.Replace(
		loader.semantic.Files[0].Content,
		"status: conflicted",
		"status: active",
		1,
	)
	loader.rawFiles[conflictTopicPath] = "# Deployment\n"
	loader.ledger = resolvedConflictLedger()
}

func applyConflictConversion(loader *semanticMemoryJobLoader, spec Spec) error {
	_, err := (semanticMemoryExtractionJob{}).ApplyResult(context.Background(), loader, JobOutput{
		Spec: spec, FinalText: "Applied the reviewed conflict resolution.",
		Messages: conversionRunMessages(
			semanticFactsRuntimePath,
			memoryRuntimePrefix+conflictTopicPath,
			workingLedgerRuntimePath,
		),
	})
	return err
}

func TestConversionAppliesBoundConflictWinnerAndRecordsEachLoser(t *testing.T) {
	t.Parallel()

	loader, spec := buildConflictConversion(t, conflictFirstID)
	resolveConflictFixture(loader)
	if err := applyConflictConversion(loader, spec); err != nil {
		t.Fatal(err)
	}
}

func TestConflictResolutionRequiresASeparateRecordForEveryLosingClaim(t *testing.T) {
	t.Parallel()

	record := conflictResolution{
		ids:    []string{conflictFirstID, conflictSecondID, "c-2026-0003"},
		winner: conflictFirstID, reason: conflictReason,
	}
	current := map[string]conflictedClaimEntry{
		conflictFirstID: {claim: claims.Claim{ID: conflictFirstID, Status: claims.StatusActive}},
	}
	entries := []claims.LedgerEntry{{
		Outcome: claims.OutcomeResolved, TargetID: conflictSecondID,
		Note: "winner=" + conflictFirstID + "; " + conflictReason,
	}}
	if err := checkConflictResolution(record, current, entries); err == nil {
		t.Fatal("three-claim resolution accepted a missing losing-claim record")
	}
	entries = append(entries, claims.LedgerEntry{
		Outcome: claims.OutcomeResolved, TargetID: "c-2026-0003",
		Note: "winner=" + conflictFirstID + "; " + conflictReason,
	})
	if err := checkConflictResolution(record, current, entries); err != nil {
		t.Fatalf("three-claim resolution with both losing-claim records: %v", err)
	}
}

func TestConversionRejectsIncompleteConflictApplication(t *testing.T) {
	t.Parallel()

	cases := map[string]func(*semanticMemoryJobLoader){
		"winner still conflicted": func(loader *semanticMemoryJobLoader) {
			loader.semantic.Files[0].Content = strings.Replace(
				loader.semantic.Files[0].Content,
				"status: active",
				"status: conflicted",
				1,
			)
		},
		"loser still present": func(loader *semanticMemoryJobLoader) {
			_, topics := conflictFixture()
			loader.rawFiles[conflictTopicPath] = topics[conflictTopicPath]
		},
		"missing ledger":   func(loader *semanticMemoryJobLoader) { loader.ledger = "" },
		"duplicate ledger": func(loader *semanticMemoryJobLoader) { loader.ledger += loader.ledger },
		"contradictory losing lifecycle": func(loader *semanticMemoryJobLoader) {
			loader.ledger += strings.Replace(loader.ledger, " resolved ", " retracted ", 1)
		},
		"false winning lifecycle": func(loader *semanticMemoryJobLoader) {
			loader.ledger += strings.Replace(
				loader.ledger,
				"→ "+conflictSecondID,
				"→ "+conflictFirstID,
				1,
			)
		},
		"wrong winner ledger": func(loader *semanticMemoryJobLoader) {
			loader.ledger = strings.Replace(
				loader.ledger,
				"winner="+conflictFirstID,
				"winner=c-2026-0099",
				1,
			)
		},
		"missing reason ledger": func(loader *semanticMemoryJobLoader) {
			loader.ledger = strings.Replace(
				loader.ledger,
				conflictReason,
				"",
				1,
			)
		},
		"different lifecycle outcome": func(loader *semanticMemoryJobLoader) {
			loader.ledger = strings.Replace(loader.ledger, " resolved ", " retracted ", 1)
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			loader, spec := buildConflictConversion(t, conflictFirstID)
			resolveConflictFixture(loader)
			mutate(loader)
			if err := applyConflictConversion(loader, spec); err == nil {
				t.Fatal("ApplyResult accepted an incomplete conflict resolution")
			}
		})
	}
}

func TestConversionPreservesInsufficientAndUnreviewedConflicts(t *testing.T) {
	t.Parallel()

	for _, unreviewed := range []bool{false, true} {
		t.Run(fmt.Sprintf("unreviewed=%t", unreviewed), func(t *testing.T) {
			t.Parallel()
			loader, spec := buildConflictConversion(t, insufficientConflictEvidence)
			if unreviewed {
				spec.conflictResolutions = nil
			}
			if err := applyConflictConversion(loader, spec); err != nil {
				t.Fatalf("unchanged conflicts rejected: %v", err)
			}
			loader.semantic.Files[0].Content = strings.Replace(
				loader.semantic.Files[0].Content,
				"src: t11",
				"src: t11, t19",
				1,
			)
			if err := applyConflictConversion(loader, spec); err == nil {
				t.Fatal("ApplyResult changed the provenance of an unresolved claim")
			}
		})
	}
}

func TestConversionDoesNotApplyAStaleReview(t *testing.T) {
	t.Parallel()

	loader, _ := buildConflictConversion(t, conflictFirstID)
	loader.semantic.Files[0].Content = strings.Replace(
		loader.semantic.Files[0].Content,
		"production;",
		"a new target;",
		1,
	)
	spec, err := (semanticMemoryExtractionJob{}).Build(context.Background(), loader)
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.conflictResolutions) != 0 || len(spec.conflictGroups) != 1 {
		t.Fatalf("stale review selected: %#v", spec.conflictResolutions)
	}
	resolveConflictFixture(loader)
	if err := applyConflictConversion(loader, spec); err == nil {
		t.Fatal("ApplyResult accepted a resolution based on a stale review")
	}
}
