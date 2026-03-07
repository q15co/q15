// Package sandboxfiles implements helper-side rooted file operations for sandbox workspaces.
package sandboxfiles

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	sandboxcontract "github.com/q15co/q15/libs/sandbox-contract"
)

const (
	defaultReadLimitLines = 400
	maxReadLimitLines     = 400
	maxReadBytes          = 16 * 1024
	diffContextLines      = 3
)

// Settings describes the rooted host/container directory mapping used by helper file ops.
type Settings struct {
	WorkspaceHostDir string
	WorkspaceDir     string
	MemoryHostDir    string
	MemoryDir        string
}

type resolvedPath struct {
	rootHostDir   string
	rootContainer string
	rel           string
	containerPath string
}

type textFile struct {
	raw         []byte
	bom         []byte
	text        string
	lineEnding  string
	wasReadable bool
}

type fileSnapshot struct {
	resolved resolvedPath
	exists   bool
	raw      []byte
}

type desiredFileState struct {
	resolved    resolvedPath
	shouldExist bool
	raw         []byte
}

// ReadFile performs a rooted text file read with optional paging.
func ReadFile(
	cfg Settings,
	req sandboxcontract.ReadFileRequest,
) (sandboxcontract.ReadFileResult, error) {
	resolved, err := resolvePath(cfg, req.Path)
	if err != nil {
		return sandboxcontract.ReadFileResult{}, err
	}
	root, err := os.OpenRoot(resolved.rootHostDir)
	if err != nil {
		return sandboxcontract.ReadFileResult{}, fmt.Errorf("open root: %w", err)
	}
	defer root.Close()

	tf, err := readTextFile(root, resolved.rel)
	if err != nil {
		return sandboxcontract.ReadFileResult{}, err
	}

	offset := req.OffsetLines
	if offset == 0 {
		offset = 1
	}
	if offset < 1 {
		return sandboxcontract.ReadFileResult{}, fmt.Errorf("offset_lines must be >= 1")
	}

	limit := req.LimitLines
	if limit == 0 {
		limit = defaultReadLimitLines
	}
	if limit < 1 {
		return sandboxcontract.ReadFileResult{}, fmt.Errorf("limit_lines must be >= 1")
	}
	if limit > maxReadLimitLines {
		limit = maxReadLimitLines
	}

	return paginateText(tf.text, offset, limit)
}

// WriteFile performs an atomic rooted text file write.
func WriteFile(
	cfg Settings,
	req sandboxcontract.WriteFileRequest,
) (sandboxcontract.WriteFileResult, error) {
	resolved, err := resolvePath(cfg, req.Path)
	if err != nil {
		return sandboxcontract.WriteFileResult{}, err
	}
	if !utf8.ValidString(req.Content) {
		return sandboxcontract.WriteFileResult{}, fmt.Errorf("content must be valid UTF-8")
	}

	root, err := os.OpenRoot(resolved.rootHostDir)
	if err != nil {
		return sandboxcontract.WriteFileResult{}, fmt.Errorf("open root: %w", err)
	}
	defer root.Close()

	if err := writeAtomic(root, resolved.rel, []byte(req.Content)); err != nil {
		return sandboxcontract.WriteFileResult{}, err
	}
	return sandboxcontract.WriteFileResult{
		Path:         resolved.containerPath,
		BytesWritten: len(req.Content),
	}, nil
}

// EditFile performs one exact text replacement with line-ending preservation.
func EditFile(
	cfg Settings,
	req sandboxcontract.EditFileRequest,
) (sandboxcontract.EditFileResult, error) {
	resolved, err := resolvePath(cfg, req.Path)
	if err != nil {
		return sandboxcontract.EditFileResult{}, err
	}
	if req.OldText == "" {
		return sandboxcontract.EditFileResult{}, fmt.Errorf("old_text is required")
	}

	root, err := os.OpenRoot(resolved.rootHostDir)
	if err != nil {
		return sandboxcontract.EditFileResult{}, fmt.Errorf("open root: %w", err)
	}
	defer root.Close()

	tf, err := readTextFile(root, resolved.rel)
	if err != nil {
		return sandboxcontract.EditFileResult{}, err
	}

	normalizedOld := normalizeLineEndings(req.OldText)
	normalizedNew := normalizeLineEndings(req.NewText)
	normalizedContent := normalizeLineEndings(tf.text)

	occurrences := strings.Count(normalizedContent, normalizedOld)
	switch {
	case occurrences == 0:
		return sandboxcontract.EditFileResult{}, fmt.Errorf("old_text not found in file")
	case occurrences > 1:
		return sandboxcontract.EditFileResult{}, fmt.Errorf(
			"old_text appears %d times",
			occurrences,
		)
	}

	updatedNormalized := strings.Replace(normalizedContent, normalizedOld, normalizedNew, 1)
	if updatedNormalized == normalizedContent {
		return sandboxcontract.EditFileResult{}, fmt.Errorf("replacement produced no change")
	}

	finalText := restoreLineEndings(updatedNormalized, tf.lineEnding)
	finalRaw := append(append([]byte(nil), tf.bom...), []byte(finalText)...)

	if err := writeAtomic(root, resolved.rel, finalRaw); err != nil {
		return sandboxcontract.EditFileResult{}, err
	}

	diff, firstChangedLine := compactDiff(normalizedContent, updatedNormalized)
	return sandboxcontract.EditFileResult{
		Path:             resolved.containerPath,
		Diff:             diff,
		FirstChangedLine: firstChangedLine,
	}, nil
}

// ApplyPatch performs a rooted multi-file Codex-style patch application.
func ApplyPatch(
	cfg Settings,
	req sandboxcontract.ApplyPatchRequest,
) (sandboxcontract.ApplyPatchResult, error) {
	parsed, err := parsePatch(req.Patch)
	if err != nil {
		return sandboxcontract.ApplyPatchResult{}, err
	}
	desired, snapshots, diff, changed, summary, err := buildPatchPlan(cfg, parsed)
	if err != nil {
		return sandboxcontract.ApplyPatchResult{}, err
	}
	if err := commitDesiredStates(desired, snapshots); err != nil {
		return sandboxcontract.ApplyPatchResult{}, err
	}
	return sandboxcontract.ApplyPatchResult{
		ChangedFiles: changed,
		Diff:         diff,
		Summary:      summary,
	}, nil
}

func paginateText(
	text string,
	offsetLines int,
	limitLines int,
) (sandboxcontract.ReadFileResult, error) {
	lines := splitLogicalLines(normalizeLineEndings(text))
	totalLines := len(lines)
	if totalLines == 0 {
		if offsetLines != 1 {
			return sandboxcontract.ReadFileResult{}, fmt.Errorf(
				"offset_lines %d is beyond end of file",
				offsetLines,
			)
		}
		return sandboxcontract.ReadFileResult{
			Content:    "",
			TotalLines: 0,
		}, nil
	}
	start := offsetLines - 1
	if start >= totalLines {
		return sandboxcontract.ReadFileResult{}, fmt.Errorf(
			"offset_lines %d is beyond end of file",
			offsetLines,
		)
	}

	var out []string
	bytesUsed := 0
	nextOffset := 0
	truncated := false

	for i := start; i < totalLines && len(out) < limitLines; i++ {
		line := lines[i]
		lineBytes := len(line)
		if len(out) > 0 {
			lineBytes++
		}
		if bytesUsed+lineBytes > maxReadBytes {
			if len(out) == 0 {
				return sandboxcontract.ReadFileResult{}, fmt.Errorf(
					"requested line exceeds %d byte output limit",
					maxReadBytes,
				)
			}
			truncated = true
			nextOffset = i + 1
			break
		}
		out = append(out, line)
		bytesUsed += lineBytes
	}

	if !truncated && start+len(out) < totalLines {
		truncated = true
		nextOffset = start + len(out) + 1
	}

	result := sandboxcontract.ReadFileResult{
		Content:    strings.Join(out, "\n"),
		Truncated:  truncated,
		TotalLines: totalLines,
	}
	if truncated {
		result.NextOffsetLines = nextOffset
	}
	return result, nil
}

func resolvePath(cfg Settings, raw string) (resolvedPath, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return resolvedPath{}, fmt.Errorf("path is required")
	}

	workspaceDir := normalizeContainerRoot(cfg.WorkspaceDir)
	memoryDir := normalizeContainerRoot(cfg.MemoryDir)

	if path.IsAbs(raw) {
		cleaned := path.Clean(raw)
		switch {
		case cleaned == workspaceDir || cleaned == memoryDir:
			return resolvedPath{}, fmt.Errorf("path must reference a file, not a root")
		case strings.HasPrefix(cleaned, workspaceDir+"/"):
			rel := strings.TrimPrefix(cleaned, workspaceDir+"/")
			if err := validateRelativePath(rel); err != nil {
				return resolvedPath{}, err
			}
			return resolvedPath{
				rootHostDir:   strings.TrimSpace(cfg.WorkspaceHostDir),
				rootContainer: workspaceDir,
				rel:           filepath.FromSlash(rel),
				containerPath: cleaned,
			}, nil
		case strings.HasPrefix(cleaned, memoryDir+"/"):
			rel := strings.TrimPrefix(cleaned, memoryDir+"/")
			if err := validateRelativePath(rel); err != nil {
				return resolvedPath{}, err
			}
			return resolvedPath{
				rootHostDir:   strings.TrimSpace(cfg.MemoryHostDir),
				rootContainer: memoryDir,
				rel:           filepath.FromSlash(rel),
				containerPath: cleaned,
			}, nil
		default:
			return resolvedPath{}, fmt.Errorf(
				"absolute paths must be under %s or %s",
				workspaceDir,
				memoryDir,
			)
		}
	}

	cleaned := path.Clean(raw)
	if err := validateRelativePath(cleaned); err != nil {
		return resolvedPath{}, err
	}
	return resolvedPath{
		rootHostDir:   strings.TrimSpace(cfg.WorkspaceHostDir),
		rootContainer: workspaceDir,
		rel:           filepath.FromSlash(cleaned),
		containerPath: path.Join(workspaceDir, cleaned),
	}, nil
}

func normalizeContainerRoot(root string) string {
	root = strings.TrimSpace(root)
	if root == "" {
		return ""
	}
	cleaned := path.Clean(root)
	if !path.IsAbs(cleaned) {
		return ""
	}
	return cleaned
}

func validateRelativePath(rel string) error {
	if rel == "" || rel == "." {
		return fmt.Errorf("path must reference a file")
	}
	if strings.ContainsRune(rel, '\\') {
		return fmt.Errorf("path must use forward slashes")
	}
	if !fs.ValidPath(rel) {
		return fmt.Errorf("path %q is invalid", rel)
	}
	if !filepath.IsLocal(filepath.FromSlash(rel)) {
		return fmt.Errorf("path %q escapes root", rel)
	}
	return nil
}

func readTextFile(root *os.Root, rel string) (textFile, error) {
	info, err := root.Stat(rel)
	if err != nil {
		if os.IsNotExist(err) {
			return textFile{}, fmt.Errorf("file not found")
		}
		return textFile{}, fmt.Errorf("stat file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return textFile{}, fmt.Errorf("path is not a regular file")
	}

	raw, err := root.ReadFile(rel)
	if err != nil {
		return textFile{}, fmt.Errorf("read file: %w", err)
	}
	return decodeTextFile(raw)
}

func decodeTextFile(raw []byte) (textFile, error) {
	if bytes.IndexByte(raw, 0) >= 0 {
		return textFile{}, fmt.Errorf("file contains NUL bytes and is not supported as text")
	}
	if !utf8.Valid(raw) {
		return textFile{}, fmt.Errorf("file is not valid UTF-8")
	}

	tf := textFile{
		raw:         append([]byte(nil), raw...),
		lineEnding:  detectLineEnding(string(raw)),
		wasReadable: true,
	}
	switch {
	case bytes.HasPrefix(raw, []byte{0xEF, 0xBB, 0xBF}):
		tf.bom = []byte{0xEF, 0xBB, 0xBF}
		tf.text = string(raw[len(tf.bom):])
	default:
		tf.text = string(raw)
	}
	return tf, nil
}

func detectLineEnding(text string) string {
	if strings.Contains(text, "\r\n") {
		return "\r\n"
	}
	return "\n"
}

func normalizeLineEndings(text string) string {
	return strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
}

func restoreLineEndings(text string, lineEnding string) string {
	if lineEnding == "\r\n" {
		return strings.ReplaceAll(text, "\n", "\r\n")
	}
	return text
}

func splitLogicalLines(text string) []string {
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func compactDiff(oldText, newText string) (string, int) {
	oldLines := splitLogicalLines(oldText)
	newLines := splitLogicalLines(newText)

	prefix := 0
	for prefix < len(oldLines) && prefix < len(newLines) && oldLines[prefix] == newLines[prefix] {
		prefix++
	}

	oldSuffix := len(oldLines)
	newSuffix := len(newLines)
	for oldSuffix > prefix && newSuffix > prefix && oldLines[oldSuffix-1] == newLines[newSuffix-1] {
		oldSuffix--
		newSuffix--
	}

	firstChangedLine := prefix + 1
	if len(oldLines) == len(newLines) && prefix == len(oldLines) {
		firstChangedLine = 0
	}

	contextStart := maxInt(0, prefix-diffContextLines)
	contextEndOld := minInt(len(oldLines), oldSuffix+diffContextLines)
	contextEndNew := minInt(len(newLines), newSuffix+diffContextLines)

	var out []string
	for _, line := range oldLines[contextStart:prefix] {
		out = append(out, " "+line)
	}
	for _, line := range oldLines[prefix:oldSuffix] {
		out = append(out, "-"+line)
	}
	for _, line := range newLines[prefix:newSuffix] {
		out = append(out, "+"+line)
	}
	for _, line := range newLines[newSuffix:contextEndNew] {
		out = append(out, " "+line)
	}
	if len(out) == 0 && contextEndOld > contextStart {
		for _, line := range oldLines[contextStart:contextEndOld] {
			out = append(out, " "+line)
		}
	}
	return strings.Join(out, "\n"), firstChangedLine
}

func writeAtomic(root *os.Root, rel string, data []byte) error {
	if rel == "." {
		return fmt.Errorf("path must reference a file")
	}
	if info, err := root.Stat(rel); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("path is not a regular file")
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat file: %w", err)
	}

	dir := filepath.Dir(rel)
	if dir != "." {
		if err := root.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create parent directories: %w", err)
		}
	}

	tmpRel := tempSiblingPath(rel)
	file, err := root.OpenFile(tmpRel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}

	commitErr := func(err error) error {
		_ = file.Close()
		_ = root.Remove(tmpRel)
		return err
	}

	if _, err := file.Write(data); err != nil {
		return commitErr(fmt.Errorf("write temp file: %w", err))
	}
	if err := file.Sync(); err != nil {
		return commitErr(fmt.Errorf("sync temp file: %w", err))
	}
	if err := file.Close(); err != nil {
		_ = root.Remove(tmpRel)
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := root.Rename(tmpRel, rel); err != nil {
		_ = root.Remove(tmpRel)
		return fmt.Errorf("rename temp file: %w", err)
	}

	syncDir := "."
	if dir != "." {
		syncDir = dir
	}
	if dirFile, err := root.Open(syncDir); err == nil {
		_ = dirFile.Sync()
		_ = dirFile.Close()
	}
	return nil
}

func tempSiblingPath(rel string) string {
	dir := filepath.Dir(rel)
	base := filepath.Base(rel)
	name := ".q15-tmp-" + base + "-" + randomHex(8)
	if dir == "." {
		return name
	}
	return filepath.Join(dir, name)
}

func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "fallback"
	}
	return hex.EncodeToString(buf)
}

func commitDesiredStates(
	desired map[string]desiredFileState,
	snapshots map[string]fileSnapshot,
) error {
	keys := make([]string, 0, len(desired))
	for key := range desired {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	applied := make([]string, 0, len(keys))
	for _, key := range keys {
		state := desired[key]
		root, err := os.OpenRoot(state.resolved.rootHostDir)
		if err != nil {
			return fmt.Errorf("open root: %w", err)
		}

		if state.shouldExist {
			err = writeAtomic(root, state.resolved.rel, state.raw)
		} else {
			err = removeRegularFile(root, state.resolved.rel)
		}
		_ = root.Close()
		if err != nil {
			rollbackDesiredStates(applied, desired, snapshots)
			return err
		}
		applied = append(applied, key)
	}
	return nil
}

func rollbackDesiredStates(
	applied []string,
	desired map[string]desiredFileState,
	snapshots map[string]fileSnapshot,
) {
	for i := len(applied) - 1; i >= 0; i-- {
		key := applied[i]
		state := desired[key]
		snapshot := snapshots[key]

		root, err := os.OpenRoot(state.resolved.rootHostDir)
		if err != nil {
			continue
		}
		if snapshot.exists {
			_ = writeAtomic(root, state.resolved.rel, snapshot.raw)
		} else {
			_ = removeIfExists(root, state.resolved.rel)
		}
		_ = root.Close()
	}
}

func removeRegularFile(root *os.Root, rel string) error {
	info, err := root.Stat(rel)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("path is not a regular file")
	}
	if err := root.Remove(rel); err != nil {
		return fmt.Errorf("remove file: %w", err)
	}
	return nil
}

func removeIfExists(root *os.Root, rel string) error {
	err := root.Remove(rel)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
