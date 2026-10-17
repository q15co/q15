package cognition

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/memory/claims"
)

func typedRepairFixture(t *testing.T, source string) (*repairProbeLoader, Spec) {
	t.Helper()
	files := repairPreFiles()
	files[repairNotesPath] = source
	loader := &repairProbeLoader{
		semanticMemoryJobLoader: &semanticMemoryJobLoader{files: repairRawFiles(files)},
	}
	for path, content := range files {
		loader.memoryFiles = append(
			loader.memoryFiles,
			MemoryFile{Path: path, Bytes: int64(len(content))},
		)
	}
	spec, err := NewSemanticMemoryExtractionRegistration(
		0,
		0,
	).NewJob().
		Build(context.Background(), loader)
	if err != nil {
		t.Fatal(err)
	}
	if !spec.repair || spec.repairTarget.Path != repairNotesPath {
		t.Fatalf(
			"unexpected mode or target: repair=%v target=%s",
			spec.repair,
			spec.repairTarget.Path,
		)
	}
	return loader, spec
}

func TestTypedRepairAppliesDecisionsWithoutModelFileWrites(t *testing.T) {
	loader, spec := typedRepairFixture(t, repairNotesPre)
	response := fmt.Sprintf(
		`{"decisions":[{"offset":%d,"outcome":"converted","claim_id":"c-2026-0007","claim":{"path":"semantic/topics/synthetic.md","text":"The synthetic project shipped in May.","status":"active","kind":"fact","confidence":"confirmed"}},{"offset":%d,"outcome":"dropped","reason":"Synthetic temporary task completed."}]}`,
		repairAlphaOffset(),
		repairScratchOffset(),
	)
	result, err := NewSemanticMemoryExtractionRegistration(
		0,
		0,
	).NewJob().
		ApplyResult(context.Background(), loader, JobOutput{Spec: spec, FinalText: response})
	if err != nil {
		t.Fatal(err)
	}
	if result.Metadata["chunk_lines_exited"] != "2" || !result.Repair {
		t.Fatalf("unexpected result: %#v", result)
	}
	if _, exists := loader.files[repairNotesPath]; exists {
		t.Fatal("drained un-named target was not adopted")
	}
	ledger := string(loader.files[workingLedgerRelativePath])
	if !strings.HasPrefix(ledger, repairLedgerPre) {
		t.Fatal("runtime rewrote ledger history")
	}
	entries, err := claims.ParseLedger([]byte(ledger))
	if err != nil || len(entries) != 3 {
		t.Fatalf("ledger entries=%v error=%v, want two exits and adoption", entries, err)
	}
	if entries[0].Description != repairAlphaLine || entries[1].Description != repairScratchLine {
		t.Fatalf("ledger descriptions did not come from source: %#v", entries)
	}
	parsed, _, err := claims.ParseClaims(string(loader.files["semantic/topics/synthetic.md"]))
	if err != nil || len(parsed) != 1 || parsed[0].ID != "c-2026-0007" ||
		len(
			parsed[0].Src,
		) != 1 || parsed[0].Src[0] != fmt.Sprintf("mem:%s:%d", repairNotesPath, repairAlphaOffset()) {
		t.Fatalf("persisted claim=%#v error=%v", parsed, err)
	}
}

func TestTypedRepairCopiesSourceBytesAndPreservesUncoveredTail(t *testing.T) {
	const line = "> **Synthetic** — inspect `sample.go`  after \"review\"."
	tail := strings.Repeat("uncovered duplicate source ", 400) + "\n" + line
	loader, spec := typedRepairFixture(t, line+"\r\n"+tail)
	result, err := NewSemanticMemoryExtractionRegistration(
		0,
		0,
	).NewJob().
		ApplyResult(context.Background(), loader, JobOutput{
			Spec: spec, FinalText: `{"decisions":[{"offset":0,"outcome":"dropped","reason":"Synthetic completed task."}]}`,
		})
	if err != nil {
		t.Fatal(err)
	}
	if result.Metadata["chunk_lines_exited"] != "1" ||
		string(loader.files[repairNotesPath]) != tail {
		t.Fatal("runtime did not remove exactly the supplied occurrence and its CRLF")
	}
	entries, err := claims.ParseLedger(loader.files[workingLedgerRelativePath])
	if err != nil || len(entries) != 1 ||
		entries[0].Description != "> **Synthetic** — inspect `sample.go` after \"review\"." {
		t.Fatalf("unexpected ledger=%#v error=%v", entries, err)
	}
}

func TestTypedRepairRejectsInvalidPlansBeforeWriting(t *testing.T) {
	for _, response := range []string{
		`{"decisions":[]}`,
		`{"decisions":[{"outcome":"dropped","reason":"done"}]}`,
		`{"decisions":[{"offset":999,"outcome":"dropped","reason":"done"}]}`,
		`{"decisions":[{"offset":0,"outcome":"dropped","reason":")"}]}`,
		`{"decisions":[{"offset":0,"outcome":"dropped","reason":"done","description":"invented"}]}`,
		`{"decisions":[{"offset":0,"outcome":"converted","claim_id":"c-2026-0007","claim":{"path":"../elsewhere.md","text":"Synthetic.","kind":"fact","confidence":"confirmed","status":"active"}}]}`,
	} {
		t.Run(response, func(t *testing.T) {
			loader, spec := typedRepairFixture(t, "Synthetic source.\n")
			before := make(map[string]string)
			for path, raw := range loader.files {
				before[path] = string(raw)
			}
			_, err := NewSemanticMemoryExtractionRegistration(
				0,
				0,
			).NewJob().
				ApplyResult(context.Background(), loader, JobOutput{Spec: spec, FinalText: response})
			if err == nil {
				t.Fatal("accepted invalid plan")
			}
			for path, raw := range loader.files {
				if before[path] != string(raw) {
					t.Fatalf("changed %s on invalid input", path)
				}
			}
			if len(loader.files) != len(before) {
				t.Fatal("invalid plan changed file inventory")
			}
		})
	}
}

func TestTypedRepairMergesExistingClaimAndPreservesSources(t *testing.T) {
	loader, spec := typedRepairFixture(t, "Synthetic corroborating source.\n")
	neighbor := "\n<!-- Preserve  neighboring text. -->\n"
	loader.files[semanticFactsRelativePath] = append(
		loader.files[semanticFactsRelativePath],
		[]byte(neighbor)...)
	spec.FileSnapshots[semanticFactsRuntimePath] = FileSnapshot{
		Existed: true,
		Content: append([]byte(nil), loader.files[semanticFactsRelativePath]...),
	}
	spec.Baseline[semanticFactsRuntimePath] = string(loader.files[semanticFactsRelativePath])
	_, err := NewSemanticMemoryExtractionRegistration(
		0,
		0,
	).NewJob().
		ApplyResult(context.Background(), loader, JobOutput{
			Spec: spec, FinalText: `{"decisions":[{"offset":0,"outcome":"converted","claim_id":"c-2026-0001"}]}`,
		})
	if err != nil {
		t.Fatal(err)
	}
	parsed, _, err := claims.ParseClaims(string(loader.files[semanticFactsRelativePath]))
	if err != nil || len(parsed) != 1 || len(parsed[0].Src) != 2 || parsed[0].Src[0] != "t12" ||
		parsed[0].Src[1] != "mem:"+repairNotesPath+":0" {
		t.Fatalf("merge lost provenance: %#v %v", parsed, err)
	}
	if !strings.HasSuffix(string(loader.files[semanticFactsRelativePath]), neighbor) {
		t.Fatal("merge rewrote neighboring bytes")
	}
}

func TestTypedWorkingRepairCreatesLoopWithoutModelCopying(t *testing.T) {
	loader, spec := workingRepairSpec(t)
	pre := spec.FileSnapshots[workingMemoryRuntimePath].Content
	offset := strings.Index(string(pre), "- Ship")
	response := fmt.Sprintf(
		`{"decisions":[{"offset":%d,"outcome":"moved","loop":{"id":"l-2026-0001","trigger":"next-session","owner":"agent"}}]}`,
		offset,
	)
	_, err := NewSemanticMemoryExtractionRegistration(
		0,
		0,
	).NewJob().
		ApplyResult(context.Background(), loader, JobOutput{Spec: spec, FinalText: response})
	if err != nil {
		t.Fatal(err)
	}
	loops, err := parseOpenLoops(string(loader.files[openLoopsRelativePath]))
	if err != nil || len(loops) != 1 || loops[0].Text != "Ship the unfinished release." ||
		loops[0].Src != fmt.Sprintf("mem:%s:%d", workingMemoryRelativePath, offset) {
		t.Fatalf("unexpected loop: %#v %v", loops, err)
	}
	if len(loader.files[workingMemoryRelativePath]) <= defaultWorkingMemoryBudgetBytes {
		t.Fatal("fixture no longer checks an incremental over-cap shrink")
	}
}

type repairFailOnceLoader struct {
	*repairProbeLoader
	calls int
}

func (l *repairFailOnceLoader) WriteFileBytes(
	ctx context.Context,
	relative string,
	raw []byte,
) error {
	l.calls++
	if l.calls == 2 {
		return fmt.Errorf("synthetic one-time write failure")
	}
	return l.repairProbeLoader.WriteFileBytes(ctx, relative, raw)
}

func TestTypedRepairWriteFailureRestoresSourceLedgerAndNewClaims(t *testing.T) {
	base, spec := typedRepairFixture(t, "Synthetic durable source.\n")
	loader := &repairFailOnceLoader{repairProbeLoader: base}
	_, err := NewSemanticMemoryExtractionRegistration(
		0,
		0,
	).NewJob().
		ApplyResult(context.Background(), loader, JobOutput{
			Spec: spec, FinalText: `{"decisions":[{"offset":0,"outcome":"converted","claim_id":"c-2026-0007","claim":{"path":"semantic/topics/synthetic.md","text":"Synthetic durable knowledge.","status":"active","kind":"fact","confidence":"confirmed"}}]}`,
		})
	if err == nil || !strings.Contains(err.Error(), "synthetic one-time write failure") {
		t.Fatalf("expected injected failure, got %v", err)
	}
	for runtimePath, snapshot := range spec.FileSnapshots {
		if string(base.files[relativeMemoryPath(runtimePath)]) != string(snapshot.Content) {
			t.Fatalf("failed to restore %s", runtimePath)
		}
	}
	if _, exists := base.files["semantic/topics/synthetic.md"]; exists {
		t.Fatal("partial claim survived failed application")
	}
}

func TestTypedRepairModelCannotMutateMemory(t *testing.T) {
	_, spec := typedRepairFixture(t, "Synthetic source.\n")
	for _, name := range spec.AllowedTools {
		if name != "read_file" && name != "embed_search" {
			t.Fatalf("repair exposes mutation tool %s", name)
		}
	}
	if spec.RequireToolCalling {
		t.Fatal("repair requires a tool call for a decision already grounded in the chunk")
	}
	if len(spec.InputMessages) != 0 {
		t.Fatal("repair gained transcript input")
	}
}

func TestTypedRepairMakesStructureOnlyProgressWithoutRemovingCanonicalHeadings(t *testing.T) {
	files := repairPreFiles()
	delete(files, repairNotesPath)
	before := "# Semantic Facts\n\n## Confirmed Facts\n" + strings.Repeat(
		"## Confirmed Facts\n",
		1800,
	) + "\n## Grounded Inferences\n\n- None\n"
	files[semanticFactsRelativePath] = before
	loader := &repairProbeLoader{
		semanticMemoryJobLoader: &semanticMemoryJobLoader{files: repairRawFiles(files)},
	}
	for path, raw := range files {
		loader.memoryFiles = append(
			loader.memoryFiles,
			MemoryFile{Path: path, Bytes: int64(len(raw))},
		)
	}
	job := NewSemanticMemoryExtractionRegistration(0, 0).NewJob()
	spec, err := job.Build(context.Background(), loader)
	if err != nil {
		t.Fatal(err)
	}
	_, err = job.ApplyResult(
		context.Background(),
		loader,
		JobOutput{Spec: spec, FinalText: `{"decisions":[]}`},
	)
	if err != nil {
		t.Fatal(err)
	}
	post := string(loader.files[semanticFactsRelativePath])
	if len(post) >= len(before) || !strings.HasPrefix(post, "# Semantic Facts\n") ||
		!strings.Contains(post, "## Confirmed Facts\n") ||
		!strings.HasSuffix(post, "\n## Grounded Inferences\n\n- None\n") {
		t.Fatal("structure-only repair did not preserve required headings while shrinking")
	}
}

func TestTypedRepairIgnoresDecisionsForLinesItDoesNotDrain(t *testing.T) {
	loader, spec := typedRepairFixture(t, "# Heading\n\nSynthetic source.\n")
	response := `{"decisions":[{"offset":0,"outcome":"dropped","reason":"Headings never leave."},` +
		`{"offset":10,"outcome":"dropped","reason":"Blank lines never leave."},` +
		`{"offset":11,"outcome":"dropped","reason":"Synthetic completed task."},` +
		`{"offset":999,"outcome":"dropped","reason":"An offset past the chunk."}]}`
	result, err := NewSemanticMemoryExtractionRegistration(
		0,
		0,
	).NewJob().
		ApplyResult(context.Background(), loader, JobOutput{Spec: spec, FinalText: response})
	if err != nil {
		t.Fatal(err)
	}
	if result.Metadata["chunk_lines_exited"] != "1" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if remainder, exists := loader.files[repairNotesPath]; exists {
		t.Fatalf("target outlived its last content line: %q", remainder)
	}
	entries, err := claims.ParseLedger(loader.files[workingLedgerRelativePath])
	if err != nil || len(entries) != 2 {
		t.Fatalf("unexpected ledger=%#v error=%v", entries, err)
	}
	var exits []claims.LedgerEntry
	for _, entry := range entries {
		if entry.Description == "Synthetic source." {
			exits = append(exits, entry)
		}
	}
	if len(exits) != 1 ||
		exits[0].SourceSeq != fmt.Sprintf("mem:%s:%d", repairNotesPath, 11) {
		t.Fatalf("blank and invented offsets reached the ledger: %#v", entries)
	}
}

func TestTypedRepairKeepsTheLastDecisionForARepeatedOffset(t *testing.T) {
	loader, spec := typedRepairFixture(t, "Synthetic source.\n")
	response := `{"decisions":[{"offset":0,"outcome":"dropped","reason":"First thought."},` +
		`{"offset":0,"outcome":"dropped","reason":"Second thought."}]}`
	result, err := NewSemanticMemoryExtractionRegistration(
		0,
		0,
	).NewJob().
		ApplyResult(context.Background(), loader, JobOutput{Spec: spec, FinalText: response})
	if err != nil {
		t.Fatal(err)
	}
	if result.Metadata["chunk_lines_exited"] != "1" {
		t.Fatalf("unexpected result: %#v", result)
	}
	entries, err := claims.ParseLedger(loader.files[workingLedgerRelativePath])
	if err != nil {
		t.Fatal(err)
	}
	var exits []claims.LedgerEntry
	for _, entry := range entries {
		if entry.SourceSeq == fmt.Sprintf("mem:%s:%d", repairNotesPath, 0) &&
			entry.Description == "Synthetic source." {
			exits = append(exits, entry)
		}
	}
	if len(exits) != 1 || exits[0].Note != "Second thought." {
		t.Fatalf("kept the wrong decision: %#v", exits)
	}
}

func TestTypedRepairNamesTheContentLineWithoutADecision(t *testing.T) {
	loader, spec := typedRepairFixture(t, "# Heading\n\nSynthetic source.\n")
	before := string(loader.files[repairNotesPath])
	_, err := NewSemanticMemoryExtractionRegistration(
		0,
		0,
	).NewJob().
		ApplyResult(
			context.Background(),
			loader,
			JobOutput{
				Spec:      spec,
				FinalText: `{"decisions":[{"offset":0,"outcome":"dropped","reason":"Headings never leave."}]}`,
			},
		)
	if err == nil || !strings.Contains(err.Error(), "offset 11 is missing") {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(loader.files[repairNotesPath]) != before {
		t.Fatal("refused plan changed the target")
	}
}
