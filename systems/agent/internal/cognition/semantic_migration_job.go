package cognition

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/memory/claims"
	filetools "github.com/q15co/q15/systems/agent/internal/tools/files"
)

const (
	// SemanticMigrationJobType is the job type of the supervised bulk pass
	// that drains the legacy semantic roots into claim format (TICKETS T12,
	// DESIGN §8.2).
	SemanticMigrationJobType = "semantic_memory.migrate"
	// semanticMigrationChunkBytes is the per-run source window, roughly 8 KB
	// of source per run as the ticket suggests. One run consumes one chunk.
	semanticMigrationChunkBytes = 8 * 1024
	// semanticMigrationOversizedChunkBytes is the one-step escalation window
	// for a degenerate source whose next line alone exceeds a regular chunk.
	// A still-unbounded line fails the run instead of looping forever.
	semanticMigrationOversizedChunkBytes = 64 * 1024
	// semanticMigrationScanStepBytes bounds the section-context pre-scan.
	semanticMigrationScanStepBytes = 32 * 1024
	// semanticMigrationBaselineSizeKeyPrefix namespaces the pre-run on-disk
	// size of the processed source inside Spec.Baseline, which otherwise
	// carries file content. The migration's growth rule needs the exact
	// pre-run byte count, and trimmed baseline content cannot provide one.
	semanticMigrationBaselineSizeKeyPrefix = "migration_source_size:"
	// semanticMigrationBaselineChunkKeyPrefix namespaces the pre-run hash of
	// the chunk this run consumes, so ApplyResult can tell that the source
	// moved between Build and ApplyResult.
	semanticMigrationBaselineChunkKeyPrefix = "migration_chunk_sha:"
)

// semanticMigrationSources is the canonical drain order across the three
// semantic roots the migration cursor tracks.
var semanticMigrationSources = []string{
	semanticFactsRelativePath,
	semanticPreferencesRelativePath,
	semanticProjectsRelativePath,
}

// semanticMigrationResidueRuntimePath is the runtime (tool-visible) path of
// the staged archive fold residue. The residue is a migration source, so a run
// must not be able to write into its own source; it is never a policy path.
var semanticMigrationResidueRuntimePath = memoryRuntimePrefix + SemanticMigrationResidueSourcePath

// migrationLedgerLinePattern matches one appended migration ledger line.
// FormatLedgerEntry renders a target as ` → id (path)` and any note as a
// trailing parenthesized group, so the migration note
// `(source=<path>:<byte> sha=<hash>)` is always the final group and the
// linking tail is machine-checkable: the run only drains lines whose cited
// hash matches the cited line.
var migrationLedgerLinePattern = regexp.MustCompile(
	`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}Z run=semantic_memory\.migrate (migrated|dropped)` +
		`(?: "(.+?)")?(?: → (\S+))?(?: \(([^()]*)\))*` +
		` \(source=([^:\s]+):(\d+) sha=([0-9a-f]{64})\)$`,
)

const (
	migrationLedgerGroupOutcome    = 1
	migrationLedgerGroupTarget     = 3
	migrationLedgerGroupSourcePath = 5
	migrationLedgerGroupSourceByte = 6
	migrationLedgerGroupSHA        = 7
)

// NewSemanticMigrationRegistration returns the supervised semantic migration
// job registration. Jobs are a static slice handed to NewController once at
// startup (internal/app/bot.go), so conditional registration is impossible:
// the registration is added unconditionally and gated by its trigger, which
// fires only while a migration cursor with an unfinished source file exists.
// Starting a migration is creating the cursor (the operator's layer above);
// finishing it is deleting the cursor, which the job does itself.
func NewSemanticMigrationRegistration() JobRegistration {
	return JobRegistration{
		NewJob: func() JobDefinition {
			return semanticMigrationJob{}
		},
		Policy: TriggerPolicy{
			State: []StateRule{
				{
					ID:       "semantic_migration_cursor_present",
					Evaluate: evaluateSemanticMigrationCursor,
				},
			},
		},
	}
}

// evaluateSemanticMigrationCursor fires the migration only while the cursor
// exists and still tracks an unfinished source file. No cursor, no run; a
// cursor whose files are all finished produces no run.
func evaluateSemanticMigrationCursor(
	ctx context.Context,
	snapshot Snapshot,
	_ JobState,
) (bool, string, error) {
	if snapshot.Loader == nil {
		return false, "", nil
	}
	cursor, err := LoadSemanticMigrationCursor(ctx, snapshot.Loader)
	if err != nil {
		return false, "", err
	}
	if !SemanticMigrationCursorHasOpenFile(cursor) {
		return false, "", nil
	}
	return true, "semantic_migration_cursor_present", nil
}

type semanticMigrationJob struct{}

func (semanticMigrationJob) Type() string {
	return SemanticMigrationJobType
}

// migrationSourceLine is one whole line of the consumed chunk with the book
// the ledger and the drain step need to name it.
type migrationSourceLine struct {
	// ByteOffset is the line's start offset in the source file.
	ByteOffset int64
	// Raw is the line's exact bytes, including the trailing newline when it
	// has one.
	Raw string
	// Hash is the SemanticLineHash of the line.
	Hash string
	// Section is the nearest preceding markdown heading at read time.
	Section string
}

// migrationLedgerRef is one parsed ledger line appended by a run.
type migrationLedgerRef struct {
	Outcome     string
	TargetID    string
	SourcePath  string
	SourceByte  int64
	Hash        string
	Description string
}

// loadMigrationSourceContent returns the trimmed content of one migration
// source file — a semantic root or the staged archive residue — read through
// the loader's per-source reader rather than the whole semantic memory set,
// which the residue is not part of. A missing file yields empty content, so
// the residue is read exactly when staged and a drained source reads as empty.
func loadMigrationSourceContent(
	ctx context.Context,
	loader ContextLoader,
	relativePath string,
) (string, error) {
	return loader.LoadSemanticSourceContent(ctx, relativePath)
}

// loadMigrationChunk reads the next whole-line window at offset, escalating
// the window once for a degenerate source whose next line alone exceeds a
// regular chunk. A still-unbounded line is an error, never an endless loop.
func loadMigrationChunk(
	ctx context.Context,
	loader ContextLoader,
	relativePath string,
	offset int64,
) (SemanticChunk, error) {
	chunk, err := loader.LoadSemanticChunk(
		ctx,
		relativePath,
		offset,
		semanticMigrationChunkBytes,
	)
	if err != nil {
		return SemanticChunk{}, err
	}
	if chunk.Content == "" && chunk.More {
		chunk, err = loader.LoadSemanticChunk(
			ctx,
			relativePath,
			offset,
			semanticMigrationOversizedChunkBytes,
		)
		if err != nil {
			return SemanticChunk{}, err
		}
		if chunk.Content == "" && chunk.More {
			return SemanticChunk{}, fmt.Errorf(
				"semantic migration source %q has one line longer than %d bytes at offset %d",
				relativePath,
				semanticMigrationOversizedChunkBytes,
				offset,
			)
		}
	}
	return chunk, nil
}

// nextMigrationTarget returns the first cursor-tracked source, in canonical
// order followed by any extra source in sorted order, that is not finished.
// The empty result with a nil error means the cursor tracks no open file.
func nextMigrationTarget(
	cursor SemanticMigrationCursor,
) (string, SemanticMigrationFileCursor, error) {
	for _, relativePath := range semanticMigrationSources {
		entry, ok := cursor.Files[relativePath]
		if !ok || entry.Finished {
			continue
		}
		return relativePath, entry, nil
	}
	for _, relativePath := range SemanticMigrationCursorExtraSourcePaths(cursor) {
		entry := cursor.Files[relativePath]
		if entry.Finished {
			continue
		}
		return relativePath, entry, nil
	}
	return "", SemanticMigrationFileCursor{}, nil
}

// validateMigrationCursor rejects cursor entries that are neither a canonical
// source nor the staged archive residue, so a mistyped operator cursor fails
// loudly instead of being ignored or drained.
func validateMigrationCursor(cursor SemanticMigrationCursor) error {
	for relativePath := range cursor.Files {
		if !isMigrationSourcePath(relativePath) {
			return fmt.Errorf(
				"semantic migration cursor tracks unknown source path %q",
				relativePath,
			)
		}
	}
	return nil
}

// isMigrationSourcePath reports whether relativePath is a migration source:
// one of the three canonical roots or the staged archive residue.
func isMigrationSourcePath(relativePath string) bool {
	if isCanonicalMigrationSource(relativePath) {
		return true
	}
	return relativePath == SemanticMigrationResidueSourcePath
}

// migrationSectionBefore scans the source file from its start to offset and
// returns the nearest preceding markdown heading, so chunk annotations can
// carry the section each legacy bullet came from even when the heading left
// the window in an earlier run.
func migrationSectionBefore(
	ctx context.Context,
	loader ContextLoader,
	relativePath string,
	offset int64,
) (string, error) {
	position := int64(0)
	section := ""
	for position < offset {
		length := offset - position
		if length > semanticMigrationScanStepBytes {
			length = semanticMigrationScanStepBytes
		}
		chunk, err := loader.LoadSemanticChunk(
			ctx,
			relativePath,
			position,
			length,
		)
		if err != nil {
			return "", err
		}
		if chunk.Content == "" {
			if !chunk.More {
				break
			}
			return "", fmt.Errorf(
				"semantic migration source %q cannot be scanned at offset %d",
				relativePath,
				position,
			)
		}
		section = migrationSectionAfterLines(section, chunk.Content)
		position += int64(len(chunk.Content))
	}
	return section, nil
}

func migrationSectionAfterLines(section string, content string) string {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "#") {
			continue
		}
		section = strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
	}
	return section
}

// migrationChunkLines splits chunk content into whole lines, carrying byte
// offsets in the file and the running section heading.
func migrationChunkLines(
	chunk SemanticChunk,
	initialSection string,
) []migrationSourceLine {
	lines := make([]migrationSourceLine, 0, strings.Count(chunk.Content, "\n")+1)
	position := chunk.Offset
	section := initialSection
	for _, raw := range strings.SplitAfter(chunk.Content, "\n") {
		if raw == "" {
			continue
		}
		trimmed := strings.TrimSpace(raw)
		if strings.HasPrefix(trimmed, "#") {
			section = strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
		}
		lines = append(lines, migrationSourceLine{
			ByteOffset: position,
			Raw:        raw,
			Hash:       SemanticLineHash(raw),
			Section:    section,
		})
		position += int64(len(raw))
	}
	return lines
}

// isLegacyBulletLine mirrors the conversion pass's item heuristic: a bullet
// line with an actual statement. Headings, blank lines, and None
// placeholders are structure and never leave a source file.
func isLegacyBulletLine(raw string) bool {
	line := strings.TrimSpace(raw)
	text, ok := strings.CutPrefix(line, "- ")
	if !ok {
		text, ok = strings.CutPrefix(line, "* ")
	}
	if !ok {
		return false
	}
	text = strings.TrimSpace(text)
	if text == "" || strings.EqualFold(strings.Trim(text, "()."), "none") {
		return false
	}
	return true
}

// renderMigrationChunkBody annotates every consumed line with the source
// reference the ledger line must copy back, so the model can cite the exact
// bytes it converted without ever seeing the rest of the file. Blank lines
// are trimmed so no annotation ends in trailing whitespace.
func renderMigrationChunkBody(
	relativePath string,
	lines []migrationSourceLine,
) string {
	var builder strings.Builder
	for _, line := range lines {
		rendered := fmt.Sprintf(
			"%s:%d sha=%s section=%s | %s",
			relativePath,
			line.ByteOffset,
			line.Hash,
			line.Section,
			strings.TrimRight(line.Raw, "\n"),
		)
		builder.WriteString(strings.TrimRight(rendered, " \t"))
		builder.WriteString("\n")
	}
	return builder.String()
}

// semanticMigrationWriteDirs returns the runtime directories a migration run
// may create files in, which a rollback sweeps for files that have no
// snapshot because the run created them.
func semanticMigrationWriteDirs() []string {
	return []string{semanticTopicsRuntimeDir}
}

// semanticMigrationPolicy scopes the migration run to the conversion ledger
// and the topic files. The source roots are deliberately absent: the run
// cannot read or write them, so the only way content ever leaves a source
// file is this job's own ledger-verified drain.
func semanticMigrationPolicy() filetools.PathAccessPolicy {
	return filetools.PathAccessPolicy{
		ReadPaths:  []string{workingLedgerRuntimePath},
		WritePaths: []string{workingLedgerRuntimePath},
		ReadDirs:   []string{semanticTopicsRuntimeDir},
		WriteDirs:  semanticMigrationWriteDirs(),
	}
}

func isMigrationSourceRuntimePath(runtimePath string) bool {
	for _, source := range semanticRuntimePaths() {
		if source == runtimePath {
			return true
		}
	}
	return runtimePath == semanticMigrationResidueRuntimePath
}

// allMigrationFilesFinished reports whether every tracked source is done;
// finishing the migration is deleting the cursor.
func allMigrationFilesFinished(cursor SemanticMigrationCursor) bool {
	if len(cursor.Files) == 0 {
		return false
	}
	for _, file := range cursor.Files {
		if !file.Finished {
			return false
		}
	}
	return true
}

func shortMigrationHash(hash string) string {
	trimmed := strings.TrimSpace(hash)
	if len(trimmed) > 12 {
		return trimmed[:12]
	}
	return trimmed
}

func (semanticMigrationJob) Build(
	ctx context.Context,
	loader ContextLoader,
) (Spec, error) {
	if loader == nil {
		return Spec{}, fmt.Errorf("context loader is required")
	}
	cursor, err := LoadSemanticMigrationCursor(ctx, loader)
	if err != nil {
		return Spec{}, fmt.Errorf("load semantic migration cursor: %w", err)
	}
	if err := validateMigrationCursor(cursor); err != nil {
		return Spec{}, err
	}
	target, fileCursor, err := nextMigrationTarget(cursor)
	if err != nil {
		return Spec{}, err
	}
	if target == "" {
		return Spec{}, fmt.Errorf(
			"semantic migration cursor %q records no unfinished source file",
			SemanticMigrationCursorArtifactPath,
		)
	}

	// The hash check runs against whole-file content, but only hash values
	// are kept: no semantic root content ever reaches the prompt.
	currentContent, err := loadMigrationSourceContent(ctx, loader, target)
	if err != nil {
		return Spec{}, err
	}
	currentHash := SemanticContentHash(currentContent)
	if fileCursor.Hash != "" && fileCursor.Hash != currentHash {
		return Spec{}, fmt.Errorf(
			"semantic migration source %q changed under the migration "+
				"(cursor hash %s, current %s); reconcile the file before resuming",
			target,
			shortMigrationHash(fileCursor.Hash),
			shortMigrationHash(currentHash),
		)
	}
	if fileCursor.Hash == "" {
		fileCursor.Hash = currentHash
	}

	section, err := migrationSectionBefore(ctx, loader, target, fileCursor.Offset)
	if err != nil {
		return Spec{}, fmt.Errorf("scan migration section context: %w", err)
	}
	chunk, err := loadMigrationChunk(ctx, loader, target, fileCursor.Offset)
	if err != nil {
		return Spec{}, fmt.Errorf("load semantic migration chunk: %w", err)
	}
	if chunk.Content == "" {
		return Spec{}, fmt.Errorf(
			"semantic migration source %q has nothing to read at offset %d",
			target,
			fileCursor.Offset,
		)
	}
	lines := migrationChunkLines(chunk, section)

	ledger, err := loader.LoadWorkingLedger(ctx)
	if err != nil {
		return Spec{}, fmt.Errorf("load working ledger: %w", err)
	}
	sizes, err := loader.LoadSemanticFileSizes(ctx)
	if err != nil {
		return Spec{}, fmt.Errorf("load semantic file sizes: %w", err)
	}
	sourceSize, ok := sizes[target]
	if !ok {
		return Spec{}, fmt.Errorf(
			"semantic migration source %q has no on-disk size",
			target,
		)
	}

	baseline := map[string]string{
		workingLedgerRuntimePath: strings.TrimSpace(ledger),
		semanticMigrationBaselineSizeKeyPrefix + target: strconv.FormatInt(
			sourceSize,
			10,
		),
		semanticMigrationBaselineChunkKeyPrefix + target: SemanticContentHash(
			chunk.Content,
		),
	}

	snapshots, err := snapshotMigrationFiles(ctx, loader, target, sizes)
	if err != nil {
		return Spec{}, err
	}

	return Spec{
		Objective: renderPromptLines(
			"This is a supervised background maintenance job, not a user-facing reply.",
			"Drain exactly one bounded chunk of the legacy semantic store into claims and the conversion ledger.",
			"The annotated chunk below is the only source material you have for this run; you cannot read the source file, and you must not try to.",
			"A legacy bullet leaves the source file only after its conversion or drop is recorded in the ledger by this run.",
		),
		CompletionContract: renderPromptLines(
			"Work only from the annotated migration_chunk section of this prompt.",
			"For every annotated bullet line, append exactly one ledger line to "+workingLedgerRuntimePath+" during this run. A converted bullet records its replacement claim; a bullet with no durable content is dropped with its reason as the quoted description:",
			"YYYY-MM-DDTHH:MMZ run=semantic_memory.migrate migrated \"<short description>\" → c-YYYY-NNNN (semantic/topics/<slug>.md) (source=<path>:<byte> sha=<hash>)",
			"YYYY-MM-DDTHH:MMZ run=semantic_memory.migrate dropped \"<reason>\" (source=<path>:<byte> sha=<hash>)",
			"Copy the source=<path>:<byte> sha=<hash> tail from the annotation verbatim; never alter it; never ledger the same source line twice.",
			"Structure lines (headings, blank lines, None placeholders) are never ledgered and never leave the file.",
			"Write converted claims into semantic/topics/<slug>.md only: one topic file per subject, created or extended with edit_file or apply_patch.",
			"Claim format: `- [c-YYYY-NNNN] <self-contained statement>` followed by an indented `{status: active|conflicted, kind: fact|inference|preference, confidence: <below>, src: , rev: YYYY-MM-DD}` line. Claim IDs are never reused.",
			"Legacy material predates the transcript checkpoint, so every claim keeps its src span empty.",
			"Take confidence from the annotated section: Confirmed Facts → confirmed; Grounded Inferences → likely; every other section → confirmed.",
			"Set kind per meaning: fact for objective states, inference for drawn conclusions, preference for subjective choices.",
			"Retrieve before writing: call embed_search and read the target topic file before deciding new, merge, conflict, or supersede.",
			"Near-duplicate legacy bullets merge into one claim rather than appending twice; the ledger still records one line per merged source line.",
			"Contradicting legacy bullets mark both claims status: conflicted and cross-reference both IDs; never flatten a conflict.",
			"Keep every topic file at or under its cap; split subjects into more slugs rather than exceeding it.",
			"Only append to the ledger: existing ledger lines are immutable, and the run must preserve them as a prefix.",
			"Never read or write a semantic root file, working memory, or the open loops; never remove source text yourself.",
		),
		PromptSections: []agent.PromptSection{
			{
				Name: "migration_chunk",
				Attributes: map[string]string{
					"path":   target,
					"offset": strconv.FormatInt(chunk.Offset, 10),
					"size":   strconv.FormatInt(chunk.Size, 10),
					"more":   strconv.FormatBool(chunk.More),
				},
				Body: renderMigrationChunkBody(target, lines),
			},
			{
				Name: "migration_context",
				Attributes: map[string]string{
					"section": section,
				},
				Body: renderPromptLines(
					fmt.Sprintf(
						"The chunk continues file %q from byte %d; bytes beyond the chunk are not shown and do not exist for this run.",
						target,
						chunk.Offset,
					),
				),
			},
		},
		ExposeTools:        true,
		RequireToolCalling: true,
		AllowedTools:       append([]string(nil), semanticMemoryAllowedTools...),
		Baseline:           baseline,
		FileSnapshots:      snapshots,
		ToolCallPolicy:     semanticMigrationPolicy(),
	}, nil
}

// snapshotMigrationFiles captures the pre-run on-disk state of every path a
// migration run may write, so a failed run can be rolled back byte-for-byte:
// the conversion ledger, every existing topic file under the topics write
// dir, the source file this run drains, and the migration cursor artifact the
// run's completion half rewrites. Topic files the run creates have no
// snapshot here; rollback deletes them by re-listing the write dir
// afterwards. The staged archive residue is a drained source like any other
// and is snapshotted with it: its bullets leave only after their own ledger
// line, so a failed chunk run has to put them back. Only working/archive/
// itself is out of scope, because the completion fold deletes it.
func snapshotMigrationFiles(
	ctx context.Context,
	loader ContextLoader,
	target string,
	sizes map[string]int64,
) (map[string]FileSnapshot, error) {
	snapshots := make(map[string]FileSnapshot, len(sizes)+3)
	read := func(runtimePath string) error {
		raw, existed, err := loader.ReadFileBytes(ctx, relativeMemoryPath(runtimePath))
		if err != nil {
			return fmt.Errorf("snapshot %s: %w", runtimePath, err)
		}
		snapshots[runtimePath] = FileSnapshot{Existed: existed, Content: raw}
		return nil
	}
	if err := read(workingLedgerRuntimePath); err != nil {
		return nil, err
	}
	for relative := range sizes {
		if path.Dir(relative) != relativeMemoryPath(semanticTopicsRuntimeDir) {
			continue
		}
		if err := read(memoryRuntimePrefix + relative); err != nil {
			return nil, err
		}
	}
	if err := read(memoryRuntimePrefix + target); err != nil {
		return nil, err
	}

	// The cursor is an artifact, not a memory file: the artifact store reads
	// and restores it. An empty artifact reads as absent, which matches the
	// cursor loader treating empty content as no migration.
	artifact, err := loader.LoadCognitionArtifact(ctx, SemanticMigrationCursorArtifactPath)
	if err != nil {
		return nil, fmt.Errorf(
			"snapshot %s: %w",
			SemanticMigrationCursorArtifactPath,
			err,
		)
	}
	snapshots[SemanticMigrationCursorArtifactPath] = FileSnapshot{
		Existed: strings.TrimSpace(artifact.Content) != "",
		Content: []byte(artifact.Content),
	}
	return snapshots, nil
}

// ApplyResult applies one migration run in two halves: the chunk half, which
// rolls the store back to the state Build snapshotted on any failure, and the
// completion half, which deliberately does not (its doc comment explains
// why).
func (semanticMigrationJob) ApplyResult(
	ctx context.Context,
	loader ContextLoader,
	output JobOutput,
) (ParsedResult, error) {
	chunk, err := applySemanticMigrationChunk(ctx, loader, output)
	if err != nil {
		return ParsedResult{}, err
	}
	return finishSemanticMigration(ctx, loader, output, chunk)
}

// Rollback returns the run's writable paths to the state Build snapshotted.
// The runner calls it when a run fails before ApplyResult - an engine error,
// a cancelled context, a turn budget that runs out - so a failure leaves no
// partial writes behind and the next run re-reads the store as it was, the
// migration cursor included.
func (semanticMigrationJob) Rollback(
	ctx context.Context,
	loader ContextLoader,
	spec Spec,
) error {
	return restoreConversionFiles(
		ctx,
		loader,
		spec.FileSnapshots,
		semanticMigrationWriteDirs(),
	)
}

// applySemanticMigrationChunk runs the chunk half of one migration run and
// rolls the store back to the state Build snapshotted on every failure, so a
// failed run leaves nothing the next run would re-count: the ledger keeps
// none of the run's lines, the topic files are as they were, and the drained
// source lines are back in the source. The returned error carries both the
// failure and any rollback failure.
func applySemanticMigrationChunk(
	ctx context.Context,
	loader ContextLoader,
	output JobOutput,
) (semanticMigrationChunkResult, error) {
	if loader == nil {
		// Without a loader there is nothing to roll back through.
		return semanticMigrationChunkResult{}, fmt.Errorf("context loader is required")
	}
	chunk, err := drainSemanticMigrationChunk(ctx, loader, output)
	if err != nil {
		return semanticMigrationChunkResult{}, rollbackFailedRun(
			ctx,
			loader,
			output.Spec.FileSnapshots,
			semanticMigrationWriteDirs(),
			err,
		)
	}
	return chunk, nil
}

// semanticMigrationChunkResult is the validated outcome of one migration
// chunk run, carried across the completion boundary so the completion half
// persists exactly what the chunk half computed.
type semanticMigrationChunkResult struct {
	// target is the source file the run drained.
	target string
	// chunkOffset and chunkBytes describe the window the run consumed.
	chunkOffset int64
	chunkBytes  int64
	// ledgerLines and drainedLines count the run's ledger appends and the
	// source lines they drained.
	ledgerLines  int
	drainedLines int
	// removedBytes, newOffset and newSize are the drain's effect on the
	// source file; finished reports the source fully drained.
	removedBytes int64
	newOffset    int64
	newSize      int64
	finished     bool
	// residueLines counts the staged residue's lines, for the fold's ledger
	// line when the run finishes the migration.
	residueLines int
	// cursor is the migration cursor as this run leaves it, for the
	// completion half to persist.
	cursor SemanticMigrationCursor
}

// drainSemanticMigrationChunk is the fallible body of the chunk half: the
// ledger parse and validation, the drain of exactly the ledgered lines, the
// offset and finished computation, the write and size checks, and the
// per-file cursor entry. Its caller wraps every failure in the snapshot
// rollback.
func drainSemanticMigrationChunk(
	ctx context.Context,
	loader ContextLoader,
	output JobOutput,
) (semanticMigrationChunkResult, error) {
	cursor, err := LoadSemanticMigrationCursor(ctx, loader)
	if err != nil {
		return semanticMigrationChunkResult{}, fmt.Errorf(
			"load semantic migration cursor: %w",
			err,
		)
	}
	if err := validateMigrationCursor(cursor); err != nil {
		return semanticMigrationChunkResult{}, err
	}
	target, _, err := nextMigrationTarget(cursor)
	if err != nil {
		return semanticMigrationChunkResult{}, err
	}
	if target == "" {
		return semanticMigrationChunkResult{}, fmt.Errorf(
			"semantic migration cursor %q records no unfinished source file",
			SemanticMigrationCursorArtifactPath,
		)
	}
	baseline := output.Spec.Baseline

	// The chunk is re-read so the drain is validated against the source as
	// it stands, and the Build-time hash pins it against mid-run movement.
	chunk, err := loadMigrationChunk(ctx, loader, target, migrationTargetOffset(cursor, target))
	if err != nil {
		return semanticMigrationChunkResult{}, fmt.Errorf(
			"re-load semantic migration chunk: %w",
			err,
		)
	}
	if expected := baseline[semanticMigrationBaselineChunkKeyPrefix+target]; expected != "" &&
		expected != SemanticContentHash(chunk.Content) {
		return semanticMigrationChunkResult{}, fmt.Errorf(
			"semantic migration source %q moved between Build and ApplyResult",
			target,
		)
	}
	consumedEnd := chunk.Offset + int64(len(chunk.Content))
	lines := migrationChunkLines(chunk, "")

	beforeLedger, ok := baseline[workingLedgerRuntimePath]
	if !ok {
		return semanticMigrationChunkResult{}, fmt.Errorf(
			"spec baseline is missing the pre-run ledger",
		)
	}
	afterLedger, err := loader.LoadWorkingLedger(ctx)
	if err != nil {
		return semanticMigrationChunkResult{}, fmt.Errorf("load post-run working ledger: %w", err)
	}
	afterLedger = strings.TrimSpace(afterLedger)
	if !strings.HasPrefix(afterLedger, beforeLedger) {
		return semanticMigrationChunkResult{}, fmt.Errorf(
			"semantic migration run rewrote existing ledger lines in %s; the ledger is append-only",
			workingLedgerRuntimePath,
		)
	}
	refs, err := parseMigrationLedgerAppends(afterLedger[len(beforeLedger):])
	if err != nil {
		return semanticMigrationChunkResult{}, err
	}

	lineByByte := make(map[int64]migrationSourceLine, len(lines))
	hadBullets := false
	for _, line := range lines {
		lineByByte[line.ByteOffset] = line
		if isLegacyBulletLine(line.Raw) {
			hadBullets = true
		}
	}
	var drainedLines []SemanticDrainLine
	seenBytes := make(map[int64]struct{})
	for _, ref := range refs {
		if ref.SourcePath != target {
			return semanticMigrationChunkResult{}, fmt.Errorf(
				"migration ledger line names source %q, but this run is draining %q",
				ref.SourcePath,
				target,
			)
		}
		line, known := lineByByte[ref.SourceByte]
		if !known || ref.SourceByte < chunk.Offset || ref.SourceByte >= consumedEnd {
			return semanticMigrationChunkResult{}, fmt.Errorf(
				"migration ledger line cites source %s:%d outside this run's chunk",
				target,
				ref.SourceByte,
			)
		}
		if line.Hash != ref.Hash {
			return semanticMigrationChunkResult{}, fmt.Errorf(
				"migration ledger line for %s:%d cites hash %s but the line hashes to %s",
				target,
				ref.SourceByte,
				shortMigrationHash(ref.Hash),
				shortMigrationHash(line.Hash),
			)
		}
		if _, dup := seenBytes[ref.SourceByte]; dup {
			return semanticMigrationChunkResult{}, fmt.Errorf(
				"migration ledger line cites source %s:%d twice",
				target,
				ref.SourceByte,
			)
		}
		seenBytes[ref.SourceByte] = struct{}{}
		if !isLegacyBulletLine(line.Raw) {
			return semanticMigrationChunkResult{}, fmt.Errorf(
				"migration ledger line cites structure line %s:%d, which never leaves the file",
				target,
				ref.SourceByte,
			)
		}
		if ref.Outcome == string(claims.OutcomeMigrated) && !strings.HasPrefix(ref.TargetID, "c-") {
			return semanticMigrationChunkResult{}, fmt.Errorf(
				"migrated ledger line for %s:%d must name its replacement claim",
				target,
				ref.SourceByte,
			)
		}
		drainedLines = append(drainedLines, SemanticDrainLine{
			ByteOffset: ref.SourceByte,
			Hash:       ref.Hash,
		})
	}
	if len(refs) == 0 && hadBullets {
		return semanticMigrationChunkResult{}, fmt.Errorf(
			"semantic migration run ledgered nothing for the bullets of %s at offset %d",
			target,
			chunk.Offset,
		)
	}

	// The fold reports how many residue lines the migration walked, but the
	// drain below removes the residue's bullets before the fold runs, so the
	// count is captured here, from the still-undrained source.
	var residueLines int
	if target == SemanticMigrationResidueSourcePath {
		residueContent, err := loadMigrationSourceContent(ctx, loader, target)
		if err != nil {
			return semanticMigrationChunkResult{}, err
		}
		residueLines = migrationResidueLineCount(residueContent)
	}

	// Nothing leaves the source file until it is ledgered: the drain runs
	// strictly after the ledger lines above were parsed and validated. It
	// removes exactly those ledgered offsets, so a byte-identical line the run
	// did not ledger keeps its bytes even inside the same chunk.
	var removedBytes int64
	newSize := chunk.Size
	if len(drainedLines) > 0 {
		sort.Slice(drainedLines, func(i, j int) bool {
			return drainedLines[i].ByteOffset < drainedLines[j].ByteOffset
		})
		newSize, err = loader.RemoveSemanticLines(ctx, target, drainedLines)
		if err != nil {
			return semanticMigrationChunkResult{}, fmt.Errorf(
				"drain ledgered source lines of %q: %w",
				target,
				err,
			)
		}
		removedBytes = chunk.Size - newSize
	}

	// A legacy bullet the run did not ledger keeps its bytes and stays in
	// front of the cursor: the next run re-reads from the first leftover
	// bullet instead of stranding it behind the offset.
	newOffset := consumedEnd - removedBytes
	remaining := newOffset - chunk.Offset
	if remaining > 0 {
		leftoverChunk, err := loader.LoadSemanticChunk(
			ctx,
			target,
			chunk.Offset,
			remaining,
		)
		if err != nil {
			return semanticMigrationChunkResult{}, fmt.Errorf("re-read drained region: %w", err)
		}
		for _, line := range migrationChunkLines(leftoverChunk, "") {
			if isLegacyBulletLine(line.Raw) {
				newOffset = line.ByteOffset
				break
			}
		}
	}
	finished := !chunk.More && newOffset == newSize

	if err := checkMigrationRunWrites(ctx, loader, output); err != nil {
		return semanticMigrationChunkResult{}, err
	}
	preRunSize, err := migrationBaselineSize(baseline, target)
	if err != nil {
		return semanticMigrationChunkResult{}, err
	}
	if newSize > preRunSize {
		return semanticMigrationChunkResult{}, fmt.Errorf(
			"semantic migration source %q grew under the migration (%d > %d bytes); the drain may only shrink a source file",
			target,
			newSize,
			preRunSize,
		)
	}

	currentContent, err := loadMigrationSourceContent(ctx, loader, target)
	if err != nil {
		return semanticMigrationChunkResult{}, err
	}
	entry := SemanticMigrationFileCursor{
		Offset:   newOffset,
		Hash:     SemanticContentHash(currentContent),
		Finished: finished,
	}
	cursor.Files[target] = entry
	return semanticMigrationChunkResult{
		target:       target,
		chunkOffset:  chunk.Offset,
		chunkBytes:   int64(len(chunk.Content)),
		ledgerLines:  len(refs),
		drainedLines: len(drainedLines),
		removedBytes: removedBytes,
		newOffset:    newOffset,
		newSize:      newSize,
		finished:     finished,
		residueLines: residueLines,
		cursor:       cursor,
	}, nil
}

// finishSemanticMigration is the completion half of one migration run: it
// persists the cursor the chunk half computed - which every run does - and,
// when the cursor's last file is finished, runs the archive fold, the
// checkpoint jump and the rescan before clearing the cursor, then builds the
// run's metadata and summary.
//
// Failures here deliberately do NOT roll the store back to the state Build
// snapshotted: the fold has already deleted working/archive/, so restoring
// the pre-run ledger would drop the fold's ledger lines with nothing left to
// re-derive them from. The unfinished cursor keeps the trigger firing, so the
// completion is retried instead; the comment on the completion block explains
// why each of its steps is idempotent.
func finishSemanticMigration(
	ctx context.Context,
	loader ContextLoader,
	output JobOutput,
	chunk semanticMigrationChunkResult,
) (ParsedResult, error) {
	var jump semanticMigrationCheckpointJump
	var fold semanticMigrationArchiveFold
	var rescan semanticMigrationRescan
	if allMigrationFilesFinished(chunk.cursor) {
		// The completion runs BEFORE the all-finished cursor is persisted, so a
		// failure here - or a process that dies here - leaves a cursor whose
		// last source is still open. The trigger re-fires, the drained source
		// yields an empty chunk, and the completion is retried; persisting the
		// finished cursor first would instead strand the migration
		// finished-but-uncleared, with the fold, the jump and the rescan never
		// done and no run left to do them. Folder, jump and rescan are each
		// idempotent, so the retry writes no second ledger line.
		var err error
		fold, err = completeArchiveFold(ctx, loader, chunk.residueLines)
		if err != nil {
			return ParsedResult{}, err
		}
		jump, err = completeSemanticMigration(ctx, loader)
		if err != nil {
			return ParsedResult{}, err
		}
		rescan = requestSemanticMigrationRescan(ctx, loader)
		if err := SaveSemanticMigrationCursor(ctx, loader, chunk.cursor); err != nil {
			return ParsedResult{}, err
		}
		if err := ClearSemanticMigrationCursor(ctx, loader); err != nil {
			return ParsedResult{}, err
		}
	} else if err := SaveSemanticMigrationCursor(ctx, loader, chunk.cursor); err != nil {
		return ParsedResult{}, err
	}

	metadata := map[string]string{
		"source_file":               chunk.target,
		"chunk_offset":              strconv.FormatInt(chunk.chunkOffset, 10),
		"chunk_bytes":               strconv.FormatInt(chunk.chunkBytes, 10),
		"source_bytes_drained":      strconv.FormatInt(chunk.removedBytes, 10),
		"source_offset_after":       strconv.FormatInt(chunk.newOffset, 10),
		"source_size_after":         strconv.FormatInt(chunk.newSize, 10),
		"source_lines_drained":      strconv.Itoa(chunk.drainedLines),
		"ledger_lines_appended":     strconv.Itoa(chunk.ledgerLines),
		"source_finished":           strconv.FormatBool(chunk.finished),
		"cursor_cleared":            strconv.FormatBool(allMigrationFilesFinished(chunk.cursor)),
		"archive_files_folded":      strconv.Itoa(fold.filesFolded),
		"archive_residue_lines":     strconv.Itoa(fold.residueLines),
		"checkpoint_jump_seq":       strconv.FormatInt(jump.seq, 10),
		"checkpoint_jump_recorded":  strconv.FormatBool(jump.recorded),
		"semantic_rescan_requested": strconv.FormatBool(rescan.requested),
	}
	summary := summarizeMigrationNotes(output.FinalText)
	if fold.unresolved != "" {
		summary = fmt.Sprintf("%s; unresolved: %s", summary, fold.unresolved)
	}
	if jump.unresolved != "" {
		summary = fmt.Sprintf("%s; unresolved: %s", summary, jump.unresolved)
	}
	if rescan.unresolved != "" {
		summary = fmt.Sprintf("%s; unresolved: %s", summary, rescan.unresolved)
	}
	return ParsedResult{
		Summary:  summary,
		Metadata: metadata,
	}, nil
}

// semanticMigrationCheckpointJump records what the migration's completion
// pass did about the steady-state checkpoint, so the run summary reports an
// honest outcome: recorded at a sequence, or why the existing checkpoint was
// left where it was.
type semanticMigrationCheckpointJump struct {
	seq        int64
	recorded   bool
	unresolved string
}

// semanticMigrationArchiveFold records what the migration's completion pass
// did about the staged archive residue and the working archive directory, so
// the run summary reports an honest outcome: the files folded away and
// residue lines walked, or why the archive was left in place.
type semanticMigrationArchiveFold struct {
	filesFolded  int
	residueLines int
	unresolved   string
}

// semanticMigrationRescan records what the migration's completion pass did
// about the embed rescan of the semantic collection, so the run summary
// reports an honest outcome: the rescan was started, or why it was not.
type semanticMigrationRescan struct {
	requested  bool
	unresolved string
}

// completeArchiveFold deletes the staged archive residue and the whole
// working/archive/ directory once the migration has walked the residue, and
// leaves one ledger line naming the fold: how many archive files were folded
// away and how many residue lines were walked. The residue line count is
// passed in because the migration drains the residue's bullets before the
// fold, so the residue file is empty by the time this runs.
//
// A deletion this pass cannot perform — the store's deletion returning an
// error — does not fail the run: the archive stays in place, and the run's
// summary reports that as unresolved.
func completeArchiveFold(
	ctx context.Context,
	loader ContextLoader,
	residueLines int,
) (semanticMigrationArchiveFold, error) {
	fold := semanticMigrationArchiveFold{residueLines: residueLines}
	filesFolded, err := loader.DeleteWorkingArchive(ctx)
	if err != nil {
		fold.unresolved = fmt.Sprintf(
			"archive fold left in place: working archive could not be deleted (%s)",
			err,
		)
	} else {
		fold.filesFolded = filesFolded
	}
	if fold.unresolved != "" {
		return fold, nil
	}
	if fold.filesFolded == 0 {
		// Nothing was left to fold: the archive was already gone, which is what
		// a retried completion sees. Writing the line again would double-count.
		return fold, nil
	}
	if err := appendSemanticMigrationFoldLedgerLine(ctx, loader, fold); err != nil {
		return semanticMigrationArchiveFold{}, err
	}
	return fold, nil
}

// appendSemanticMigrationFoldLedgerLine writes the one ledger line the fold
// leaves behind: the archive copies folded away and the residue lines walked.
func appendSemanticMigrationFoldLedgerLine(
	ctx context.Context,
	loader ContextLoader,
	fold semanticMigrationArchiveFold,
) error {
	line := fmt.Sprintf(
		"%s run=%s archive folded: %d files, %d residue lines walked, working/archive deleted",
		migrationLedgerTimestamp(),
		SemanticMigrationJobType,
		fold.filesFolded,
		fold.residueLines,
	)
	return loader.AppendWorkingLedger(ctx, line)
}

// migrationResidueLineCount counts the non-blank lines of the staged archive
// residue, the same count StageArchiveResidue reported when it staged the
// residue.
func migrationResidueLineCount(content string) int {
	n := 0
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}

// completeSemanticMigration folds the drained backlog into steady state. The
// extraction checkpoint lands on the transcript head: the backlog reached the
// head through this cursor and not through that checkpoint, and leaving the
// checkpoint behind would make the next steady-state pass replay and
// re-convert ground the migration has just covered.
//
// A head this pass cannot record — a head read that fails, or a sequence the
// store's setter has no turn file for — does not fail the run: the existing
// checkpoint stays where it is, and the run's summary reports that as
// unresolved.
func completeSemanticMigration(
	ctx context.Context,
	loader ContextLoader,
) (semanticMigrationCheckpointJump, error) {
	jump := semanticMigrationCheckpointJump{}
	writeJumpLine := true
	seq, _, err := loader.LoadHead(ctx)
	if err != nil {
		jump.unresolved = fmt.Sprintf(
			"semantic extraction checkpoint left where it was: transcript head could not be read (%s)",
			err,
		)
	} else {
		jump.seq = seq
		current, err := loader.LoadSemanticExtractionCheckpoint(ctx)
		switch {
		case err != nil:
			jump.unresolved = fmt.Sprintf(
				"semantic extraction checkpoint left where it was: the existing checkpoint could not be read (%s)",
				err,
			)
		case current.LastExtractedSeq == seq:
			// Already at head, which is what a retried completion sees. The
			// jump already happened, so the run reports it as recorded and
			// writes no second ledger line for it.
			jump.recorded = true
			writeJumpLine = false
		default:
			if _, err := loader.StoreSemanticExtractionCheckpoint(
				ctx,
				SemanticExtractionCheckpoint{LastExtractedSeq: seq},
			); err != nil {
				jump.unresolved = fmt.Sprintf(
					"semantic extraction checkpoint left where it was: transcript head seq %d could not be recorded (%s)",
					seq,
					err,
				)
			} else {
				jump.recorded = true
			}
		}
	}
	// The jump is ledgered whatever the checkpoint did: the migration ended
	// either way, and the line names what the steady-state pass inherits. A
	// checkpoint that was already at head writes no line, so a retried
	// completion leaves the ledger where the first one left it.
	if writeJumpLine {
		if err := appendSemanticMigrationJumpLedgerLine(ctx, loader, jump); err != nil {
			jump.unresolved = fmt.Sprintf(
				"jump ledger line could not be written (%s)",
				err,
			)
			return jump, fmt.Errorf("append migration jump ledger line: %w", err)
		}
	}
	return jump, nil
}

// appendSemanticMigrationJumpLedgerLine writes the one ledger line the
// migration's finish leaves behind: the migration ending, the sequence it
// recorded, and why the jump happened.
func appendSemanticMigrationJumpLedgerLine(
	ctx context.Context,
	loader ContextLoader,
	jump semanticMigrationCheckpointJump,
) error {
	var line string
	if jump.recorded {
		line = fmt.Sprintf(
			"%s run=%s migration finished: semantic extraction checkpoint set to seq %d because the legacy backlog was drained by this migration and not by that checkpoint",
			migrationLedgerTimestamp(),
			SemanticMigrationJobType,
			jump.seq,
		)
	} else {
		line = fmt.Sprintf(
			"%s run=%s migration finished: semantic extraction checkpoint left unrecorded (%s)",
			migrationLedgerTimestamp(),
			SemanticMigrationJobType,
			jump.unresolved,
		)
	}
	return loader.AppendWorkingLedger(ctx, line)
}

// requestSemanticMigrationRescan asks the runtime to start a full re-index of
// the semantic collection so Qdrant reflects the chunk shapes the migration
// produced. The rescan is asynchronous and the migration does not wait on it;
// a loader that cannot start it (for example no embedding service configured,
// or a sync already in flight) does not fail the run — the migration is
// finished either way and no content depends on the index, so the summary
// reports the request as unresolved.
func requestSemanticMigrationRescan(
	ctx context.Context,
	loader ContextLoader,
) semanticMigrationRescan {
	rescan := semanticMigrationRescan{}
	if err := loader.RequestSemanticRescan(ctx); err != nil {
		rescan.unresolved = fmt.Sprintf("semantic rescan not started (%s)", err)
	} else {
		rescan.requested = true
	}
	return rescan
}

// migrationLedgerTimestamp renders the ledger line prefix the conversion
// ledger discipline uses: UTC to the second.
func migrationLedgerTimestamp() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05Z")
}

// migrationTargetOffset returns the tracked offset one source file carries
// in the cursor, or zero when the cursor does not track the file.
func migrationTargetOffset(
	cursor SemanticMigrationCursor,
	relativePath string,
) int64 {
	if file, ok := cursor.Files[relativePath]; ok {
		return file.Offset
	}
	return 0
}

// migrationBaselineSize reads the pre-run on-disk size the migration parked
// in Spec.Baseline under its namespaced key.
func migrationBaselineSize(baseline map[string]string, target string) (int64, error) {
	key := semanticMigrationBaselineSizeKeyPrefix + target
	raw, ok := baseline[key]
	if !ok {
		return 0, fmt.Errorf("spec baseline is missing pre-run size for %q", target)
	}
	size, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || size < 0 {
		return 0, fmt.Errorf("spec baseline pre-run size for %q is not valid: %q", target, raw)
	}
	return size, nil
}

// checkMigrationRunWrites rejects any semantic root the model itself wrote —
// the policy already denies it, so a bypass is a contract failure — and
// applies the ordinary caps to every semantic destination the run wrote.
func checkMigrationRunWrites(
	ctx context.Context,
	loader ContextLoader,
	output JobOutput,
) error {
	for _, written := range successfulWritePaths(output.Messages) {
		if isMigrationSourceRuntimePath(written) {
			return fmt.Errorf(
				"semantic migration run wrote semantic source %s; sources are drained only by the job",
				written,
			)
		}
	}
	return checkSemanticCaps(ctx, loader, output.Messages)
}

// parseMigrationLedgerAppends parses and validates every ledger line this
// run appended. Anything that is not a migration line, or that cites a
// source outside the canonical set, fails the run.
func parseMigrationLedgerAppends(appended string) ([]migrationLedgerRef, error) {
	var refs []migrationLedgerRef
	for _, line := range strings.Split(appended, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		match := migrationLedgerLinePattern.FindStringSubmatch(line)
		if match == nil {
			return nil, fmt.Errorf(
				"migration ledger line %q does not match the migration ledger format",
				compactSemanticMemoryText(line),
			)
		}
		sourceByte, err := strconv.ParseInt(match[migrationLedgerGroupSourceByte], 10, 64)
		if err != nil || sourceByte < 0 {
			return nil, fmt.Errorf(
				"migration ledger line cites an invalid source offset %q",
				match[migrationLedgerGroupSourceByte],
			)
		}
		refs = append(refs, migrationLedgerRef{
			Outcome:     match[migrationLedgerGroupOutcome],
			TargetID:    match[migrationLedgerGroupTarget],
			SourcePath:  match[migrationLedgerGroupSourcePath],
			SourceByte:  sourceByte,
			Hash:        match[migrationLedgerGroupSHA],
			Description: strings.TrimSpace(line),
		})
	}
	return refs, nil
}

func summarizeMigrationNotes(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return compactSemanticMemoryText(line)
		}
	}
	return "Semantic migration chunk drained."
}
