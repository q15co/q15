package memory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

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
