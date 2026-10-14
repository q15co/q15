package memory

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/cognition"
)

// semanticSourceRelativePath normalizes and validates one semantic source
// path. The chunk and drain accessors are deliberately restricted to the
// three canonical roots: they exist so the migration can walk the oversized
// legacy files, and no other file may pass through them.
func semanticSourceRelativePath(relativePath string) (string, error) {
	normalized := filepath.ToSlash(strings.TrimSpace(relativePath))
	switch normalized {
	case semanticFactsRelativePath,
		semanticPreferencesRelativePath,
		semanticProjectsRelativePath:
		return normalized, nil
	}
	return "", fmt.Errorf("path %q is not a semantic source file", relativePath)
}

// LoadSemanticChunk reads at most length bytes of one semantic source file
// starting at offset, cut back to the last line boundary the window
// contains, so a caller never converts half an entry. A window with no line
// boundary at all yields empty content while the file continues past it; a
// window with no newline only because it reaches the end of the file yields
// that whole unterminated final line. A missing file yields an empty chunk.
func (s *Store) LoadSemanticChunk(
	ctx context.Context,
	relativePath string,
	offset int64,
	length int64,
) (cognition.SemanticChunk, error) {
	_ = ctx

	if length <= 0 {
		return cognition.SemanticChunk{}, fmt.Errorf(
			"semantic chunk length must be positive, got %d",
			length,
		)
	}
	if offset < 0 {
		return cognition.SemanticChunk{}, fmt.Errorf(
			"semantic chunk offset must not be negative, got %d",
			offset,
		)
	}
	relative, err := semanticSourceRelativePath(relativePath)
	if err != nil {
		return cognition.SemanticChunk{}, err
	}

	release := s.repository.Acquire()
	defer release()

	path := filepath.Join(s.root(), filepath.FromSlash(relative))
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cognition.SemanticChunk{
				RelativePath: relative,
				Offset:       offset,
			}, nil
		}
		return cognition.SemanticChunk{}, fmt.Errorf(
			"read semantic source %q: %w",
			path,
			err,
		)
	}
	size := int64(len(data))
	if offset > size {
		offset = size
	}
	end := offset + length
	if end > size {
		end = size
	}
	window := data[offset:end]
	content := ""
	switch {
	case len(window) == 0:
	case end == size:
		// The window ends at the end of the file, so its last line is whole
		// whether or not the file closes with a newline.
		content = string(window)
	default:
		cut := bytes.LastIndexByte(window, '\n')
		if cut >= 0 {
			content = string(window[:cut+1])
		}
	}
	return cognition.SemanticChunk{
		RelativePath: relative,
		Offset:       offset,
		Content:      content,
		Size:         size,
		More:         offset+int64(len(content)) < size,
	}, nil
}

// RemoveSemanticLines deletes the whole lines of one semantic source file
// that start in the byte range [start, end) and whose line hash
// (cognition.SemanticLineHash) is listed, keeping every other byte of the
// file byte-for-byte. It returns the file's size after the rewrite and
// fails when any listed hash matches no line in range, so a source file that
// moved under the caller is reported instead of drained wrongly. Empty
// hashes lists and ranges without a match are caller errors.
func (s *Store) RemoveSemanticLines(
	ctx context.Context,
	relativePath string,
	start int64,
	end int64,
	hashes []string,
) (int64, error) {
	_ = ctx

	if end < start {
		return 0, fmt.Errorf(
			"semantic drain range end %d is before start %d",
			end,
			start,
		)
	}
	if len(hashes) == 0 {
		return 0, fmt.Errorf("semantic drain requires at least one line hash")
	}
	relative, err := semanticSourceRelativePath(relativePath)
	if err != nil {
		return 0, err
	}

	release := s.repository.Acquire()
	defer release()

	path := filepath.Join(s.root(), filepath.FromSlash(relative))
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read semantic source %q: %w", path, err)
	}

	wanted := make(map[string]struct{}, len(hashes))
	for _, hash := range hashes {
		trimmed := strings.TrimSpace(hash)
		if trimmed == "" {
			return 0, fmt.Errorf("semantic drain received an empty line hash")
		}
		wanted[trimmed] = struct{}{}
	}

	var remaining []byte
	unmatched := make(map[string]struct{}, len(wanted))
	for hash := range wanted {
		unmatched[hash] = struct{}{}
	}
	position := int64(0)
	for position < int64(len(data)) {
		cut := bytes.IndexByte(data[position:], '\n')
		lineEnd := position
		if cut >= 0 {
			lineEnd += int64(cut) + 1
		} else {
			lineEnd = int64(len(data))
		}
		line := data[position:lineEnd]
		match := false
		if position >= start && position < end {
			hash := cognition.SemanticLineHash(string(line))
			if _, ok := wanted[hash]; ok {
				match = true
				delete(unmatched, hash)
			}
		}
		if !match {
			remaining = append(remaining, line...)
		}
		position = lineEnd
	}
	if len(unmatched) > 0 {
		return 0, fmt.Errorf(
			"semantic drain of %q found no line in range [%d, %d) matching hash %s",
			relative,
			start,
			end,
			anyUnmatchedHash(unmatched),
		)
	}
	if len(remaining) == len(data) {
		return int64(len(data)), nil
	}
	if err := writeBytesFileAtomic(path, remaining); err != nil {
		return 0, fmt.Errorf("rewrite semantic source %q: %w", path, err)
	}
	if err := s.repository.Commit(
		ctx,
		fmt.Sprintf("memory: drain migrated semantic lines from %s", relative),
		filepath.ToSlash(relative),
	); err != nil {
		return 0, fmt.Errorf(
			"commit drained semantic source %q: %w",
			relative,
			err,
		)
	}
	return int64(len(remaining)), nil
}

func anyUnmatchedHash(unmatched map[string]struct{}) string {
	for hash := range unmatched {
		return hash
	}
	return ""
}
