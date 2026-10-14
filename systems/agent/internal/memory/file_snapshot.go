package memory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// resolveMemoryFilePath normalizes and validates one repository-relative
// slash path for raw byte access. It rejects empty, absolute, and
// traversal-escaping paths so a caller can never read or write outside the
// memory root.
func resolveMemoryFilePath(relativePath string) (string, error) {
	normalized := filepath.ToSlash(strings.TrimSpace(relativePath))
	if normalized == "" || normalized == "." {
		return "", fmt.Errorf("memory file path is required")
	}
	fromSlash := filepath.FromSlash(normalized)
	if filepath.IsAbs(fromSlash) {
		return "", fmt.Errorf("memory file path %q must be relative", relativePath)
	}
	clean := filepath.ToSlash(filepath.Clean(fromSlash))
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("memory file path %q escapes the memory root", relativePath)
	}
	return clean, nil
}

// ReadFileBytes returns the raw on-disk bytes of one repository-relative
// memory file and whether it existed. A missing file yields (nil, false, nil).
func (s *Store) ReadFileBytes(
	ctx context.Context,
	relativePath string,
) ([]byte, bool, error) {
	_ = ctx

	relative, err := resolveMemoryFilePath(relativePath)
	if err != nil {
		return nil, false, err
	}

	release := s.repository.Acquire()
	defer release()

	path := filepath.Join(s.root(), filepath.FromSlash(relative))
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("read memory file %q: %w", path, err)
	}
	return data, true, nil
}

// WriteFileBytes replaces the whole content of one repository-relative memory
// file and commits it, preserving the bytes exactly as given. The parent
// directory must already exist: the conversion-pass rollback only ever
// restores files that existed before the run, so it never creates directories.
func (s *Store) WriteFileBytes(
	ctx context.Context,
	relativePath string,
	data []byte,
) error {
	relative, err := resolveMemoryFilePath(relativePath)
	if err != nil {
		return err
	}

	release := s.repository.Acquire()
	defer release()

	path := filepath.Join(s.root(), filepath.FromSlash(relative))
	if err := writeBytesFileAtomic(path, data); err != nil {
		return fmt.Errorf("write memory file %q: %w", path, err)
	}
	if err := s.repository.Commit(
		ctx,
		"memory: restore file",
		relative,
	); err != nil {
		return fmt.Errorf("commit memory file %q: %w", relative, err)
	}
	return nil
}

// DeleteFile removes one repository-relative memory file and commits the
// removal. A missing file counts as already deleted.
func (s *Store) DeleteFile(ctx context.Context, relativePath string) error {
	relative, err := resolveMemoryFilePath(relativePath)
	if err != nil {
		return err
	}

	release := s.repository.Acquire()
	defer release()

	path := filepath.Join(s.root(), filepath.FromSlash(relative))
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("delete memory file %q: %w", path, err)
	}
	if err := s.repository.Commit(
		ctx,
		"memory: delete file",
		relative,
	); err != nil {
		return fmt.Errorf("commit delete of memory file %q: %w", relative, err)
	}
	return nil
}
