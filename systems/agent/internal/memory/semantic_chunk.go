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

// semanticSourceRelativePath normalizes and validates one migration source
// path. The chunk and drain accessors are deliberately restricted to the
// three canonical roots plus the staged archive residue: they exist so the
// migration can walk the oversized legacy files, and no other file may pass
// through them.
func semanticSourceRelativePath(relativePath string) (string, error) {
	normalized := filepath.ToSlash(strings.TrimSpace(relativePath))
	switch normalized {
	case semanticFactsRelativePath,
		semanticPreferencesRelativePath,
		semanticProjectsRelativePath,
		archiveResidueRelativePath:
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

// RemoveSemanticLines deletes exactly the whole lines of one semantic source
// file that the caller cites by byte offset and line hash, keeping every other
// byte of the file byte-for-byte. It returns the file's size after the rewrite
// and fails when a cited offset begins no line, or begins a line that does not
// hash to the cited hash, so a source file that moved under the caller is
// reported instead of drained wrongly. Citing lines by offset rather than
// citing a set of hashes is what keeps a byte-identical line at another offset
// in the file: only the lines a ledger line names may leave. An empty citation
// list, a negative offset, a repeated offset and an empty hash are caller
// errors.
func (s *Store) RemoveSemanticLines(
	ctx context.Context,
	relativePath string,
	lines []cognition.SemanticDrainLine,
) (int64, error) {
	_ = ctx

	if len(lines) == 0 {
		return 0, fmt.Errorf("semantic drain requires at least one cited line")
	}
	relative, err := semanticSourceRelativePath(relativePath)
	if err != nil {
		return 0, err
	}

	wanted := make(map[int64]string, len(lines))
	for _, line := range lines {
		hash := strings.TrimSpace(line.Hash)
		if hash == "" {
			return 0, fmt.Errorf(
				"semantic drain received an empty line hash for offset %d",
				line.ByteOffset,
			)
		}
		if line.ByteOffset < 0 {
			return 0, fmt.Errorf(
				"semantic drain received a negative line offset %d",
				line.ByteOffset,
			)
		}
		if _, dup := wanted[line.ByteOffset]; dup {
			return 0, fmt.Errorf(
				"semantic drain cites line offset %d twice",
				line.ByteOffset,
			)
		}
		wanted[line.ByteOffset] = hash
	}

	release := s.repository.Acquire()
	defer release()

	path := filepath.Join(s.root(), filepath.FromSlash(relative))
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read semantic source %q: %w", path, err)
	}

	var remaining []byte
	drained := make(map[int64]struct{}, len(wanted))
	position := int64(0)
	for position < int64(len(data)) {
		cut := bytes.IndexByte(data[position:], '\n')
		lineEnd := int64(len(data))
		if cut >= 0 {
			lineEnd = position + int64(cut) + 1
		}
		line := data[position:lineEnd]
		if cited, ok := wanted[position]; ok {
			if hash := cognition.SemanticLineHash(string(line)); hash != cited {
				return 0, fmt.Errorf(
					"semantic drain of %q cites offset %d with hash %s, but the "+
						"line there hashes to %s",
					relative,
					position,
					cited,
					hash,
				)
			}
			drained[position] = struct{}{}
		} else {
			remaining = append(remaining, line...)
		}
		position = lineEnd
	}
	for _, line := range lines {
		if _, ok := drained[line.ByteOffset]; !ok {
			return 0, fmt.Errorf(
				"semantic drain of %q cites offset %d, which begins no line in "+
					"the %d bytes of the file",
				relative,
				line.ByteOffset,
				len(data),
			)
		}
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
