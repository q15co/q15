package cognition

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/memory/claims"
	"github.com/q15co/q15/systems/agent/internal/memory/memcheck"
	filetools "github.com/q15co/q15/systems/agent/internal/tools/files"
)

// repairChunkBytes bounds how much of a target one run may read.
const repairChunkBytes = 8 * 1024

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
// by repairChunkBytes. The file is its own cursor: the chunk is always the
// head of the file as it stands before the run. A line that straddles the
// window boundary waits for a later run, and a window that holds no whole
// line at all - a first line longer than the window - is an error, never a
// silently truncated or empty chunk. At end of file a final line without a
// trailing newline is whole, because its last byte is the file's last byte.
func cutRepairChunk(head []byte) ([]repairChunkLine, error) {
	base := repairChunkStart(head)
	window := min(len(head), base+repairChunkBytes)
	var lines []repairChunkLine
	start := base
	for start < window {
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
		return nil, fmt.Errorf(
			"no whole line fits the %d-byte repair window; a first line longer than the window is refused, not truncated",
			repairChunkBytes,
		)
	}
	return lines, nil
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

// repairSpec builds the bounded repair run for the first repairable surface
// target, or reports a clean surface with (Spec{}, false, nil). It is small
// and self-contained on purpose: the surface being dirty can mean the
// semantic roots are exactly what is too large to read, so a repair prompt
// never loads the roots, the topics, the transcript or working memory - only
// the stat-only surface scan and one bounded chunk of the target.
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
	target := surface.Targets[0]
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
		return Spec{}, false, fmt.Errorf("cut repair chunk of %s: %w", target.Path, err)
	}

	snapshots := map[string]FileSnapshot{
		// The target is pinned to the bytes the chunk was cut from, so the
		// prompt, the rollback restore and the validation all describe the
		// same pre-run state.
		memoryRuntimePrefix + target.Path: {Existed: true, Content: raw},
	}
	if err := snapshotRepairWriteSet(ctx, loader, snapshots); err != nil {
		return Spec{}, false, err
	}
	baseline := make(map[string]string, len(snapshots))
	for runtimePath, snapshot := range snapshots {
		baseline[runtimePath] = string(snapshot.Content)
	}
	claimIDs, err := conversionClaimIDSummary(ctx, loader, baseline)
	if err != nil {
		return Spec{}, false, err
	}
	checkpoint, err := loader.LoadSemanticExtractionCheckpoint(ctx)
	if err != nil {
		return Spec{}, false, fmt.Errorf("load semantic extraction checkpoint: %w", err)
	}

	return Spec{
		Objective:          renderRepairObjective(target),
		CompletionContract: renderRepairContract(target),
		PromptSections: []agent.PromptSection{
			{Name: "claim_id_allocation", Body: claimIDs},
			repairTargetSection(target),
			repairChunkSection(target, chunk),
			repairRulesSection(target),
		},
		ExposeTools:        true,
		RequireToolCalling: true,
		AllowedTools:       append([]string(nil), semanticMemoryAllowedTools...),
		ToolCallPolicy:     repairToolPolicy(target),
		// A repair run consumes no transcript, so it reports the extraction
		// checkpoint it was handed: the controller then keeps the checkpoint
		// where it was instead of marking unprocessed turns as extracted.
		// The zero-checkpoint corner still advances to the run head, as every
		// job does when it holds no window boundary.
		CheckpointSeq: checkpoint.LastExtractedSeq,
		Baseline:      baseline,
		FileSnapshots: snapshots,
		repair:        true,
		repairTarget:  target,
	}, true, nil
}

// snapshotRepairWriteSet adds every path a repair run may touch besides the
// target - the conversion ledger, the three semantic roots and the direct
// topic children - to the snapshots with their pre-run bytes, so a failed
// run rolls back byte-for-byte through the existing rollback path.
func snapshotRepairWriteSet(
	ctx context.Context,
	loader ContextLoader,
	snapshots map[string]FileSnapshot,
) error {
	for _, runtimePath := range append(semanticRuntimePaths(), workingLedgerRuntimePath) {
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

// repairToolPolicy scopes a repair run's file tools to exactly its writable
// set: the target, the conversion ledger, and the claim files (the semantic
// roots and the direct topic children). Nothing else may be rewritten.
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
	return filetools.PathAccessPolicy{
		ReadPaths:  append([]string(nil), paths...),
		WritePaths: paths,
		ReadDirs:   []string{semanticTopicsRuntimeDir},
		WriteDirs:  []string{semanticTopicsRuntimeDir},
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

// renderRepairContract carries the exit rules: every content line of the
// chunk becomes a claim or a recorded drop with a citation naming the line
// it drained, the lines that left are deleted from the target, an un-named
// target is removed by the runtime once it holds no content line, and
// nothing outside the target is rewritten except the ledger and the claim
// files.
func renderRepairContract(target MemorySurfaceTarget) string {
	lines := []string{
		"Every content line in the repair chunk leaves the target in this run, in exactly one of two ways: rewritten as a claim in a canonical claim file and recorded converted, or recorded dropped with a reason. Markdown headings, blank lines and the exact `- None` scaffold are structure and need no exit.",
		fmt.Sprintf(
			"Append exactly one ledger line per exit to %s: run=%s, src naming the drained line as src=mem:%s:<byte-offset> with the offset from the chunk listing, and the quoted description copying that line's entire text with whitespace collapsed.",
			workingLedgerRuntimePath,
			semanticMemoryExtractionJobType,
			target.Path,
		),
		"Each content line of the chunk is cited exactly once. A cited line must no longer appear in the target when the run ends, and a content line that stays in the chunk range must have no citation: an un-ledgered chunk line, a claim with no ledger line, a citation outside the chunk or a cited line still present all roll the whole run back.",
		"Delete from the target exactly the lines that left, and nothing that did not. Structure lines may leave with the content lines they separated. When the same text appears on several chunk lines, remove every copy you cite and keep no copy whose text went out as drained.",
		fmt.Sprintf(
			"Keep every semantic root file at or under %d bytes and every topic file at or under %d bytes: a written file that ends over its cap fails validation. The target itself is judged by shrinking, not by its cap.",
			semanticRootCapBytes,
			semanticTopicCapBytes,
		),
		"Rewrite nothing outside the target except the ledger and the claim files.",
		"After the tool work completes, reply with one or two short sentences summarizing what left the target.",
	}
	if target.Named {
		lines = append(
			lines,
			"The target is schema-owned: leave the file in place even if this run drains it to headings alone. Later runs keep shrinking it from the new head.",
		)
	} else {
		lines = append(lines,
			"If this run leaves the target holding no content line, the runtime removes it afterwards and appends one ledger line recording the adoption (outcome dropped, src=mem:"+target.Path+":0). Removing the file is the runtime's step, never a tool call of yours.",
		)
	}
	return renderPromptLines(lines...)
}

// repairTargetSection binds the two spellings of the target path: the
// runtime path the file tools use and the repository-relative path the
// ledger citation uses.
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
func repairChunkSection(
	target MemorySurfaceTarget,
	chunk []repairChunkLine,
) agent.PromptSection {
	lines := make([]string, 0, len(chunk))
	for _, line := range chunk {
		lines = append(lines, fmt.Sprintf(
			"offset=%d sha256=%x | %s",
			line.offset,
			line.sum,
			line.text,
		))
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
			"window_bytes": strconv.Itoa(repairChunkBytes),
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
			conversionLedgerFormatRule,
			fmt.Sprintf(
				"- Memory citation: a repair exit cites the line it drained as src=mem:%s:<byte-offset> with the offset from the chunk listing (the path is repository-relative, without the runtime prefix). Transcript markers (t<seq>, t<a>..t<b>) keep their meaning for the conversions that have them.",
				target.Path,
			),
			"- Src on claims: a claim a repair run creates cites the drained line in its metadata in place of transcript markers, as src: mem:<relative-path>:<byte-offset>; a claim it merges into keeps every existing source marker and appends that citation.",
		),
	}
}

// applyRepairResult validates one bounded repair run against the chunk its
// spec drained and then performs the runtime's own step: removing a fully
// adopted un-named target and recording the adoption on the ledger. A failed
// validation rolls the whole run back through the runner's existing
// rollback path, exactly like the steady-state conversion.
func applyRepairResult(
	ctx context.Context,
	loader ContextLoader,
	output JobOutput,
) (ParsedResult, error) {
	if loader == nil {
		return ParsedResult{}, fmt.Errorf("context loader is required")
	}
	target := output.Spec.repairTarget
	drained, err := validateRepairRun(ctx, loader, output, target)
	if err != nil {
		return ParsedResult{}, rollbackFailedRun(
			ctx,
			loader,
			output.Spec.FileSnapshots,
			err,
		)
	}
	removed, err := adoptDrainedRepairTarget(ctx, loader, target)
	if err != nil {
		return ParsedResult{}, rollbackFailedRun(
			ctx,
			loader,
			output.Spec.FileSnapshots,
			err,
		)
	}
	return ParsedResult{
		Summary: summarizeSemanticMemoryNotes(output.FinalText),
		Metadata: map[string]string{
			"repair":                "true",
			"repair_target":         target.Path,
			"repair_target_removed": strconv.FormatBool(removed),
			"chunk_lines_exited":    strconv.Itoa(drained),
		},
		CheckpointSeq: output.Spec.CheckpointSeq,
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

	content := make(map[int]string)
	for _, line := range chunk {
		if repairContentLine(line.text) {
			content[line.offset] = line.text
		}
	}
	cited := make(map[int]claims.LedgerEntry, len(entries))
	for _, entry := range entries {
		if entry.Run != semanticMemoryExtractionJobType {
			return 0, fmt.Errorf(
				"memory repair ledger entry has wrong run %q",
				entry.Run,
			)
		}
		if entry.Outcome != claims.OutcomeConverted && entry.Outcome != claims.OutcomeDropped {
			return 0, fmt.Errorf(
				"memory repair ledger entry has outcome %q; a repair exit is converted or dropped",
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
		offset := int(citationOffset)
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
			return 0, fmt.Errorf(
				"memory repair ledger description %q does not copy the drained chunk line %q",
				entry.Description,
				compactSemanticMemoryText(text),
			)
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
	written := successfulWritePaths(output.Messages)
	if err := checkRepairClaimExits(ctx, loader, output.Spec.Baseline, entries, written); err != nil {
		return 0, err
	}

	if postExisted {
		postLines := make(map[string]bool)
		for _, line := range strings.Split(strings.TrimSuffix(string(post), "\n"), "\n") {
			postLines[line] = true
		}
		for offset, entry := range cited {
			line := content[offset]
			if postLines[line] {
				return 0, fmt.Errorf(
					"chunk line drained by %s is still present in %s: %q",
					entry.SourceSeq,
					target.Path,
					compactSemanticMemoryText(line),
				)
			}
		}
		if len(content) > 0 && len(post) >= len(pre) {
			return 0, fmt.Errorf(
				"memory repair run made no progress on %s (%d bytes before, %d after)",
				target.Path,
				len(pre),
				len(post),
			)
		}
	}

	if err := checkRepairCaps(ctx, loader, written, targetRuntime); err != nil {
		return 0, err
	}
	return len(entries), nil
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

	preClaims := make(map[string]bool)
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
		for _, claim := range parsed {
			preClaims[claim.ID] = true
		}
	}

	postClaims := make(map[string]string)
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
		}
	}
	for id := range preClaims {
		if _, kept := postClaims[id]; !kept {
			return fmt.Errorf(
				"memory repair run removed durable claim %s with no lifecycle record",
				id,
			)
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
		converted[entry.TargetID] = entry.TargetPath
	}
	for id, runtimePath := range postClaims {
		if preClaims[id] {
			continue
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
		if err := loader.DeleteFile(ctx, target.Path); err != nil {
			return false, fmt.Errorf("remove adopted memory file %s: %w", target.Path, err)
		}
	}
	if err := appendRepairAdoptionRecord(ctx, loader, target.Path); err != nil {
		return false, err
	}
	return true, nil
}

// appendRepairAdoptionRecord appends the one ledger line that records an
// adoption. Its src is mem:<path>:0: offset zero is the file-level citation,
// not one drained line, because no single line caused the removal - the
// absence of any remaining content line did.
func appendRepairAdoptionRecord(
	ctx context.Context,
	loader ContextLoader,
	relativePath string,
) error {
	ledger, _, err := loader.ReadFileBytes(ctx, workingLedgerRelativePath)
	if err != nil {
		return fmt.Errorf("load working ledger for the adoption record: %w", err)
	}
	entry := claims.FormatLedgerEntry(claims.LedgerEntry{
		Run:         semanticMemoryExtractionJobType,
		SourceSeq:   "mem:" + relativePath + ":0",
		Outcome:     claims.OutcomeDropped,
		Description: relativePath,
		Note:        "un-named memory file adopted by a memory repair run: no content line remained, so the runtime removed the file",
	})
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
