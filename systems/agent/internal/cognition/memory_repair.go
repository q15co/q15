package cognition

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/memory/claims"
	"github.com/q15co/q15/systems/agent/internal/memory/memcheck"
	filetools "github.com/q15co/q15/systems/agent/internal/tools/files"
)

// repairChunkBytes is the default bound on how much of a target one run may
// read.
const repairChunkBytes = 8 * 1024

// Each line carries a digest and an offset in the prompt. Bound that framing
// as well as source bytes so short lines cannot expand an 8 KiB window into
// hundreds of kilobytes of model input.
const repairChunkMaxLines = 128

// repairChunkMaxBytes is the largest window one run may open: the default
// window grows past repairChunkBytes only to fit one first line whole, up to
// this bound. A first line beyond it can never be drained by a repair run,
// so repairSpec answers it with the runtime's own step - adoption for an
// un-named target, a durable refusal for a named one - instead of building a
// run that would fail on the same bytes forever.
const repairChunkMaxBytes = 64 * 1024

// repairLineTooLongError reports a first content line that not even the
// maximum repair window holds whole. A repair run only ever drains whole
// lines, so this line can never exit: repairSpec turns the error into the
// runtime's own answer rather than an erroring run.
type repairLineTooLongError struct {
	lineBytes int
}

func (e *repairLineTooLongError) Error() string {
	return fmt.Sprintf(
		"first content line is %d bytes, over the %d-byte maximum repair window; refused, not truncated",
		e.lineBytes,
		repairChunkMaxBytes,
	)
}

// repairChunkLine is one whole line inside a repair chunk, pinned by its
// byte offset in the pre-run file and by the SHA-256 of its bytes without
// the trailing newline. That is the convention the src=mem:<path>:<offset>
// citation and the prompt's chunk listing use, so the ledger can name
// exactly the line a repair run drained.
type repairChunkLine struct {
	offset int
	text   string
	sum    [sha256.Size]byte
}

// cutRepairChunk slices the head of a repair target into whole lines bounded
// by the window repairChunkWindow reports. The file is its own cursor: the
// chunk is always the head of the file as it stands before the run. A line
// that straddles the window boundary waits for a later run, and a window
// that holds no whole line at all - a first line longer than the maximum
// window - is a repairLineTooLongError, never a silently truncated or empty
// chunk. At end of file a final line without a trailing newline is whole,
// because its last byte is the file's last byte.
func cutRepairChunk(head []byte) ([]repairChunkLine, error) {
	base := repairChunkStart(head)
	window := repairChunkWindow(head, base)
	var lines []repairChunkLine
	start := base
	for start < window && len(lines) < repairChunkMaxLines {
		end := -1
		if newline := bytes.IndexByte(head[start:window], '\n'); newline >= 0 {
			end = start + newline
		} else if window == len(head) {
			end = window
		}
		if end < 0 {
			break
		}
		text := head[start:end]
		lines = append(lines, repairChunkLine{
			offset: start,
			text:   string(text),
			sum:    sha256.Sum256(text),
		})
		start = end + 1
	}
	if len(lines) == 0 && len(head) > base {
		return nil, &repairLineTooLongError{lineBytes: repairFirstLineBytes(head, base)}
	}
	return lines, nil
}

// repairChunkWindow returns the end offset of the window one chunk may span.
// The default window starts repairChunkBytes past the chunk's first line,
// but a first line that straddles that boundary grows the window up to
// repairChunkMaxBytes so the line is still cut whole: lines after the first
// keep waiting for later runs, but the first line itself can never wait -
// no later run would see a different head - so it either fits the grown
// window or repairSpec must answer it without a model run.
func repairChunkWindow(head []byte, base int) int {
	window := min(len(head), base+repairChunkBytes)
	if firstLen := repairFirstLineBytes(head, base); firstLen >= repairChunkBytes &&
		firstLen <= repairChunkMaxBytes {
		if end := base + firstLen + 1; end > window {
			window = min(len(head), end)
		}
	}
	return window
}

// repairFirstLineBytes returns the length of the line starting at base,
// excluding any trailing newline.
func repairFirstLineBytes(head []byte, base int) int {
	if newline := bytes.IndexByte(head[base:], '\n'); newline >= 0 {
		return newline
	}
	return len(head) - base
}

// repairChunkWindowBytes returns the size of the window one chunk of head
// was cut from, measured from the chunk's first line.
func repairChunkWindowBytes(head []byte) int {
	base := repairChunkStart(head)
	return repairChunkWindow(head, base) - base
}

// repairChunkStart returns the byte offset of the first line of head that
// carries content, so a chunk never consists of structure alone while content
// waits below it. Without this, a target whose head is headings and blank
// lines would produce an empty-content chunk on every run: validation would
// ask for no progress, the target would never shrink, and the layer-1 state
// rule would refire on the same bytes forever. It returns 0 when every line is
// structure, which leaves that case to the runtime's adoption step.
func repairChunkStart(head []byte) int {
	start := 0
	for start < len(head) {
		end := len(head)
		if newline := bytes.IndexByte(head[start:], '\n'); newline >= 0 {
			end = start + newline
		}
		if repairContentLine(string(head[start:end])) {
			return start
		}
		start = end + 1
	}
	return 0
}

// repairContentLine reports whether one target line carries content the
// ledger must account for: every content line of the chunk leaves the file
// as a claim or a recorded drop, cited by its offset. Blank lines, markdown
// headings and the exact working-memory "- None" scaffold are structure -
// the model may keep or clear them freely as it drains their neighbours.
func repairContentLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || trimmed == "- None" {
		return false
	}
	return !strings.HasPrefix(trimmed, "#")
}

type repairRecoveryPreparer interface {
	BeginMemoryRepairRecovery(context.Context, map[string]FileSnapshot, string) (string, error)
}

// repairSpec builds the bounded repair run for the first repairable surface
// target, reports a clean surface with (Spec{}, false, nil), or returns a
// handled NoWork spec after runtime-only adoption or refusal. It is small
// and self-contained on purpose: the surface being dirty can mean the
// semantic roots are exactly what is too large to read, so a repair prompt
// never includes the roots, topics, transcript or working memory as prompt
// context. Raw snapshots still cover the complete writable set for recovery.
func repairSpec(
	ctx context.Context,
	loader ContextLoader,
	budgets memoryFileBudgets,
) (Spec, bool, error) {
	files, err := loader.ListMemoryFiles(ctx)
	if err != nil {
		return Spec{}, false, fmt.Errorf("list memory files: %w", err)
	}
	surface := ScanMemorySurface(files, budgets.working, budgets.openLoops)
	if len(surface.Targets) == 0 {
		return Spec{}, false, nil
	}
	// The refusal set is loaded only on a dirty surface, so the ordinary
	// build pays nothing for it, and it is applied by the callers - the
	// state rule and this loop - never inside the stat-only scan. Pruning
	// here is what clears an entry whose path was repaired, edited back
	// inside the schema, or removed: anything the surface no longer lists as
	// repairable has nothing left to suppress.
	refusals, err := loadRepairRefusals(ctx, loader)
	if err != nil {
		return Spec{}, false, fmt.Errorf("load repair refusals: %w", err)
	}
	if pruned, changed := pruneRepairRefusals(refusals, surface); changed {
		refusals = pruned
		if err := storeRepairRefusals(ctx, loader, refusals); err != nil {
			return Spec{}, false, fmt.Errorf("prune repair refusals: %w", err)
		}
	}
	for _, target := range surface.Targets {
		if !claims.ValidMemorySource("mem:" + target.Path + ":0") {
			if err := recordRepairRefusalReason(ctx, loader, refusals, target,
				"unsupported memory citation path: the filename cannot be represented by the repair ledger grammar; rename the file to a portable path before retrying"); err != nil {
				return Spec{}, false, err
			}
			continue
		}
		raw, existed, err := loader.ReadFileBytes(ctx, target.Path)
		if err != nil {
			return Spec{}, false, fmt.Errorf("read repair target %s: %w", target.Path, err)
		}
		if !existed {
			return Spec{}, false, fmt.Errorf(
				"repair target %s vanished between the surface scan and the read",
				target.Path,
			)
		}
		chunk, err := cutRepairChunk(raw)
		if err != nil {
			var tooLong *repairLineTooLongError
			if !errors.As(err, &tooLong) {
				return Spec{}, false, fmt.Errorf("cut repair chunk of %s: %w", target.Path, err)
			}
			if target.Named {
				if err := recordRepairRefusal(ctx, loader, refusals, target, tooLong); err != nil {
					return Spec{}, false, err
				}
				continue
			}
			if err := adoptUnreadableRepairTarget(ctx, loader, target, raw, tooLong); err != nil {
				return Spec{}, false, err
			}
			continue
		}
		if _, refused := refusals[target.Path]; refused {
			// The file changed since the refusal was recorded: a shorter first
			// line, an edit by hand, a merge. Re-cutting every refused target is
			// what stops an entry suppressing a file a run could now drain. The
			// state rule stays read-only, so the build is the only place on this
			// route that may clear it.
			delete(refusals, target.Path)
			if err := storeRepairRefusals(ctx, loader, refusals); err != nil {
				return Spec{}, false, fmt.Errorf(
					"clear repair refusal for %s: %w",
					target.Path,
					err,
				)
			}
		}
		spec, err := buildRepairSpec(ctx, loader, target, raw, chunk)
		if err != nil {
			return Spec{}, false, err
		}
		return spec, true, nil
	}
	return Spec{NoWork: &NoWorkResult{
		Summary: "Memory repair deferred ordinary conversion; no model work was performed.",
		Metadata: map[string]string{
			"memory_repair_no_work":  "true",
			"memory_repair_refusals": strconv.Itoa(len(refusals)),
		},
	}}, true, nil
}

// buildRepairSpec assembles the bounded repair run for the one chosen
// target: the spec pins the pre-run bytes of the target and every writable
// side file, so a failed run rolls back byte-for-byte, and the prompt
// carries exactly the chunk the run must drain.
func buildRepairSpec(
	ctx context.Context,
	loader ContextLoader,
	target MemorySurfaceTarget,
	raw []byte,
	chunk []repairChunkLine,
) (Spec, error) {
	snapshots := map[string]FileSnapshot{
		// The target is pinned to the bytes the chunk was cut from, so the
		// prompt, the rollback restore and the validation all describe the
		// same pre-run state.
		memoryRuntimePrefix + target.Path: {Existed: true, Content: raw},
	}
	if err := snapshotRepairWriteSet(ctx, loader, snapshots); err != nil {
		return Spec{}, err
	}
	baseline := make(map[string]string, len(snapshots))
	for runtimePath, snapshot := range snapshots {
		baseline[runtimePath] = string(snapshot.Content)
	}
	claimIDs, err := conversionClaimIDSummary(ctx, loader, baseline)
	if err != nil {
		return Spec{}, err
	}
	checkpoint, err := loader.LoadSemanticExtractionCheckpoint(ctx)
	if err != nil {
		return Spec{}, fmt.Errorf("load semantic extraction checkpoint: %w", err)
	}

	recoveryID, err := beginRepairRecovery(ctx, loader, snapshots, target.Path)
	if err != nil {
		return Spec{}, err
	}

	return Spec{
		MemoryRecoveryID:   recoveryID,
		Objective:          renderRepairObjective(target),
		CompletionContract: renderTypedRepairContract(target),
		PromptSections: []agent.PromptSection{
			{Name: "claim_id_allocation", Body: claimIDs},
			repairTargetSection(target),
			repairChunkSection(target, chunk, repairChunkWindowBytes(raw)),
			repairRulesSection(target),
		},
		ExposeTools:    true,
		AllowedTools:   []string{"read_file", "embed_search"},
		ToolCallPolicy: repairToolPolicy(target),
		RunAt:          time.Now().UTC(),
		// A repair run consumes no transcript, so it reports the extraction
		// checkpoint it was handed - state accounting still sees the true
		// boundary - while the repair bit the result carries back is what
		// holds the checkpoint in place: the controller skips the checkpoint
		// store for a repair run, so a zero handed checkpoint no longer
		// advances to the run head.
		CheckpointSeq: checkpoint.LastExtractedSeq,
		Baseline:      baseline,
		FileSnapshots: snapshots,
		repair:        true,
		repairTarget:  target,
	}, nil
}

func beginRepairRecovery(
	ctx context.Context,
	loader ContextLoader,
	snapshots map[string]FileSnapshot,
	targetPath string,
) (string, error) {
	preparer, ok := loader.(repairRecoveryPreparer)
	if !ok {
		return "", fmt.Errorf("context loader does not support durable memory repair recovery")
	}
	id, err := preparer.BeginMemoryRepairRecovery(ctx, snapshots, targetPath)
	if err != nil {
		return "", fmt.Errorf("prepare memory repair recovery: %w", err)
	}
	if id == "" {
		return "", fmt.Errorf("prepare memory repair recovery returned no journal identity")
	}
	return id, nil
}

// snapshotRepairWriteSet adds every path a repair run may touch besides the
// target - all three working files, the three semantic roots and the direct
// topic children - to the snapshots with their pre-run bytes, so a failed
// run rolls back byte-for-byte through the existing rollback path.
func snapshotRepairWriteSet(
	ctx context.Context,
	loader ContextLoader,
	snapshots map[string]FileSnapshot,
) error {
	for _, runtimePath := range append(semanticRuntimePaths(), workingMemoryRuntimePath, openLoopsRuntimePath, workingLedgerRuntimePath) {
		if _, done := snapshots[runtimePath]; done {
			continue
		}
		raw, existed, err := loader.ReadFileBytes(ctx, relativeMemoryPath(runtimePath))
		if err != nil {
			return fmt.Errorf("snapshot %s: %w", runtimePath, err)
		}
		snapshots[runtimePath] = FileSnapshot{Existed: existed, Content: raw}
	}
	sizes, err := loader.LoadSemanticFileSizes(ctx)
	if err != nil {
		return fmt.Errorf("list semantic files to snapshot: %w", err)
	}
	for relative := range sizes {
		if path.Dir(relative) != relativeMemoryPath(semanticTopicsRuntimeDir) {
			continue
		}
		runtimePath := memoryRuntimePrefix + relative
		if _, done := snapshots[runtimePath]; done {
			continue
		}
		raw, existed, err := loader.ReadFileBytes(ctx, relative)
		if err != nil {
			return fmt.Errorf("snapshot %s: %w", runtimePath, err)
		}
		snapshots[runtimePath] = FileSnapshot{Existed: existed, Content: raw}
	}
	return nil
}

// repairToolPolicy permits only evidence reads. The runtime owns every mutation
// after the complete typed decision plan has passed validation.
func repairToolPolicy(target MemorySurfaceTarget) filetools.PathAccessPolicy {
	targetRuntime := memoryRuntimePrefix + target.Path
	seen := make(map[string]bool)
	paths := make([]string, 0, 5)
	for _, runtimePath := range append(
		semanticRuntimePaths(),
		workingLedgerRuntimePath,
		targetRuntime,
	) {
		if seen[runtimePath] {
			continue
		}
		seen[runtimePath] = true
		paths = append(paths, runtimePath)
	}
	if target.Path == workingMemoryRelativePath {
		paths = append(paths, openLoopsRuntimePath)
	}
	return filetools.PathAccessPolicy{
		ReadPaths: append([]string(nil), paths...),
		ReadDirs:  []string{semanticTopicsRuntimeDir},
	}
}

// renderRepairObjective states what the target is and what becomes of it.
// A named target over its cap is schema-owned and is drained chunk by chunk
// until it fits; an un-named target is adopted and removed once no content
// line remains.
func renderRepairObjective(target MemorySurfaceTarget) string {
	runtimePath := memoryRuntimePrefix + target.Path
	var fate string
	if target.Named {
		fate = fmt.Sprintf(
			"The repair target is %s, a named memory file of %d bytes, over its %d-byte cap. Drain its content lines into claims or recorded drops chunk by chunk until it fits; the file is schema-owned and is never removed.",
			runtimePath,
			target.Bytes,
			target.Cap,
		)
	} else {
		fate = fmt.Sprintf(
			"The repair target is %s, an un-named memory file of %d bytes that no schema entry owns. Adopt it: every content line becomes a claim or a recorded drop, and once no content line remains the runtime removes the file itself - you never delete it.",
			runtimePath,
			target.Bytes,
		)
	}
	return renderPromptLines(
		"This is a background maintenance job, not a user-facing reply.",
		"This is a memory repair run, the bounded mode of the semantic conversion pass: the memory surface holds a file that does not fit the schema, and this run drains one bounded chunk of it. The file is the cursor: the chunk below is the head of the file as it stands now, and the next run continues from whatever head remains after this one.",
		"This prompt is deliberately self-contained: it does not load the semantic roots, the topic files, the transcript or working memory, because on a dirty surface those may be exactly what is too large to read. Decide only from the chunk below, and use embed_search rather than whole-file reads when you need to compare against existing semantic memory.",
		fate,
	)
}

func repairTargetSection(target MemorySurfaceTarget) agent.PromptSection {
	attributes := map[string]string{
		"path":  memoryRuntimePrefix + target.Path,
		"bytes": strconv.FormatInt(target.Bytes, 10),
		"named": strconv.FormatBool(target.Named),
	}
	if target.Cap > 0 {
		attributes["cap_bytes"] = strconv.Itoa(target.Cap)
	}
	return agent.PromptSection{
		Name:       "memory_repair_target",
		Attributes: attributes,
		Body: renderPromptLines(
			fmt.Sprintf(
				"Address this file with file tools as %s; cite its lines in the ledger with the repository-relative path %s.",
				memoryRuntimePrefix+target.Path,
				target.Path,
			),
		),
	}
}

// repairChunkSection lists the chunk's lines, each with its byte offset in
// the pre-run file and the SHA-256 of its bytes without the trailing
// newline - the address a ledger citation uses to name the line it drained.
// windowBytes is the byte size of the window the chunk was cut from:
// repairChunkBytes by default, grown only to fit an outsized first line.
func repairChunkSection(
	target MemorySurfaceTarget,
	chunk []repairChunkLine,
	windowBytes int,
) agent.PromptSection {
	lines := make([]string, 0, len(chunk))
	for _, line := range chunk {
		description := normalizeExitDescription(line.text)
		row := fmt.Sprintf(
			"offset=%d sha256=%x ledger_description=%s",
			line.offset,
			line.sum,
			strconv.Quote(description),
		)
		if description != line.text {
			row += " raw_line=" + strconv.Quote(line.text)
		}
		lines = append(lines, row)
	}
	if len(lines) == 0 {
		lines = append(
			lines,
			"The target holds no line at all. There is nothing to drain and nothing to cite; leave the store alone and let the runtime remove the file.",
		)
	}
	return agent.PromptSection{
		Name: "repair_chunk",
		Attributes: map[string]string{
			"path":         memoryRuntimePrefix + target.Path,
			"window_bytes": strconv.Itoa(windowBytes),
		},
		Body: strings.Join(lines, "\n"),
	}
}

// repairRulesSection reuses the conversion contract's claim format, storage
// scope and ledger grammar verbatim, and only adds the memory citation form
// of src on top of them.
func repairRulesSection(target MemorySurfaceTarget) agent.PromptSection {
	return agent.PromptSection{
		Name: "repair_rules",
		Body: renderPromptLines(
			conversionClaimFormatRule,
			conversionClaimStorageRule,
			fmt.Sprintf(
				"- Memory citation: a repair exit cites the line it drained as src=mem:%s:<byte-offset> with the offset from the chunk listing (the path is repository-relative, without the runtime prefix). Transcript markers (t<seq>, t<a>..t<b>) keep their meaning for the conversions that have them.",
				target.Path,
			),
			"- Src on claims: a claim a repair run creates cites the drained line in its metadata in place of transcript markers, as src: mem:<relative-path>:<byte-offset>; a claim it merges into keeps every existing source marker and appends that citation.",
		),
	}
}

// applyWrittenRepairResult validates one bounded repair run against the chunk its
// spec drained and then performs the runtime's own step: removing a fully
// adopted un-named target and recording the adoption on the ledger. A failed
// validation rolls the whole run back through the runner's existing
// rollback path, exactly like the steady-state conversion.
func applyWrittenRepairResult(
	ctx context.Context,
	loader ContextLoader,
	output JobOutput,
	budgets memoryFileBudgets,
	written []string,
) (ParsedResult, error) {
	if loader == nil {
		return ParsedResult{}, fmt.Errorf("context loader is required")
	}
	target := output.Spec.repairTarget
	drained, err := validateRepairRun(ctx, loader, output, target, budgets, written)
	if err != nil {
		return ParsedResult{}, rollbackFailedRun(
			ctx,
			loader,
			output.Spec.FileSnapshots,
			err,
		)
	}
	removed, err := adoptDrainedRepairTarget(ctx, loader, target, output.Spec.MemoryRecoveryID)
	if err != nil {
		return ParsedResult{}, rollbackFailedRun(
			ctx,
			loader,
			output.Spec.FileSnapshots,
			err,
		)
	}
	// A target whose repair run just validated holds no refusal anymore: a
	// refused target never reaches a run, so an entry for it can only be a
	// stale one from a concurrent recording, and it clears here.
	if err := clearRepairRefusal(ctx, loader, target.Path); err != nil {
		return ParsedResult{}, rollbackFailedRun(
			ctx,
			loader,
			output.Spec.FileSnapshots,
			err,
		)
	}
	return ParsedResult{
		Summary: fmt.Sprintf("Repaired %d memory lines.", drained),
		Metadata: map[string]string{
			"repair":                "true",
			"repair_target":         target.Path,
			"repair_target_removed": strconv.FormatBool(removed),
			"chunk_lines_exited":    strconv.Itoa(drained),
		},
		CheckpointSeq: output.Spec.CheckpointSeq,
		// The repair bit is what makes the checkpoint hold unconditional:
		// the controller skips the semantic checkpoint store for a repair
		// run, so a zero handed checkpoint cannot advance to the run head.
		Repair: true,
	}, nil
}

// validateRepairRun is the post-run check that makes the repair citation
// mean something: every ledger line this run appended parses and names this
// run, every content line of the chunk left exactly one exit that cites it,
// the drained bytes are gone from the post-run target, the target shrank (or
// an un-named target is gone), converted exits name claims this run wrote,
// and every written file ends inside its cap. It returns the number of
// ledgered exits.
func validateRepairRun(
	ctx context.Context,
	loader ContextLoader,
	output JobOutput,
	target MemorySurfaceTarget,
	budgets memoryFileBudgets,
	written []string,
) (int, error) {
	targetRuntime := memoryRuntimePrefix + target.Path
	snapshot, ok := output.Spec.FileSnapshots[targetRuntime]
	if !ok || !snapshot.Existed {
		return 0, fmt.Errorf(
			"memory repair spec lacks the pre-run snapshot of %s",
			targetRuntime,
		)
	}
	pre := snapshot.Content
	chunk, err := cutRepairChunk(pre)
	if err != nil {
		return 0, fmt.Errorf("re-cut the repair chunk of %s: %w", target.Path, err)
	}
	post, postExisted, err := loader.ReadFileBytes(ctx, target.Path)
	if err != nil {
		return 0, fmt.Errorf("load post-run repair target %s: %w", target.Path, err)
	}
	if !postExisted && target.Named {
		return 0, fmt.Errorf(
			"named repair target %s was removed; named files are schema-owned and only shrink, never disappear",
			target.Path,
		)
	}
	entries, err := appendedWorkingLedgerEntries(
		ctx,
		loader,
		output.Spec.Baseline,
		"memory repair run",
	)
	if err != nil {
		return 0, err
	}

	content := make(map[uint64]string)
	for _, line := range chunk {
		if repairContentLine(line.text) {
			content[uint64(line.offset)] = line.text
		}
	}
	cited := make(map[uint64]claims.LedgerEntry, len(entries))
	for _, entry := range entries {
		if entry.Run != semanticMemoryExtractionJobType {
			return 0, fmt.Errorf(
				"memory repair ledger entry has wrong run %q",
				entry.Run,
			)
		}
		if entry.Outcome != claims.OutcomeConverted && entry.Outcome != claims.OutcomeDropped &&
			!(target.Path == workingMemoryRelativePath && entry.Outcome == claims.OutcomeMoved) {
			return 0, fmt.Errorf(
				"memory repair ledger entry has outcome %q; a repair exit is converted or dropped (or moved for working memory)",
				entry.Outcome,
			)
		}
		citationPath, citationOffset, isCitation := claims.ParseMemorySource(entry.SourceSeq)
		if !isCitation || citationPath != target.Path {
			return 0, fmt.Errorf(
				"memory repair ledger entry src %q is not a citation into %s",
				entry.SourceSeq,
				target.Path,
			)
		}
		offset := citationOffset
		text, inChunk := content[offset]
		if !inChunk {
			return 0, fmt.Errorf(
				"memory repair citation %q does not name a content line of the chunk",
				entry.SourceSeq,
			)
		}
		if _, duplicate := cited[offset]; duplicate {
			return 0, fmt.Errorf(
				"memory repair citation %q drains the same chunk line twice",
				entry.SourceSeq,
			)
		}
		if got, want := normalizeExitDescription(entry.Description),
			normalizeExitDescription(text); got != want {
			return 0, repairDescriptionMismatch(got, want)
		}
		cited[offset] = entry
	}
	for offset, text := range content {
		if _, ok := cited[offset]; !ok {
			return 0, fmt.Errorf(
				"chunk content line at offset %d left no ledger exit: %q",
				offset,
				compactSemanticMemoryText(text),
			)
		}
	}

	if err := validateExitTargets(ctx, loader, entries); err != nil {
		return 0, err
	}
	if err := checkRepairClaimExits(ctx, loader, output.Spec.Baseline, entries, written); err != nil {
		return 0, err
	}

	if err := checkRepairRetention(pre, post, chunk, cited); err != nil {
		return 0, fmt.Errorf("memory repair target %s: %w", target.Path, err)
	}
	if err := checkRepairHeadings(target.Path, pre, post); err != nil {
		return 0, err
	}
	if postExisted && (len(content) > 0 || target.Named) && len(post) >= len(pre) {
		return 0, fmt.Errorf(
			"memory repair run made no progress on %s (%d bytes before, %d after)",
			target.Path, len(pre), len(post),
		)
	}
	if target.Path == workingMemoryRelativePath {
		if err := checkRepairOpenLoops(ctx, loader, output.Spec.Baseline, entries, budgets.openLoops); err != nil {
			return 0, err
		}
	}

	changed, err := repairChangedPaths(ctx, loader, output.Spec.Baseline, written)
	if err != nil {
		return 0, err
	}
	if err := checkRepairCaps(ctx, loader, changed, targetRuntime); err != nil {
		return 0, err
	}
	return len(entries), nil
}

func repairDescriptionMismatch(got, want string) error {
	offset := 0
	for offset < min(len(got), len(want)) && got[offset] == want[offset] {
		offset++
	}
	ledgerByte, sourceByte := "end", "end"
	if offset < len(got) {
		ledgerByte = fmt.Sprintf("0x%02x", got[offset])
	}
	if offset < len(want) {
		sourceByte = fmt.Sprintf("0x%02x", want[offset])
	}
	return fmt.Errorf(
		"memory repair ledger description does not copy the drained chunk line at normalized byte offset %d (ledger=%s source=%s; ledger_bytes=%d source_bytes=%d)",
		offset,
		ledgerByte,
		sourceByte,
		len(got),
		len(want),
	)
}

// checkRepairRetention permits only the cited occurrences and surrounding
// structure inside the covered prefix to leave. The uncovered suffix is pinned
// byte-for-byte, even when it contains text identical to a cited occurrence.
func checkRepairRetention(
	pre, post []byte,
	chunk []repairChunkLine,
	cited map[uint64]claims.LedgerEntry,
) error {
	end := 0
	if len(chunk) > 0 {
		last := chunk[len(chunk)-1]
		end = last.offset + len(last.text)
		if end < len(pre) && pre[end] == '\n' {
			end++
		}
	}
	tail := pre[end:]
	if !bytes.HasSuffix(post, tail) {
		return fmt.Errorf("changed or removed uncited bytes outside the repair chunk")
	}
	remaining := post[:len(post)-len(tail)]
	for offset := 0; offset < end; {
		next := end
		if newline := bytes.IndexByte(pre[offset:end], '\n'); newline >= 0 {
			next = offset + newline + 1
		}
		line := pre[offset:next]
		if _, drained := cited[uint64(offset)]; !drained && bytes.HasPrefix(remaining, line) {
			remaining = remaining[len(line):]
		}
		offset = next
	}
	if len(remaining) != 0 {
		return fmt.Errorf(
			"cited content is still present in the covered prefix or retained bytes were rewritten",
		)
	}
	return nil
}

func checkRepairOpenLoops(
	ctx context.Context,
	loader ContextLoader,
	baseline map[string]string,
	entries []claims.LedgerEntry,
	budget int,
) error {
	raw, _, err := loader.ReadFileBytes(ctx, openLoopsRelativePath)
	if err != nil {
		return fmt.Errorf("load repair open loops: %w", err)
	}
	if len(raw) > effectiveOpenLoopsBudget(budget) {
		return fmt.Errorf(
			"memory repair left open loops over byte budget (%d>%d)",
			len(raw),
			effectiveOpenLoopsBudget(budget),
		)
	}
	before, err := parseOpenLoops(baseline[openLoopsRuntimePath])
	if err != nil {
		return fmt.Errorf("parse pre-run repair open loops: %w", err)
	}
	after, err := parseOpenLoops(string(raw))
	if err != nil {
		return fmt.Errorf("parse post-run repair open loops: %w", err)
	}
	kept := make(map[string]conversionOpenLoop, len(after))
	for _, loop := range after {
		kept[loop.ID] = loop
	}
	existing := make(map[string]bool, len(before))
	for _, loop := range before {
		existing[loop.ID] = true
		if current, found := kept[loop.ID]; !found || current.Item != loop.Item {
			return fmt.Errorf("memory repair changed or removed existing open loop %s", loop.ID)
		}
	}
	moved := make(map[string]bool)
	for _, entry := range entries {
		if entry.Outcome != claims.OutcomeMoved {
			continue
		}
		loop, found := kept[entry.TargetID]
		if !found || existing[entry.TargetID] || moved[entry.TargetID] ||
			loop.Src != entry.SourceSeq {
			return fmt.Errorf(
				"moved repair exit %s requires a new open loop citing %s",
				entry.TargetID,
				entry.SourceSeq,
			)
		}
		intention := workingItemDescription(strings.TrimSpace(entry.Description))
		if normalizeExitDescription(loop.Text) != intention {
			return fmt.Errorf(
				"moved repair exit %s did not preserve its full intention",
				entry.TargetID,
			)
		}
		moved[entry.TargetID] = true
	}
	for id := range kept {
		if !existing[id] && !moved[id] {
			return fmt.Errorf("open loop %s has no moved repair exit", id)
		}
	}
	return nil
}

// checkRepairClaimExits enforces the claim half of the repair bijection:
// every converted exit names a claim in a file this run wrote, every claim
// this run created is named by one such exit, and no pre-existing claim
// vanished - a repair run has no lifecycle outcomes for removals.
func checkRepairClaimExits(
	ctx context.Context,
	loader ContextLoader,
	baseline map[string]string,
	entries []claims.LedgerEntry,
	written []string,
) error {
	claimFiles := map[string]bool{
		semanticFactsRuntimePath:       true,
		semanticPreferencesRuntimePath: true,
		semanticProjectsRuntimePath:    true,
	}
	for runtimePath := range baseline {
		if semanticClaimPath(runtimePath) {
			claimFiles[runtimePath] = true
		}
	}
	for _, runtimePath := range written {
		if semanticClaimPath(runtimePath) {
			claimFiles[runtimePath] = true
		}
	}

	sizes, err := loader.LoadSemanticFileSizes(ctx)
	if err != nil {
		return fmt.Errorf("inventory post-run repair claims: %w", err)
	}
	for relative := range sizes {
		if runtimePath := memoryRuntimePrefix + relative; semanticClaimPath(runtimePath) {
			claimFiles[runtimePath] = true
		}
	}

	preClaims := make(map[string]map[string]bool)
	ordered := make([]string, 0, len(claimFiles))
	for runtimePath := range claimFiles {
		ordered = append(ordered, runtimePath)
	}
	sort.Strings(ordered)
	for _, runtimePath := range ordered {
		raw, present := baseline[runtimePath]
		if !present || raw == "" {
			continue
		}
		parsed, _, err := claims.ParseClaims(raw)
		if err != nil {
			return fmt.Errorf("parse pre-run claims in %s: %w", runtimePath, err)
		}
		sources, err := repairClaimSources(raw)
		if err != nil {
			return fmt.Errorf("read pre-run provenance in %s: %w", runtimePath, err)
		}
		for _, claim := range parsed {
			preClaims[claim.ID] = sources[claim.ID]
		}
	}

	postClaims := make(map[string]string)
	postSources := make(map[string]map[string]bool)
	for _, runtimePath := range ordered {
		raw, existed, err := loader.ReadFileBytes(ctx, relativeMemoryPath(runtimePath))
		if err != nil {
			return fmt.Errorf("load post-run claims in %s: %w", runtimePath, err)
		}
		if !existed {
			continue
		}
		parsed, _, err := claims.ParseClaims(string(raw))
		if err != nil {
			return fmt.Errorf("parse post-run claims in %s: %w", runtimePath, err)
		}
		sources, err := repairClaimSources(string(raw))
		if err != nil {
			return fmt.Errorf("read post-run provenance in %s: %w", runtimePath, err)
		}
		for _, claim := range parsed {
			if previous, duplicate := postClaims[claim.ID]; duplicate {
				return fmt.Errorf(
					"duplicate claim ID %s in %s and %s",
					claim.ID,
					previous,
					runtimePath,
				)
			}
			postClaims[claim.ID] = runtimePath
			postSources[claim.ID] = sources[claim.ID]
		}
	}
	for id, sources := range preClaims {
		if _, kept := postClaims[id]; !kept {
			return fmt.Errorf(
				"memory repair run removed durable claim %s with no lifecycle record",
				id,
			)
		}
		for source := range sources {
			if !postSources[id][source] {
				return fmt.Errorf(
					"memory repair removed existing provenance %s from claim %s",
					source,
					id,
				)
			}
		}
	}

	converted := make(map[string]string) // claim ID -> file the exit names
	for _, entry := range entries {
		if entry.Outcome != claims.OutcomeConverted {
			continue
		}
		runtimePath := memoryRuntimePrefix + entry.TargetPath
		if !accessIncludesPath(written, runtimePath) {
			return fmt.Errorf(
				"converted repair exit %s names %s, which this run did not write",
				entry.TargetID,
				entry.TargetPath,
			)
		}
		where, found := postClaims[entry.TargetID]
		if !found || where != runtimePath {
			return fmt.Errorf(
				"converted repair exit names claim %s in %s, but the run left it nowhere there",
				entry.TargetID,
				entry.TargetPath,
			)
		}
		if previous, duplicate := converted[entry.TargetID]; duplicate &&
			previous != entry.TargetPath {
			return fmt.Errorf(
				"converted repair exits disagree about where claim %s lives",
				entry.TargetID,
			)
		}
		if !postSources[entry.TargetID][entry.SourceSeq] {
			return fmt.Errorf(
				"converted repair claim %s does not cite drained source %s",
				entry.TargetID,
				entry.SourceSeq,
			)
		}
		converted[entry.TargetID] = entry.TargetPath
	}
	history, err := loader.LoadConversionLedgerHistory(ctx)
	if err != nil {
		return fmt.Errorf("load historical repair claim IDs: %w", err)
	}
	historical := make(map[string]bool)
	for _, id := range historicalClaimIDPattern.FindAllString(history+"\n"+baseline[workingLedgerRuntimePath], -1) {
		historical[id] = true
	}
	for id, runtimePath := range postClaims {
		if _, existed := preClaims[id]; existed {
			continue
		}
		if historical[id] {
			return fmt.Errorf("repair claim ID %s was previously used and cannot be reused", id)
		}
		if _, ledgered := converted[id]; !ledgered {
			return fmt.Errorf(
				"claim %s added in %s has no repair ledger exit",
				id,
				relativeMemoryPath(runtimePath),
			)
		}
	}
	return nil
}

func repairClaimSources(raw string) (map[string]map[string]bool, error) {
	byID := make(map[string]map[string]bool)
	for _, item := range workingItems(raw) {
		parsed, _, err := claims.ParseClaims(item)
		if err != nil {
			return nil, err
		}
		if len(parsed) != 1 {
			continue
		}
		fields, err := itemMetadata(item, "status", "kind", "confidence", "src", "rev")
		if err != nil {
			return nil, err
		}
		if !validSourceList(fields["src"]) {
			return nil, fmt.Errorf("invalid claim sources %q", fields["src"])
		}
		sources := make(map[string]bool)
		for _, source := range strings.Split(fields["src"], ",") {
			sources[strings.TrimSpace(source)] = true
		}
		byID[parsed[0].ID] = sources
	}
	return byID, nil
}

func repairChangedPaths(
	ctx context.Context,
	loader ContextLoader,
	baseline map[string]string,
	written []string,
) ([]string, error) {
	sizes, err := loader.LoadSemanticFileSizes(ctx)
	if err != nil {
		return nil, fmt.Errorf("inventory repair result: %w", err)
	}
	paths := make(map[string]bool)
	for runtimePath := range baseline {
		paths[runtimePath] = true
	}
	for relative := range sizes {
		paths[memoryRuntimePrefix+relative] = true
	}
	for _, runtimePath := range written {
		paths[runtimePath] = true
	}
	var changed []string
	for runtimePath := range paths {
		raw, _, err := loader.ReadFileBytes(ctx, relativeMemoryPath(runtimePath))
		if err != nil {
			return nil, fmt.Errorf("inspect repair result %s: %w", runtimePath, err)
		}
		if accessIncludesPath(written, runtimePath) || string(raw) != baseline[runtimePath] {
			changed = append(changed, runtimePath)
		}
	}
	sort.Strings(changed)
	return changed, nil
}

// checkRepairCaps is the repair side of checkSemanticCaps: every file the
// run wrote ends inside its cap, judged with the schema's SemanticCapBytes.
// The target is exempt here because it can still be over its cap while a
// multi-run repair is draining it; the shrink check above is what judges it.
func checkRepairCaps(
	ctx context.Context,
	loader ContextLoader,
	written []string,
	targetRuntime string,
) error {
	for _, runtimePath := range written {
		if runtimePath == targetRuntime {
			continue
		}
		limit := SemanticCapBytes(relativeMemoryPath(runtimePath))
		if limit <= 0 {
			continue
		}
		raw, existed, err := loader.ReadFileBytes(ctx, relativeMemoryPath(runtimePath))
		if err != nil {
			return fmt.Errorf("inspect written file %s: %w", runtimePath, err)
		}
		if !existed {
			continue
		}
		if memcheck.OverLimit(int64(len(raw)), int64(limit)) {
			return fmt.Errorf(
				"memory repair run left written file %s over cap (%d>%d)",
				runtimePath,
				len(raw),
				limit,
			)
		}
	}
	return nil
}

// adoptDrainedRepairTarget is the runtime's own step of the repair, never
// the model's: once an un-named target holds no content line, the runtime
// removes the file and appends exactly one ledger line recording the
// adoption. A named target is schema-owned and stays however small it gets.
func adoptDrainedRepairTarget(
	ctx context.Context,
	loader ContextLoader,
	target MemorySurfaceTarget,
	recoveryID string,
) (bool, error) {
	if target.Named {
		return false, nil
	}
	raw, existed, err := loader.ReadFileBytes(ctx, target.Path)
	if err != nil {
		return false, fmt.Errorf("inspect drained repair target %s: %w", target.Path, err)
	}
	if existed {
		for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
			if repairContentLine(line) {
				return false, nil
			}
		}
	}
	if err := appendRepairAdoptionRecord(ctx, loader, target.Path, repairAdoptionNote, recoveryID); err != nil {
		return false, err
	}
	if existed {
		if err := loader.DeleteFile(ctx, target.Path); err != nil {
			return false, fmt.Errorf("remove adopted memory file %s: %w", target.Path, err)
		}
	}
	return true, nil
}

// repairAdoptionNote is the ledger note for the adoption the runtime records
// when it removes an un-named target whose content lines have all drained.
const repairAdoptionNote = "un-named memory file adopted by a memory repair run: no content line remained, so the runtime removed the file"

// adoptUnreadableRepairTarget is the runtime's own answer to an un-named
// target whose first content line not even the maximum repair window can
// hold: that line can never exit, so no repair run can drain the file and
// none is built. The runtime records exactly one adoption line on the
// ledger - the file-level citation src=mem:<path>:0 with outcome dropped and
// the note naming the reason - and then removes the file itself, without
// which the state rule would fire on the same bytes forever.
func adoptUnreadableRepairTarget(
	ctx context.Context,
	loader ContextLoader,
	target MemorySurfaceTarget,
	raw []byte,
	tooLong *repairLineTooLongError,
) error {
	snapshots := map[string]FileSnapshot{
		memoryRuntimePrefix + target.Path: {Existed: true, Content: raw},
	}
	if err := snapshotRepairWriteSet(ctx, loader, snapshots); err != nil {
		return err
	}
	id, err := beginRepairRecovery(ctx, loader, snapshots, target.Path)
	if err != nil {
		return err
	}
	ownerCtx := withMemoryRecoveryOwner(ctx, id)
	note := fmt.Sprintf(
		"un-named memory file adopted by a memory repair run: first content line is %d bytes, over the %d-byte maximum repair window, so the runtime removed the file unreadable",
		tooLong.lineBytes,
		repairChunkMaxBytes,
	)
	if err := appendRepairAdoptionRecord(ownerCtx, loader, target.Path, note, id); err != nil {
		return recoverRepairAdoption(ctx, loader, id, err)
	}
	if err := loader.DeleteFile(ownerCtx, target.Path); err != nil {
		return recoverRepairAdoption(ctx, loader, id,
			fmt.Errorf("remove unreadable memory file %s: %w", target.Path, err))
	}
	if err := loader.CompleteMemoryRecovery(ctx, id); err != nil {
		return recoverRepairAdoption(ctx, loader, id,
			fmt.Errorf("complete unreadable memory adoption: %w", err))
	}
	return nil
}

func recoverRepairAdoption(
	ctx context.Context,
	loader ContextLoader,
	id string,
	adoptionErr error,
) error {
	cleanupCtx, cancel := memoryRecoveryContext(ctx)
	defer cancel()
	if err := loader.RecoverMemory(cleanupCtx, id); err != nil {
		return fmt.Errorf("%w (adoption rollback also failed: %w)", adoptionErr, err)
	}
	return adoptionErr
}

// appendRepairAdoptionRecord appends the one ledger line that records an
// adoption. Its src is mem:<path>:0: offset zero is the file-level citation,
// not one drained line, because no single line caused the removal - the
// absence of any remaining content line did.
func appendRepairAdoptionRecord(
	ctx context.Context,
	loader ContextLoader,
	relativePath string,
	note string,
	recoveryID string,
) error {
	if recoveryID == "" {
		return fmt.Errorf("repair adoption requires a recovery journal identity")
	}
	note += "; repair_id=" + recoveryID
	ledger, _, err := loader.ReadFileBytes(ctx, workingLedgerRelativePath)
	if err != nil {
		return fmt.Errorf("load working ledger for the adoption record: %w", err)
	}
	history, err := loader.LoadConversionLedgerHistory(ctx)
	if err != nil {
		return fmt.Errorf("load ledger history for adoption: %w", err)
	}
	wanted := claims.LedgerEntry{
		Run:         semanticMemoryExtractionJobType,
		SourceSeq:   "mem:" + relativePath + ":0",
		Outcome:     claims.OutcomeDropped,
		Description: relativePath,
		Note:        note,
	}
	// A retry of the same journal may already have this exact record in the
	// active ledger or an archive. A new journal gives a re-created path its
	// own exit, even when the path and bytes match an earlier adoption.
	for _, content := range []string{string(ledger), history} {
		entries, err := claims.ParseLedger([]byte(content))
		if err != nil {
			return fmt.Errorf("parse adoption ledger history: %w", err)
		}
		for _, entry := range entries {
			if entry.Run == wanted.Run && entry.SourceSeq == wanted.SourceSeq &&
				entry.Outcome == wanted.Outcome && entry.Description == wanted.Description &&
				entry.TargetID == "" && entry.TargetPath == "" && entry.Note == wanted.Note {
				return nil
			}
		}
	}
	entry := claims.FormatLedgerEntry(wanted)
	if _, err := claims.ParseLedgerEntry(strings.TrimSuffix(entry, "\n")); err != nil {
		return fmt.Errorf("format repair adoption record: %w", err)
	}
	after := append([]byte(nil), ledger...)
	if len(after) > 0 && !bytes.HasSuffix(after, []byte("\n")) {
		after = append(after, '\n')
	}
	after = append(after, entry...)
	if err := loader.WriteFileBytes(ctx, workingLedgerRelativePath, after); err != nil {
		return fmt.Errorf("append repair adoption record: %w", err)
	}
	return nil
}
