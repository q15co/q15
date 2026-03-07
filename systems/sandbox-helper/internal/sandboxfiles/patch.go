package sandboxfiles

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

const (
	patchBeginMarker      = "*** Begin Patch"
	patchEndMarker        = "*** End Patch"
	patchAddFileMarker    = "*** Add File: "
	patchDeleteFileMarker = "*** Delete File: "
	patchUpdateFileMarker = "*** Update File: "
	patchMoveToMarker     = "*** Move to: "
	patchEndOfFileMarker  = "*** End of File"
)

type patchFileOp struct {
	kind     string
	path     string
	moveTo   string
	addLines []string
	hunks    []patchHunk
}

type patchHunk struct {
	header    string
	lines     []patchLine
	endOfFile bool
}

type patchLine struct {
	kind byte
	text string
}

func parsePatch(input string) ([]patchFileOp, error) {
	input = normalizeLineEndings(input)
	if strings.TrimSpace(input) == "" {
		return nil, fmt.Errorf("patch is required")
	}

	lines := strings.Split(input, "\n")
	i := 0
	for i < len(lines) && lines[i] == "" {
		i++
	}
	if i >= len(lines) || lines[i] != patchBeginMarker {
		return nil, fmt.Errorf("patch must begin with %q", patchBeginMarker)
	}
	i++

	var ops []patchFileOp
	for i < len(lines) {
		line := lines[i]
		switch {
		case line == "":
			i++
			continue
		case line == patchEndMarker:
			for j := i + 1; j < len(lines); j++ {
				if lines[j] != "" {
					return nil, fmt.Errorf("unexpected trailing content after %q", patchEndMarker)
				}
			}
			if len(ops) == 0 {
				return nil, fmt.Errorf("patch must contain at least one file operation")
			}
			return ops, nil
		case strings.HasPrefix(line, patchAddFileMarker):
			op, next, err := parseAddFile(lines, i)
			if err != nil {
				return nil, err
			}
			ops = append(ops, op)
			i = next
		case strings.HasPrefix(line, patchDeleteFileMarker):
			op := patchFileOp{
				kind: "delete",
				path: strings.TrimSpace(strings.TrimPrefix(line, patchDeleteFileMarker)),
			}
			if op.path == "" {
				return nil, fmt.Errorf("delete file path is required")
			}
			ops = append(ops, op)
			i++
		case strings.HasPrefix(line, patchUpdateFileMarker):
			op, next, err := parseUpdateFile(lines, i)
			if err != nil {
				return nil, err
			}
			ops = append(ops, op)
			i = next
		default:
			return nil, fmt.Errorf("unexpected patch line %q", line)
		}
	}
	return nil, fmt.Errorf("patch missing %q", patchEndMarker)
}

func parseAddFile(lines []string, start int) (patchFileOp, int, error) {
	op := patchFileOp{
		kind: "add",
		path: strings.TrimSpace(strings.TrimPrefix(lines[start], patchAddFileMarker)),
	}
	if op.path == "" {
		return patchFileOp{}, 0, fmt.Errorf("add file path is required")
	}

	i := start + 1
	for i < len(lines) {
		line := lines[i]
		if line == patchEndMarker || strings.HasPrefix(line, "*** ") {
			break
		}
		if len(line) == 0 || line[0] != '+' {
			return patchFileOp{}, 0, fmt.Errorf("add file lines must start with '+'")
		}
		op.addLines = append(op.addLines, line[1:])
		i++
	}
	if len(op.addLines) == 0 {
		return patchFileOp{}, 0, fmt.Errorf("add file must include at least one '+' line")
	}
	return op, i, nil
}

func parseUpdateFile(lines []string, start int) (patchFileOp, int, error) {
	op := patchFileOp{
		kind: "update",
		path: strings.TrimSpace(strings.TrimPrefix(lines[start], patchUpdateFileMarker)),
	}
	if op.path == "" {
		return patchFileOp{}, 0, fmt.Errorf("update file path is required")
	}

	i := start + 1
	if i < len(lines) && strings.HasPrefix(lines[i], patchMoveToMarker) {
		op.moveTo = strings.TrimSpace(strings.TrimPrefix(lines[i], patchMoveToMarker))
		if op.moveTo == "" {
			return patchFileOp{}, 0, fmt.Errorf("move destination is required")
		}
		i++
	}

	for i < len(lines) {
		line := lines[i]
		switch {
		case line == patchEndMarker || strings.HasPrefix(line, "*** Add File: ") ||
			strings.HasPrefix(line, "*** Delete File: ") ||
			strings.HasPrefix(line, "*** Update File: "):
			if len(op.hunks) == 0 && op.moveTo == "" {
				return patchFileOp{}, 0, fmt.Errorf("update file must include at least one hunk")
			}
			return op, i, nil
		case strings.HasPrefix(line, "@@"):
			hunk, next, err := parseHunk(lines, i)
			if err != nil {
				return patchFileOp{}, 0, err
			}
			op.hunks = append(op.hunks, hunk)
			i = next
		case line == "":
			i++
		default:
			return patchFileOp{}, 0, fmt.Errorf("unexpected update-file line %q", line)
		}
	}
	if len(op.hunks) == 0 && op.moveTo == "" {
		return patchFileOp{}, 0, fmt.Errorf("update file must include at least one hunk")
	}
	return op, i, nil
}

func parseHunk(lines []string, start int) (patchHunk, int, error) {
	hunk := patchHunk{
		header: strings.TrimSpace(strings.TrimPrefix(lines[start], "@@")),
	}

	i := start + 1
	for i < len(lines) {
		line := lines[i]
		switch {
		case line == patchEndOfFileMarker:
			hunk.endOfFile = true
			i++
		case line == patchEndMarker || strings.HasPrefix(line, "*** Add File: ") ||
			strings.HasPrefix(line, "*** Delete File: ") ||
			strings.HasPrefix(line, "*** Update File: ") ||
			strings.HasPrefix(line, "@@"):
			if len(hunk.lines) == 0 {
				return patchHunk{}, 0, fmt.Errorf("hunk must include at least one line")
			}
			return hunk, i, nil
		case line != "" && (line[0] == ' ' || line[0] == '+' || line[0] == '-'):
			hunk.lines = append(hunk.lines, patchLine{
				kind: line[0],
				text: line[1:],
			})
			i++
		default:
			return patchHunk{}, 0, fmt.Errorf("invalid hunk line %q", line)
		}
	}
	if len(hunk.lines) == 0 {
		return patchHunk{}, 0, fmt.Errorf("hunk must include at least one line")
	}
	return hunk, i, nil
}

func buildPatchPlan(
	cfg Settings,
	ops []patchFileOp,
) (
	map[string]desiredFileState,
	map[string]fileSnapshot,
	string,
	[]string,
	string,
	error,
) {
	desired := make(map[string]desiredFileState)
	snapshots := make(map[string]fileSnapshot)
	reserved := make(map[string]string)
	var changedFiles []string
	var diffSections []string

	for _, op := range ops {
		source, err := resolvePath(cfg, op.path)
		if err != nil {
			return nil, nil, "", nil, "", err
		}
		sourceKey := stateKey(source)
		if prior, ok := reserved[sourceKey]; ok {
			return nil, nil, "", nil, "", fmt.Errorf(
				"path %s is referenced by both %s and %s",
				source.containerPath,
				prior,
				op.kind,
			)
		}
		reserved[sourceKey] = op.kind

		switch op.kind {
		case "add":
			existing, err := snapshotPath(source)
			if err != nil {
				return nil, nil, "", nil, "", err
			}
			if existing.exists {
				return nil, nil, "", nil, "", fmt.Errorf(
					"cannot add existing file %s",
					source.containerPath,
				)
			}
			snapshots[sourceKey] = existing
			newText := strings.Join(op.addLines, "\n")
			desired[sourceKey] = desiredFileState{
				resolved:    source,
				shouldExist: true,
				raw:         []byte(newText),
			}
			changedFiles = append(changedFiles, source.containerPath)
			diff, _ := compactDiff("", normalizeLineEndings(newText))
			diffSections = append(diffSections, diffHeader(source.containerPath, "")+"\n"+diff)
		case "delete":
			snapshot, err := snapshotTextPath(source)
			if err != nil {
				return nil, nil, "", nil, "", err
			}
			if !snapshot.snapshot.exists {
				return nil, nil, "", nil, "", fmt.Errorf(
					"cannot delete missing file %s",
					source.containerPath,
				)
			}
			snapshots[sourceKey] = snapshot.snapshot
			desired[sourceKey] = desiredFileState{
				resolved:    source,
				shouldExist: false,
			}
			changedFiles = append(changedFiles, source.containerPath)
			diff, _ := compactDiff(normalizeLineEndings(snapshot.text.text), "")
			diffSections = append(diffSections, diffHeader(source.containerPath, "")+"\n"+diff)
		case "update":
			snapshot, err := snapshotTextPath(source)
			if err != nil {
				return nil, nil, "", nil, "", err
			}
			if !snapshot.snapshot.exists {
				return nil, nil, "", nil, "", fmt.Errorf(
					"cannot update missing file %s",
					source.containerPath,
				)
			}

			dest := source
			moveSummary := ""
			if op.moveTo != "" {
				dest, err = resolvePath(cfg, op.moveTo)
				if err != nil {
					return nil, nil, "", nil, "", err
				}
				if dest.rootContainer != source.rootContainer {
					return nil, nil, "", nil, "", fmt.Errorf(
						"move destination must stay within the same root",
					)
				}
				destKey := stateKey(dest)
				if destKey != sourceKey {
					if prior, ok := reserved[destKey]; ok {
						return nil, nil, "", nil, "", fmt.Errorf(
							"move destination %s conflicts with %s",
							dest.containerPath,
							prior,
						)
					}
					reserved[destKey] = "move-destination"
					destSnapshot, err := snapshotPath(dest)
					if err != nil {
						return nil, nil, "", nil, "", err
					}
					if destSnapshot.exists {
						return nil, nil, "", nil, "", fmt.Errorf(
							"move destination already exists: %s",
							dest.containerPath,
						)
					}
					snapshots[destKey] = destSnapshot
					moveSummary = fmt.Sprintf(
						"rename %s -> %s",
						source.containerPath,
						dest.containerPath,
					)
				}
			}

			updatedNormalized, err := applyHunks(normalizeLineEndings(snapshot.text.text), op.hunks)
			if err != nil {
				return nil, nil, "", nil, "", fmt.Errorf("%s: %w", source.containerPath, err)
			}
			finalText := restoreLineEndings(updatedNormalized, snapshot.text.lineEnding)
			finalRaw := append(append([]byte(nil), snapshot.text.bom...), []byte(finalText)...)

			snapshots[sourceKey] = snapshot.snapshot
			if stateKey(dest) == sourceKey {
				desired[sourceKey] = desiredFileState{
					resolved:    source,
					shouldExist: true,
					raw:         finalRaw,
				}
				changedFiles = append(changedFiles, source.containerPath)
				diff, _ := compactDiff(normalizeLineEndings(snapshot.text.text), updatedNormalized)
				if moveSummary != "" {
					diffSections = append(
						diffSections,
						diffHeader(
							dest.containerPath,
							source.containerPath,
						)+"\n"+moveSummary+"\n"+diff,
					)
				} else {
					diffSections = append(diffSections, diffHeader(source.containerPath, "")+"\n"+diff)
				}
			} else {
				destKey := stateKey(dest)
				desired[sourceKey] = desiredFileState{
					resolved:    source,
					shouldExist: false,
				}
				desired[destKey] = desiredFileState{
					resolved:    dest,
					shouldExist: true,
					raw:         finalRaw,
				}
				changedFiles = append(changedFiles, source.containerPath, dest.containerPath)
				diff, _ := compactDiff(normalizeLineEndings(snapshot.text.text), updatedNormalized)
				diffSections = append(diffSections, diffHeader(dest.containerPath, source.containerPath)+"\n"+moveSummary+"\n"+diff)
			}
		default:
			return nil, nil, "", nil, "", fmt.Errorf("unsupported patch op %q", op.kind)
		}
	}

	changedFiles = uniqueStrings(changedFiles)
	summary := fmt.Sprintf("applied patch to %d file(s)", len(changedFiles))
	return desired, snapshots, strings.Join(diffSections, "\n\n"), changedFiles, summary, nil
}

type textSnapshot struct {
	snapshot fileSnapshot
	text     textFile
}

func snapshotPath(resolved resolvedPath) (fileSnapshot, error) {
	root, err := os.OpenRoot(resolved.rootHostDir)
	if err != nil {
		return fileSnapshot{}, fmt.Errorf("open root: %w", err)
	}
	defer root.Close()

	info, err := root.Stat(resolved.rel)
	switch {
	case err == nil:
		if !info.Mode().IsRegular() {
			return fileSnapshot{}, fmt.Errorf(
				"path is not a regular file: %s",
				resolved.containerPath,
			)
		}
		raw, err := root.ReadFile(resolved.rel)
		if err != nil {
			return fileSnapshot{}, fmt.Errorf("read file: %w", err)
		}
		return fileSnapshot{resolved: resolved, exists: true, raw: raw}, nil
	case os.IsNotExist(err):
		return fileSnapshot{resolved: resolved, exists: false}, nil
	default:
		return fileSnapshot{}, fmt.Errorf("stat file: %w", err)
	}
}

func snapshotTextPath(resolved resolvedPath) (textSnapshot, error) {
	snapshot, err := snapshotPath(resolved)
	if err != nil {
		return textSnapshot{}, err
	}
	if !snapshot.exists {
		return textSnapshot{snapshot: snapshot}, nil
	}
	tf, err := decodeTextFile(snapshot.raw)
	if err != nil {
		return textSnapshot{}, fmt.Errorf("%s: %w", resolved.containerPath, err)
	}
	return textSnapshot{snapshot: snapshot, text: tf}, nil
}

func applyHunks(text string, hunks []patchHunk) (string, error) {
	if len(hunks) == 0 {
		return text, nil
	}

	hadTrailingNewline := strings.HasSuffix(text, "\n")
	lines := splitLogicalLines(text)
	forceNoTrailingNewline := false
	searchStart := 0
	for _, hunk := range hunks {
		oldChunk, newChunk := materializeHunk(hunk)
		if len(oldChunk) == 0 {
			return "", fmt.Errorf("hunk must include context or removed lines")
		}
		idx, matches := findUniqueChunk(lines, oldChunk, searchStart)
		if idx < 0 {
			return "", fmt.Errorf("hunk did not match file contents")
		}
		if matches > 1 {
			return "", fmt.Errorf("hunk matched multiple locations")
		}
		lines = replaceChunk(lines, idx, idx+len(oldChunk), newChunk)
		searchStart = idx + len(newChunk)
		if hunk.endOfFile {
			forceNoTrailingNewline = true
		}
	}

	out := strings.Join(lines, "\n")
	if !forceNoTrailingNewline && hadTrailingNewline {
		out += "\n"
	}
	return out, nil
}

func materializeHunk(hunk patchHunk) ([]string, []string) {
	oldChunk := make([]string, 0, len(hunk.lines))
	newChunk := make([]string, 0, len(hunk.lines))
	for _, line := range hunk.lines {
		switch line.kind {
		case ' ':
			oldChunk = append(oldChunk, line.text)
			newChunk = append(newChunk, line.text)
		case '-':
			oldChunk = append(oldChunk, line.text)
		case '+':
			newChunk = append(newChunk, line.text)
		}
	}
	return oldChunk, newChunk
}

func findUniqueChunk(lines, chunk []string, start int) (int, int) {
	if len(chunk) == 0 {
		return -1, 0
	}
	if start < 0 {
		start = 0
	}
	matchFrom := func(begin int) (int, int) {
		matches := 0
		matchIdx := -1
		for i := begin; i+len(chunk) <= len(lines); i++ {
			if equalChunk(lines[i:i+len(chunk)], chunk) {
				matches++
				matchIdx = i
			}
		}
		return matchIdx, matches
	}

	matchIdx, matches := matchFrom(start)
	if matches == 0 && start > 0 {
		return matchFrom(0)
	}
	return matchIdx, matches
}

func equalChunk(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func replaceChunk(lines []string, start, end int, replacement []string) []string {
	out := make([]string, 0, len(lines)-(end-start)+len(replacement))
	out = append(out, lines[:start]...)
	out = append(out, replacement...)
	out = append(out, lines[end:]...)
	return out
}

func stateKey(resolved resolvedPath) string {
	return resolved.rootHostDir + "\x00" + resolved.rel
}

func diffHeader(path string, from string) string {
	if from == "" {
		return "=== " + path + " ==="
	}
	return fmt.Sprintf("=== %s (from %s) ===", path, from)
}

func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
