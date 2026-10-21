package cognition

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/tools"
)

func saturatedConversionLoader(t *testing.T) *semanticMemoryJobLoader {
	t.Helper()
	loader := goldenSemanticLoader()
	for i := range loader.semantic.Files {
		file := &loader.semantic.Files[i]
		claim := fmt.Sprintf(
			"- [c-2026-%04d] %%s\n  {status: active, kind: fact, confidence: confirmed, src: t1, rev: 2026-10-18}",
			9001+i,
		)
		base := strings.Replace(file.Content, "- None", fmt.Sprintf(claim, ""), 1)
		file.Content = strings.Replace(
			file.Content,
			"- None",
			fmt.Sprintf(claim, strings.Repeat("x", semanticRootCapBytes-len(base))),
			1,
		)
	}
	prefix := "# Working Memory\n\n## Temporary Context\n\n- "
	loader.working.Content = prefix + strings.Repeat(
		"x",
		defaultWorkingMemoryBudgetBytes-len(prefix)-1,
	) + "\n"
	loader.recent = []conversation.Message{conversation.UserMessage("")}
	loader.recent = []conversation.Message{
		conversation.UserMessage(
			strings.Repeat("x", MaxTranscriptWindowBytes-TranscriptArtifactBytes(loader.recent)),
		),
	}
	loader.rawFiles = make(map[string]string)
	for i := 1; i <= 120; i += 2 {
		text := strings.Repeat("<東京>", 150)
		if i == 1 {
			text = strings.Repeat("<", 6000)
		}
		var topic strings.Builder
		for _, pair := range [][2]int{{i, i + 1}, {i + 1, i}} {
			fmt.Fprintf(
				&topic,
				"- [c-2026-%04d] %s contradicts c-2026-%04d.\n  {status: conflicted, kind: fact, confidence: uncertain, src: t1, rev: 2026-10-18}\n",
				pair[0],
				text,
				pair[1],
			)
		}
		loader.rawFiles[fmt.Sprintf("semantic/topics/conflict-%03d.md", i)] = topic.String()
	}
	baseline, err := conversionBaselineFiles(context.Background(), loader)
	if err != nil {
		t.Fatal(err)
	}
	groups, err := conflictGroupsFromBaseline(baseline)
	if err != nil {
		t.Fatal(err)
	}
	var review strings.Builder
	for i, group := range groups {
		reason := "Synthetic unresolved evidence with <quotes> and \\\" escapes."
		if i == 1 {
			reason = strings.Repeat("<", 10000)
		}
		record := conflictResolution{
			ids:    conflictedClaimIDs(group),
			winner: insufficientConflictEvidence,
			reason: reason,
			basis:  conflictGroupBasis(group),
		}
		review.WriteString(record.line() + "\n")
	}
	review.WriteString(strings.Repeat("<synthetic review 東京>\n", 10000))
	loader.artifacts = map[string]Artifact{VerificationReviewPath: {Content: review.String()}}
	return loader
}

func TestSemanticExtractionBoundsWholeResolutionsAndPreservesOmittedGroups(t *testing.T) {
	loader := saturatedConversionLoader(t)
	spec, err := (semanticMemoryExtractionJob{}).Build(context.Background(), loader)
	if err != nil {
		t.Fatal(err)
	}
	section, _ := findPromptSection(spec.PromptSections, "current_conflict_resolutions")
	if size := encodedTextBytes(section.Body); size > 32*1024 {
		t.Fatalf("resolution section exceeds cap: %d", size)
	}
	selected := len(spec.conflictResolutions)
	omitted := len(spec.conflictGroups) - selected
	if selected == 0 || omitted < 2 ||
		!strings.Contains(section.Body, fmt.Sprintf("%d conflict groups omitted", omitted)) ||
		!strings.Contains(section.Body, "unchanged") {
		t.Fatalf(
			"partial conflict selection was not disclosed: selected=%d omitted=%d body=%s",
			selected,
			omitted,
			section.Body,
		)
	}
	for _, record := range spec.conflictResolutions {
		if len(record.group) != 2 || !strings.Contains(section.Body, record.line()) {
			t.Fatalf("incomplete selected resolution: %s", record.key())
		}
		for _, entry := range record.group {
			if !strings.Contains(section.Body, entry.claim.Text) {
				t.Fatalf("split group %s", record.key())
			}
		}
		if record.key() == conflictGroupKey(spec.conflictGroups[0]) ||
			record.key() == conflictGroupKey(spec.conflictGroups[1]) {
			t.Fatal("indivisible oversized claims or resolution reason selected")
		}
	}
	if err := checkConflictResolutions(context.Background(), loader, spec); err != nil {
		t.Fatalf("unchanged omitted groups failed validation: %v", err)
	}
	if _, err := (semanticMemoryExtractionJob{}).ApplyResult(context.Background(), loader, JobOutput{
		Spec: spec, Messages: conversionRunMessages(), FinalText: "Conflicts remain unresolved.",
	}); err != nil {
		t.Fatalf("conversion leaving omitted groups unchanged failed: %v", err)
	}
	firstOmitted := spec.conflictGroups[0][0]
	loader.rawFiles[firstOmitted.relativePath] = strings.Replace(
		loader.rawFiles[firstOmitted.relativePath],
		firstOmitted.claim.Text,
		"Changed omitted evidence.",
		1,
	)
	if err := checkConflictResolutions(context.Background(), loader, spec); err == nil ||
		!strings.Contains(err.Error(), "unreviewed conflict group must remain unchanged") {
		t.Fatalf("omitted group mutation escaped validation: %v", err)
	}
}

func TestSemanticExtractionSaturatedPromptAndToolsFitRequestBudget(t *testing.T) {
	loader := saturatedConversionLoader(t)
	spec, err := (semanticMemoryExtractionJob{}).Build(context.Background(), loader)
	if err != nil {
		t.Fatal(err)
	}
	review, _ := findPromptSection(spec.PromptSections, "verification_review_input")
	if encodedTextBytes(review.Body) > verificationSnapshotBytes ||
		!strings.Contains(review.Body, "partial evidence") {
		t.Fatalf("unbounded verification artifact: %d bytes", encodedTextBytes(review.Body))
	}
	prompt, err := renderPrompt(semanticMemoryExtractionJobType, spec)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := agent.NewToolRegistry(
		tools.NewReadFile(nil),
		tools.NewWriteFile(nil),
		tools.NewEditFile(nil),
		tools.NewApplyPatch(nil),
		tools.NewEmbedSearch(nil),
	)
	if err != nil {
		t.Fatal(err)
	}
	request, err := json.Marshal(struct {
		Messages []conversation.Message `json:"messages"`
		Tools    []agent.ToolDefinition `json:"tools"`
	}{append([]conversation.Message{conversation.SystemMessage(prompt)}, spec.InputMessages...), registry.Definitions()})
	limit := cognitionRequestLimit(spec, prompt, 0, 0)
	if err != nil || len(request) > limit-limit/8 {
		t.Fatalf(
			"composed request exhausted replay reserve: bytes=%d limit=%d error=%v",
			len(request),
			limit,
			err,
		)
	}
	t.Logf(
		"composed request including all conversion tool definitions: %d bytes; reserve: %d",
		len(request),
		limit-len(request),
	)
	diagnostic := describeRequestBudget(
		semanticMemoryExtractionJobType,
		spec,
		prompt,
		&agent.RequestBudgetError{Bytes: limit + 1, Limit: limit},
	)
	accounting, _, _ := strings.Cut(diagnostic.Error(), ": model request")
	parts := regexp.MustCompile(`[a-z_]+_bytes=(-?[0-9]+)`).FindAllStringSubmatch(accounting, -1)
	sum := 0
	for _, part := range parts {
		value, err := strconv.Atoi(part[1])
		if err != nil {
			t.Fatal(err)
		}
		sum += value
	}
	if len(parts) != len(spec.PromptSections)+1 || sum != encodedTextBytes(prompt) {
		t.Fatalf(
			"diagnostic accounting = %d components, %d bytes; want %d components, %d bytes",
			len(parts),
			sum,
			len(spec.PromptSections)+1,
			encodedTextBytes(prompt),
		)
	}
}

type conversionRecoveryCounter struct {
	*semanticMemoryJobLoader
	begins int
}

func (l *conversionRecoveryCounter) BeginMemoryRecovery(
	context.Context,
	map[string]FileSnapshot,
	string,
) (string, error) {
	l.begins++
	return "", nil
}

func TestSemanticExtractionBuildNamesOversizedSectionBeforeRecovery(t *testing.T) {
	loader := &conversionRecoveryCounter{semanticMemoryJobLoader: saturatedConversionLoader(t)}
	loader.semantic.Files[0].Content = strings.ReplaceAll(
		loader.semantic.Files[0].Content,
		"x",
		"<",
	)
	_, err := (semanticMemoryExtractionJob{}).Build(context.Background(), loader)
	if err == nil || !strings.Contains(err.Error(), "largest_section=semantic_facts_target") ||
		!strings.Contains(err.Error(), "reserve_bytes=") {
		t.Fatalf("missing early section diagnostic: %v", err)
	}
	if loader.begins != 0 {
		t.Fatal("oversized Build opened a recovery journal")
	}
}
