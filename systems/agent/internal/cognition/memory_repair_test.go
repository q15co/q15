package cognition

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/memory/claims"
)

// TestCutRepairChunk pins the bounded-cursor contract: a target far larger
// than the window yields only whole lines, no further than repairChunkBytes
// into the file, each pinned by its byte offset and the SHA-256 of its bytes
// without the trailing newline.
func TestCutRepairChunk(t *testing.T) {
	t.Parallel()

	// A 900 KB target of fixed-width 90-byte lines.
	const lineBytes = 90
	line := func(i int) string {
		text := fmt.Sprintf("line-%06d", i)
		return text + strings.Repeat(".", lineBytes-1-len(text)) + "\n"
	}
	var builder strings.Builder
	for i := 0; i < 900*1024/lineBytes; i++ {
		builder.WriteString(line(i))
	}
	head := builder.String()
	if len(head) != 900*1024 {
		t.Fatalf("fixture is %d bytes, want 900 KiB", len(head))
	}

	chunk, err := cutRepairChunk([]byte(head))
	if err != nil {
		t.Fatalf("cutRepairChunk() error = %v", err)
	}
	span := 0
	for i, got := range chunk {
		want := strings.TrimSuffix(line(i), "\n")
		if got.offset != i*lineBytes || got.text != want {
			t.Fatalf("chunk[%d] = offset %d text %q, want offset %d text %q",
				i, got.offset, got.text, i*lineBytes, want)
		}
		if got.sum != sha256.Sum256([]byte(want)) {
			t.Fatalf("chunk[%d] sum = %x, want sha256 of the line without newline", i, got.sum)
		}
		span = got.offset + len(want) + 1
	}
	if span > repairChunkBytes {
		t.Fatalf("chunk spans %d bytes, over the %d-byte window", span, repairChunkBytes)
	}
	if (len(chunk)+1)*lineBytes <= repairChunkBytes {
		t.Fatalf("chunk holds %d lines but one more whole line fits the window", len(chunk))
	}
}

// TestCutRepairChunkBoundaries pins the window edges: a line ending exactly
// at the boundary is in, a straddling line waits for the next run, a first
// line longer than the window is an error rather than a silent truncation,
// and a final line at end of file without a trailing newline is whole.
func TestCutRepairChunkBoundaries(t *testing.T) {
	t.Parallel()

	atBoundary := strings.Repeat("x", repairChunkBytes-1) + "\ntail\n"
	chunk, err := cutRepairChunk([]byte(atBoundary))
	if err != nil || len(chunk) != 1 || len(chunk[0].text) != repairChunkBytes-1 {
		t.Fatalf(
			"boundary line: chunk=%d lines err=%v, want the one line that ends at the boundary",
			len(chunk),
			err,
		)
	}

	straddle := "first\n" + strings.Repeat("y", repairChunkBytes)
	chunk, err = cutRepairChunk([]byte(straddle))
	if err != nil || len(chunk) != 1 || chunk[0].text != "first" {
		t.Fatalf("straddling line: chunk=%v err=%v, want only the first line", chunk, err)
	}

	if _, err := cutRepairChunk([]byte(strings.Repeat("z", repairChunkBytes+1))); err == nil {
		t.Fatal("a single line longer than the window was accepted; want refusal, not truncation")
	}
	// An empty file holds no line to drain at all, which is the adoption case
	// rather than a refusal: the run is a no-op and the runtime removes the file.
	chunk, err = cutRepairChunk(nil)
	if err != nil || len(chunk) != 0 {
		t.Fatalf("empty file: chunk=%v err=%v, want no lines and no refusal", chunk, err)
	}

	chunk, err = cutRepairChunk([]byte("one\ntwo"))
	if err != nil || len(chunk) != 2 || chunk[1].offset != 4 || chunk[1].text != "two" {
		t.Fatalf("EOF-terminated line: chunk=%v err=%v, want both lines whole", chunk, err)
	}
}

// TestCutRepairChunkSkipsLeadingStructure pins the named twin of the
// adoption loop: a target whose head is headings and blank lines must yield a
// chunk that starts at its first content line. Cutting at byte zero would
// produce an empty-content chunk on every run, validation would ask for no
// progress, and the target would never shrink while the state rule kept
// refiring on the same bytes.
func TestCutRepairChunkSkipsLeadingStructure(t *testing.T) {
	t.Parallel()

	head := []byte("# Heading\n\n\n" + "a content line\n")
	chunk, err := cutRepairChunk(head)
	if err != nil {
		t.Fatalf("cutRepairChunk() error = %v", err)
	}
	if len(chunk) != 1 || chunk[0].text != "a content line" {
		t.Fatalf("chunk = %#v, want only the content line", chunk)
	}
	if want := strings.Index(string(head), "a content line"); chunk[0].offset != want {
		t.Fatalf("offset = %d, want the content line's file offset %d", chunk[0].offset, want)
	}
}

// A target that is structure all the way down still yields the empty chunk the
// adoption step needs, rather than a refusal.
func TestCutRepairChunkStructureOnlyStillCuts(t *testing.T) {
	t.Parallel()

	chunk, err := cutRepairChunk([]byte("# Heading\n\n- None\n"))
	if err != nil || len(chunk) == 0 {
		t.Fatalf("chunk = %#v err = %v, want the structure lines and no refusal", chunk, err)
	}
}

// TestRepairContentLine pins which lines need a ledgered exit: blank lines,
// markdown headings and the exact working-memory scaffold are structure;
// everything else is content.
func TestRepairContentLine(t *testing.T) {
	t.Parallel()

	for _, line := range []string{"", "  ", "# Heading", "## deep", "\t# Indented heading", "- None"} {
		if repairContentLine(line) {
			t.Fatalf("repairContentLine(%q) = true, want structure", line)
		}
	}
	for _, line := range []string{"text", "- [c-2026-0001] claim", "  {status: active}", `{ "json": true }`} {
		if !repairContentLine(line) {
			t.Fatalf("repairContentLine(%q) = false, want content", line)
		}
	}
}

// repairProbeLoader drives repair tests from a raw file map and records the
// loader calls a repair build must never make: the semantic roots, the
// topics, the transcript and working memory.
type repairProbeLoader struct {
	*semanticMemoryJobLoader
	memoryFiles []MemoryFile
	forbidden   []string
}

var _ ContextLoader = (*repairProbeLoader)(nil)

func (l *repairProbeLoader) ListMemoryFiles(context.Context) ([]MemoryFile, error) {
	return l.memoryFiles, nil
}

func (l *repairProbeLoader) LoadSemanticMemory(ctx context.Context) (agent.SemanticMemory, error) {
	l.forbidden = append(l.forbidden, "LoadSemanticMemory")
	return l.semanticMemoryJobLoader.LoadSemanticMemory(ctx)
}

func (l *repairProbeLoader) LoadSemanticTopics(ctx context.Context) (map[string]string, error) {
	l.forbidden = append(l.forbidden, "LoadSemanticTopics")
	return l.semanticMemoryJobLoader.LoadSemanticTopics(ctx)
}

func (l *repairProbeLoader) LoadWorkingMemory(ctx context.Context) (agent.WorkingMemory, error) {
	l.forbidden = append(l.forbidden, "LoadWorkingMemory")
	return l.semanticMemoryJobLoader.LoadWorkingMemory(ctx)
}

func (l *repairProbeLoader) LoadRecentMessages(
	ctx context.Context,
	turns int,
) ([]conversation.Message, error) {
	l.forbidden = append(l.forbidden, "LoadRecentMessages")
	return l.semanticMemoryJobLoader.LoadRecentMessages(ctx, turns)
}

func (l *repairProbeLoader) LoadLatestMessages(
	ctx context.Context,
	turns int,
) ([]conversation.Message, error) {
	l.forbidden = append(l.forbidden, "LoadLatestMessages")
	return l.semanticMemoryJobLoader.LoadLatestMessages(ctx, turns)
}

func (l *repairProbeLoader) LoadMessagesSinceSeq(
	ctx context.Context,
	afterSeq int64,
) ([]conversation.Message, error) {
	l.forbidden = append(l.forbidden, "LoadMessagesSinceSeq")
	return l.semanticMemoryJobLoader.LoadMessagesSinceSeq(ctx, afterSeq)
}

func (l *repairProbeLoader) LoadMessagesSinceSeqWindow(
	ctx context.Context,
	afterSeq int64,
	maxTurns int,
) (TurnWindow, error) {
	l.forbidden = append(l.forbidden, "LoadMessagesSinceSeqWindow")
	return l.semanticMemoryJobLoader.LoadMessagesSinceSeqWindow(ctx, afterSeq, maxTurns)
}

// TestSemanticRepairBuildStaysSmallWithHugeRoots is the acceptance test the
// layer exists for: with the semantic roots far beyond anything a model will
// read, a dirty surface still builds a small, self-contained repair prompt,
// and Build never touches the roots, the topics, the transcript or working
// memory to do it.
func TestSemanticRepairBuildStaysSmallWithHugeRoots(t *testing.T) {
	t.Parallel()

	marker := "THE BURIED MARKER LINE FAR BELOW THE FIRST CHUNK"
	bigFacts := strings.Repeat(
		"A durable fact line of ninety bytes, padded out for the root file here 000000000\n",
		36000,
	) +
		marker + "\n" +
		strings.Repeat(
			"Another durable fact line of ninety bytes, padded out for the file 0000000000\n",
			4000,
		)
	roots := map[string]string{
		semanticFactsRelativePath:       bigFacts,
		semanticPreferencesRelativePath: strings.Repeat("p", 2*1024*1024),
		semanticProjectsRelativePath:    strings.Repeat("j", 2*1024*1024),
		"working/LEDGER.md":             "# Conversion Ledger\n",
	}
	loader := &repairProbeLoader{
		semanticMemoryJobLoader: &semanticMemoryJobLoader{
			files:      repairRawFiles(roots),
			checkpoint: SemanticExtractionCheckpoint{LastExtractedSeq: 100},
		},
		memoryFiles: []MemoryFile{
			{Path: semanticFactsRelativePath, Bytes: int64(len(bigFacts))},
			{Path: semanticPreferencesRelativePath, Bytes: 2 * 1024 * 1024},
			{Path: semanticProjectsRelativePath, Bytes: 2 * 1024 * 1024},
			{Path: "working/LEDGER.md", Bytes: 20},
		},
	}

	spec, err := NewSemanticMemoryExtractionRegistration(0, 0).NewJob().Build(
		context.Background(),
		loader,
	)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if !spec.repair {
		t.Fatal("Build() did not branch into the repair spec on a dirty surface")
	}
	if len(loader.forbidden) != 0 {
		t.Fatalf(
			"repair build loaded forbidden inputs %v: roots, topics, transcript and working memory stay out of the repair prompt",
			loader.forbidden,
		)
	}
	prompt, err := renderPrompt(semanticMemoryExtractionJobType, spec)
	if err != nil {
		t.Fatalf("renderPrompt() error = %v", err)
	}
	if len(prompt) >= 32*1024 {
		t.Fatalf(
			"repair prompt is %d bytes against roots of several MB; the deadlock is not broken",
			len(prompt),
		)
	}
	if strings.Contains(prompt, marker) {
		t.Fatal("repair prompt contains target content below the first chunk")
	}
	for _, want := range []string{"offset=0 sha256=", "src=mem:" + semanticFactsRelativePath + ":", conversionClaimFormatRule, conversionLedgerFormatRule} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("repair prompt does not carry %q", want)
		}
	}
	if got, want := spec.CheckpointSeq, int64(100); got != want {
		t.Fatalf(
			"repair spec CheckpointSeq = %d, want %d (the untouched extraction checkpoint)",
			got,
			want,
		)
	}
	if !spec.FileSnapshots[semanticFactsRuntimePath].Existed {
		t.Fatal("repair spec did not pin the target's pre-run bytes")
	}
	if _, ok := spec.FileSnapshots[workingLedgerRuntimePath]; !ok {
		t.Fatal("repair spec carries no ledger snapshot for rollback")
	}

	// The write scope is exactly the target, the ledger and the claim files.
	call := func(path string) error {
		return spec.ToolCallPolicy.CheckToolCall(agent.ToolCall{
			ID:        "probe",
			Name:      "write_file",
			Arguments: fmt.Sprintf(`{"path":%q,"content":"x"}`, path),
		})
	}
	if err := call(semanticFactsRuntimePath); err != nil {
		t.Fatalf("the target is not writable in a repair run: %v", err)
	}
	if err := call(workingLedgerRuntimePath); err != nil {
		t.Fatalf("the ledger is not writable in a repair run: %v", err)
	}
	for _, path := range []string{workingMemoryRuntimePath, openLoopsRuntimePath} {
		if err := call(path); err == nil {
			t.Fatalf(
				"repair policy permits writing %s; want the write scope limited to the target, the ledger and the claim files",
				path,
			)
		}
	}
}

// TestSemanticRepairCleanSurfaceBuildsOrdinaryPrompt pins that a clean
// surface changes nothing: Build loads the roots and builds the ordinary
// conversion spec.
func TestSemanticRepairCleanSurfaceBuildsOrdinaryPrompt(t *testing.T) {
	t.Parallel()

	loader := &repairProbeLoader{
		semanticMemoryJobLoader: goldenSemanticLoader(),
		memoryFiles: []MemoryFile{
			{Path: semanticFactsRelativePath, Bytes: 100},
			{Path: "working/WORKING_MEMORY.md", Bytes: 8192},
			{Path: "working/LEDGER.md", Bytes: 4096},
		},
	}
	spec, err := NewSemanticMemoryExtractionRegistration(0, 0).NewJob().Build(
		context.Background(),
		loader,
	)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if spec.repair {
		t.Fatal("Build() branched into the repair spec on a clean surface")
	}
	if !accessIncludesPath(loader.forbidden, "LoadSemanticMemory") {
		t.Fatal("clean-surface Build never loaded the semantic roots")
	}
	prompt, err := renderPrompt(semanticMemoryExtractionJobType, spec)
	if err != nil {
		t.Fatalf("renderPrompt() error = %v", err)
	}
	if !strings.Contains(prompt, "semantic_facts_target") {
		t.Fatal("clean-surface prompt lost the ordinary semantic targets")
	}
}

// TestSemanticRepairBuildFailsWhenTargetVanishes pins the scan/read race:
// a listed target that cannot be read back is a build error, not a skipped
// pass.
func TestSemanticRepairBuildFailsWhenTargetVanishes(t *testing.T) {
	t.Parallel()

	loader := &repairProbeLoader{
		semanticMemoryJobLoader: &semanticMemoryJobLoader{files: map[string][]byte{}},
		memoryFiles:             []MemoryFile{{Path: "semantic/state/stale.json", Bytes: 512}},
	}
	if _, err := NewSemanticMemoryExtractionRegistration(0, 0).NewJob().Build(
		context.Background(),
		loader,
	); err == nil || !strings.Contains(err.Error(), "vanished") {
		t.Fatalf("Build() error = %v, want the vanished-target error", err)
	}
}

// -- shared fixtures for the repair apply tests --

const (
	repairNotesPath   = "semantic/state/notes.md"
	repairAlphaLine   = "The alpha project shipped in May."
	repairScratchLine = "Scratch: remember to water the plant."
	repairNotesPre    = "# Old notes\n" + repairAlphaLine + "\n" + repairScratchLine + "\n"
	repairLedgerPre   = "# Conversion Ledger\n\n"
)

func repairRawFiles(files map[string]string) map[string][]byte {
	raw := make(map[string][]byte, len(files))
	for path, content := range files {
		raw[path] = []byte(content)
	}
	return raw
}

func repairPreFiles() map[string]string {
	return map[string]string{
		repairNotesPath:                 repairNotesPre,
		semanticFactsRelativePath:       validConversionClaim,
		semanticPreferencesRelativePath: "# Semantic Preferences\n",
		semanticProjectsRelativePath:    "# Semantic Projects\n",
		"working/LEDGER.md":             repairLedgerPre,
	}
}

func repairPreListing() []MemoryFile {
	files := repairPreFiles()
	listing := make([]MemoryFile, 0, len(files))
	for path, content := range files {
		listing = append(listing, MemoryFile{Path: path, Bytes: int64(len(content))})
	}
	return listing
}

func repairAlphaOffset() int   { return strings.Index(repairNotesPre, repairAlphaLine) }
func repairScratchOffset() int { return strings.Index(repairNotesPre, repairScratchLine) }

// repairConvertedExit is the happy-path ledger line for the alpha line:
// converted into claim c-2026-0007 with the memory citation naming the line.
func repairConvertedExit() string {
	return fmt.Sprintf(
		"2026-10-16T05:00Z run=%s src=mem:%s:%d converted %q → c-2026-0007 (%s)\n",
		semanticMemoryExtractionJobType,
		repairNotesPath,
		repairAlphaOffset(),
		repairAlphaLine,
		semanticFactsRelativePath,
	)
}

func repairDroppedExit() string {
	return fmt.Sprintf(
		"2026-10-16T05:01Z run=%s src=mem:%s:%d dropped %q (transient chore)\n",
		semanticMemoryExtractionJobType,
		repairNotesPath,
		repairScratchOffset(),
		repairScratchLine,
	)
}

// repairAdoptedClaim is the claim the alpha line becomes, citing the line it
// drained from with the memory source form.
func repairAdoptedClaim() string {
	return fmt.Sprintf(
		"- [c-2026-0007] %s\n  {status: active, kind: fact, confidence: confirmed, src: mem:%s:%d, rev: 2026-10-16}\n",
		repairAlphaLine,
		repairNotesPath,
		repairAlphaOffset(),
	)
}

// repairRunMessages is the repair counterpart of conversionRunMessages: one
// successful file-tool write per path, no reads.
func repairRunMessages(writes ...string) []conversation.Message {
	var messages []conversation.Message
	for i, target := range writes {
		id := fmt.Sprintf("repair-write-%d", i)
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

// repairApplyFixtures describes the post-run store state that ApplyResult
// wakes up to after a repair run; a nil targetPost means the file is gone.
type repairApplyFixture struct {
	factsPost  string
	ledgerPost string
	targetPost *string
	writes     []string
}

// repairHappyFixture is the good run: the alpha line became a claim, the
// scratch line was dropped, both are gone from the target, which keeps only
// its heading.
func repairHappyFixture() repairApplyFixture {
	targetPost := "# Old notes\n"
	return repairApplyFixture{
		factsPost:  validConversionClaim + repairAdoptedClaim(),
		ledgerPost: repairLedgerPre + repairConvertedExit() + repairDroppedExit(),
		targetPost: &targetPost,
		writes: []string{
			semanticFactsRuntimePath,
			memoryRuntimePrefix + repairNotesPath,
			workingLedgerRuntimePath,
		},
	}
}

// runRepairApply builds the repair spec from the pre-run fixture, moves the
// store to the described post-run state, and applies.
func runRepairApply(
	t *testing.T,
	fixture repairApplyFixture,
) (ParsedResult, *repairProbeLoader, error) {
	t.Helper()
	loader := &repairProbeLoader{
		semanticMemoryJobLoader: &semanticMemoryJobLoader{
			files:      repairRawFiles(repairPreFiles()),
			checkpoint: SemanticExtractionCheckpoint{LastExtractedSeq: 100},
		},
		memoryFiles: repairPreListing(),
	}
	job := NewSemanticMemoryExtractionRegistration(0, 0).NewJob()
	spec, err := job.Build(context.Background(), loader)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if !spec.repair {
		t.Fatal("Build() did not branch into the repair spec")
	}

	if fixture.factsPost != "" {
		loader.files[semanticFactsRelativePath] = []byte(fixture.factsPost)
	}
	if fixture.ledgerPost != "" {
		loader.files["working/LEDGER.md"] = []byte(fixture.ledgerPost)
	}
	if fixture.targetPost == nil {
		delete(loader.files, repairNotesPath)
	} else {
		loader.files[repairNotesPath] = []byte(*fixture.targetPost)
	}

	result, err := job.ApplyResult(context.Background(), loader, JobOutput{
		Type:      semanticMemoryExtractionJobType,
		Spec:      spec,
		FinalText: "Drained the stray notes chunk.",
		Messages:  repairRunMessages(fixture.writes...),
	})
	return result, loader, err
}

// TestSemanticRepairAdoptsTargetWithNoContentLine pins the case that would
// otherwise never end: an un-named target holding no content line at all -
// structure only, or empty. There is nothing to drain, so requiring progress
// would fail every run while the layer-1 state rule keeps firing on the same
// target: the surface would stay dirty and the job would error forever. The
// run must instead be a no-op that lets the runtime adopt the file.
func TestSemanticRepairAdoptsTargetWithNoContentLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		pre  string
	}{
		{"structure only", "# Old notes\n"},
		{"empty", ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			files := repairPreFiles()
			files[repairNotesPath] = test.pre
			listing := make([]MemoryFile, 0, len(files))
			for path, content := range files {
				listing = append(listing, MemoryFile{Path: path, Bytes: int64(len(content))})
			}
			loader := &repairProbeLoader{
				semanticMemoryJobLoader: &semanticMemoryJobLoader{
					files:      repairRawFiles(files),
					checkpoint: SemanticExtractionCheckpoint{LastExtractedSeq: 100},
				},
				memoryFiles: listing,
			}

			job := NewSemanticMemoryExtractionRegistration(0, 0).NewJob()
			spec, err := job.Build(context.Background(), loader)
			if err != nil {
				t.Fatalf(
					"Build() error = %v, want a repair spec for a target with no content line",
					err,
				)
			}
			if !spec.repair {
				t.Fatal("Build() did not branch into the repair spec")
			}
			if _, err := renderPrompt(semanticMemoryExtractionJobType, spec); err != nil {
				t.Fatalf("renderPrompt() error = %v", err)
			}

			// The model does nothing, because there is nothing to drain.
			result, err := job.ApplyResult(context.Background(), loader, JobOutput{
				Type: semanticMemoryExtractionJobType,
				Spec: spec,
			})
			if err != nil {
				t.Fatalf("ApplyResult() error = %v, want the runtime to adopt the target", err)
			}
			if len(loader.deleted) != 1 || loader.deleted[0] != repairNotesPath {
				t.Fatalf("deleted = %v, want exactly the adopted target", loader.deleted)
			}
			if got := result.Metadata["repair_target_removed"]; got != "true" {
				t.Fatalf("Metadata[repair_target_removed] = %q, want true", got)
			}
			ledger := string(loader.files["working/LEDGER.md"])
			appended := strings.TrimSuffix(strings.TrimPrefix(ledger, repairLedgerPre), "\n")
			if !strings.Contains(appended, "src=mem:"+repairNotesPath+":0") {
				t.Fatalf("ledger = %q, want one adoption record", ledger)
			}
			if strings.Count(appended, "\n") != 0 {
				t.Fatalf("appended ledger = %q, want exactly one record", appended)
			}
		})
	}
}

// TestSemanticRepairRunAdoptsDrainedUnNamedTarget is the happy path end to
// end: validation passes, the runtime deletes the target that holds no
// content line and records the adoption on the ledger exactly once, and the
// result reports the repair.
func TestSemanticRepairRunAdoptsDrainedUnNamedTarget(t *testing.T) {
	t.Parallel()

	result, loader, err := runRepairApply(t, repairHappyFixture())
	if err != nil {
		t.Fatalf("ApplyResult() error = %v", err)
	}
	for key, want := range map[string]string{
		"repair":                "true",
		"repair_target":         repairNotesPath,
		"repair_target_removed": "true",
		"chunk_lines_exited":    "2",
	} {
		if got := result.Metadata[key]; got != want {
			t.Fatalf(
				"Metadata[%q] = %q, want %q (all metadata: %v)",
				key,
				got,
				want,
				result.Metadata,
			)
		}
	}
	if got, want := result.CheckpointSeq, int64(100); got != want {
		t.Fatalf("CheckpointSeq = %d, want the carried extraction checkpoint %d", got, want)
	}
	if len(loader.deleted) != 1 || loader.deleted[0] != repairNotesPath {
		t.Fatalf("deleted = %v, want exactly the drained target", loader.deleted)
	}

	ledger := string(loader.files["working/LEDGER.md"])
	wantLedger := repairLedgerPre + repairConvertedExit() + repairDroppedExit()
	if !strings.HasPrefix(ledger, wantLedger) {
		t.Fatalf("ledger = %q, want the run's two exits appended to the pre-run ledger", ledger)
	}
	appended := strings.TrimPrefix(ledger, wantLedger)
	adoptions := strings.Count(appended, "src=mem:"+repairNotesPath+":0")
	if adoptions != 1 || strings.Count(strings.TrimSuffix(appended, "\n"), "\n") != 0 {
		t.Fatalf("adoption suffix = %q, want exactly one adoption record", appended)
	}
	record, err := claims.ParseLedgerEntry(strings.TrimSuffix(appended, "\n"))
	if err != nil {
		t.Fatalf("adoption record does not parse: %v", err)
	}
	if record.Run != semanticMemoryExtractionJobType || record.Outcome != claims.OutcomeDropped ||
		record.Description != repairNotesPath || !strings.Contains(record.Note, "adopt") {
		t.Fatalf("adoption record = %#v, want a dropped exit naming the adoption", record)
	}
}

// TestSemanticRepairRunDeletesNothingForNamedTarget pins the asymmetry: a
// named target over its budget drains like any other but is schema-owned, so
// the runtime never removes it and records no adoption. The listing reports
// the file over budget; its content is the small head the chunk cut from.
func TestSemanticRepairRunDeletesNothingForNamedTarget(t *testing.T) {
	t.Parallel()

	workingPre := "# Working Memory\n\n## Active Tasks\n\n- Ship T9.\n\n## Open Threads\n\n- None\n"
	taskOffset := strings.Index(workingPre, "- Ship T9.")
	pre := repairPreFiles()
	delete(pre, repairNotesPath)
	pre["working/WORKING_MEMORY.md"] = workingPre
	listing := make([]MemoryFile, 0, len(pre))
	for path, content := range pre {
		bytes := int64(len(content))
		if path == "working/WORKING_MEMORY.md" {
			bytes = 9000 // the stat-only scan saw the file over its budget
		}
		listing = append(listing, MemoryFile{Path: path, Bytes: bytes})
	}
	loader := &repairProbeLoader{
		semanticMemoryJobLoader: &semanticMemoryJobLoader{files: repairRawFiles(pre)},
		memoryFiles:             listing,
	}
	job := NewSemanticMemoryExtractionRegistration(0, 0).NewJob()
	spec, err := job.Build(context.Background(), loader)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if !spec.repair || spec.repairTarget.Path != "working/WORKING_MEMORY.md" ||
		!spec.repairTarget.Named || spec.repairTarget.Cap != 8192 {
		t.Fatalf("repair target = %#v, want working memory over its budget", spec.repairTarget)
	}

	drop := fmt.Sprintf(
		"2026-10-16T05:00Z run=%s src=mem:working/WORKING_MEMORY.md:%d dropped %q (task finished elsewhere)\n",
		semanticMemoryExtractionJobType,
		taskOffset,
		"- Ship T9.",
	)
	loader.files["working/LEDGER.md"] = []byte(repairLedgerPre + drop)
	loader.files["working/WORKING_MEMORY.md"] = []byte(
		strings.Replace(workingPre, "- Ship T9.\n", "", 1),
	)

	result, err := job.ApplyResult(context.Background(), loader, JobOutput{
		Type:      semanticMemoryExtractionJobType,
		Spec:      spec,
		FinalText: "Dropped the finished task.",
		Messages:  repairRunMessages(workingMemoryRuntimePath, workingLedgerRuntimePath),
	})
	if err != nil {
		t.Fatalf("ApplyResult() error = %v", err)
	}
	if got := result.Metadata["repair_target_removed"]; got != "false" {
		t.Fatalf("repair_target_removed = %q, want false for a named target", got)
	}
	if len(loader.deleted) != 0 {
		t.Fatalf("deleted = %v, want no deletion of a named target", loader.deleted)
	}
	if ledger := string(loader.files["working/LEDGER.md"]); ledger != repairLedgerPre+drop {
		t.Fatalf("ledger gained an adoption record for a named target: %q", ledger)
	}
}

// TestSemanticRepairRunRecordsAdoptionWhenTargetAlreadyGone covers the
// progress fallback: the un-named target is simply gone after the run (this
// run's two exits are recorded drops), and the runtime still records the
// adoption once without needing to delete anything.
func TestSemanticRepairRunRecordsAdoptionWhenTargetAlreadyGone(t *testing.T) {
	t.Parallel()

	dropAlpha := fmt.Sprintf(
		"2026-10-16T05:00Z run=%s src=mem:%s:%d dropped %q (superseded by the ledger)\n",
		semanticMemoryExtractionJobType,
		repairNotesPath,
		repairAlphaOffset(),
		repairAlphaLine,
	)
	fixture := repairHappyFixture()
	fixture.factsPost = ""
	fixture.ledgerPost = repairLedgerPre + dropAlpha + repairDroppedExit()
	fixture.targetPost = nil
	fixture.writes = []string{workingLedgerRuntimePath}

	result, loader, err := runRepairApply(t, fixture)
	if err != nil {
		t.Fatalf("ApplyResult() error = %v", err)
	}
	if got := result.Metadata["repair_target_removed"]; got != "true" {
		t.Fatalf("repair_target_removed = %q, want true", got)
	}
	ledger := string(loader.files["working/LEDGER.md"])
	if strings.Count(ledger, "src=mem:"+repairNotesPath+":0") != 1 {
		t.Fatalf("ledger = %q, want exactly one adoption record", ledger)
	}
}

// TestSemanticRepairValidationRefusals pins the checks that make validation
// mean something, each against one minimal deviation from the happy run. A
// refused run restores every pre-run byte it snapshotted.
func TestSemanticRepairValidationRefusals(t *testing.T) {
	t.Parallel()

	grown := "# Old notes\n" + strings.Repeat("the file grew instead of shrinking ", 5) + "\n"
	tests := []struct {
		name      string
		mutate    func(*repairApplyFixture)
		wantError string
	}{
		{
			name: "an un-ledgered chunk line",
			mutate: func(f *repairApplyFixture) {
				f.ledgerPost = repairLedgerPre + repairConvertedExit()
			},
			wantError: "left no ledger exit",
		},
		{
			name: "a claim with no ledger line",
			mutate: func(f *repairApplyFixture) {
				f.factsPost += "- [c-2026-0008] An unledgered extra claim.\n  {status: active, kind: fact, confidence: likely, src: mem:" + repairNotesPath + ":46, rev: 2026-10-16}\n"
			},
			wantError: "has no repair ledger exit",
		},
		{
			name: "a citation outside the chunk",
			mutate: func(f *repairApplyFixture) {
				f.ledgerPost = repairLedgerPre + fmt.Sprintf(
					"2026-10-16T05:00Z run=%s src=mem:%s:%d converted %q → c-2026-0007 (%s)\n",
					semanticMemoryExtractionJobType,
					repairNotesPath,
					len(repairNotesPre)+100,
					repairAlphaLine,
					semanticFactsRelativePath,
				) + repairDroppedExit()
			},
			wantError: "does not name a content line of the chunk",
		},
		{
			name: "a cited line still present",
			mutate: func(f *repairApplyFixture) {
				target := repairNotesPre
				f.targetPost = &target
			},
			wantError: "still present in",
		},
		{
			name: "a target that did not shrink",
			mutate: func(f *repairApplyFixture) {
				f.targetPost = &grown
			},
			wantError: "made no progress",
		},
		{
			name: "ledger history rewritten",
			mutate: func(f *repairApplyFixture) {
				f.ledgerPost = "rewritten ledger\n" + repairConvertedExit() + repairDroppedExit()
			},
			wantError: "append-only",
		},
		{
			name: "outcome that is not converted or dropped",
			mutate: func(f *repairApplyFixture) {
				f.ledgerPost = repairLedgerPre + repairConvertedExit() + fmt.Sprintf(
					"2026-10-16T05:01Z run=%s src=mem:%s:%d moved %q → l-2026-0001 (working/OPEN_LOOPS.md)\n",
					semanticMemoryExtractionJobType,
					repairNotesPath,
					repairScratchOffset(),
					repairScratchLine,
				)
			},
			wantError: "converted or dropped",
		},
		{
			name: "an entry that does not name this run",
			mutate: func(f *repairApplyFixture) {
				f.ledgerPost = repairLedgerPre + strings.Replace(
					repairConvertedExit(),
					"run=semantic_memory.extract",
					"run=working_memory.refresh",
					1,
				) + repairDroppedExit()
			},
			wantError: "wrong run",
		},
		{
			name: "a description that does not copy the drained line",
			mutate: func(f *repairApplyFixture) {
				f.ledgerPost = repairLedgerPre + fmt.Sprintf(
					"2026-10-16T05:00Z run=%s src=mem:%s:%d converted %q → c-2026-0007 (%s)\n",
					semanticMemoryExtractionJobType,
					repairNotesPath,
					repairAlphaOffset(),
					"A paraphrase of the alpha line.",
					semanticFactsRelativePath,
				) + repairDroppedExit()
			},
			wantError: "does not copy the drained chunk line",
		},
		{
			name: "a converted exit for a file the run did not write",
			mutate: func(f *repairApplyFixture) {
				f.writes = []string{
					memoryRuntimePrefix + repairNotesPath,
					workingLedgerRuntimePath,
				}
			},
			wantError: "did not write",
		},
		{
			name: "a written claim file left over its cap",
			mutate: func(f *repairApplyFixture) {
				// The padding stays a separate block so the adopted claim
				// itself still parses; only the byte cap is violated.
				f.factsPost = repairHappyFixture().factsPost + "\n" + strings.Repeat(
					"x",
					semanticRootCapBytes,
				) + "\n"
			},
			wantError: "over cap",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := repairHappyFixture()
			test.mutate(&fixture)
			_, loader, err := runRepairApply(t, fixture)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("ApplyResult() error = %v, want %q", err, test.wantError)
			}
			for path, pre := range repairPreFiles() {
				if got := string(loader.files[path]); got != pre {
					t.Fatalf(
						"after a refused run %s = %q, want the pre-run bytes restored",
						path,
						got,
					)
				}
			}
			if len(loader.deleted) != 0 {
				t.Fatalf("a refused run deleted %v", loader.deleted)
			}
		})
	}
}

// TestSemanticRepairValidationFailureRollsBack pins the whole-map restore:
// a failed validation returns the target, the ledger and the claim files to
// the bytes the spec snapshotted.
func TestSemanticRepairValidationFailureRollsBack(t *testing.T) {
	t.Parallel()

	fixture := repairHappyFixture()
	fixture.ledgerPost = repairLedgerPre // nothing exited: every chunk line is un-ledgered
	_, loader, err := runRepairApply(t, fixture)
	if err == nil {
		t.Fatal("ApplyResult() = nil error with no exits recorded")
	}
	if len(loader.files) != len(repairPreFiles()) {
		t.Fatalf("files map = %v, want the pre-run set", reflectKeys(loader))
	}
	for path, pre := range repairPreFiles() {
		if got := string(loader.files[path]); got != pre {
			t.Fatalf("%s = %q after rollback, want %q", path, got, pre)
		}
	}
}

func reflectKeys(loader *repairProbeLoader) []string {
	keys := make([]string, 0, len(loader.files))
	for path := range loader.files {
		keys = append(keys, path)
	}
	return keys
}
