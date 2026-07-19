// Package atomicfile provides durable atomic filesystem mutations.
package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
)

// WriteBytes replaces path using a temporary sibling, an atomic rename, and a
// parent-directory sync.
func WriteBytes(path string, data []byte) (err error) {
	dir := filepath.Dir(path)
	if err := EnsureDirectory(dir, 0o755); err != nil {
		return fmt.Errorf("create parent dir %q: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file for %q: %w", path, err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp file for %q: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp file for %q: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file for %q: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("rename temp file for %q: %w", path, err)
	}
	if err := SyncDirectory(dir); err != nil {
		return fmt.Errorf("sync parent dir for %q: %w", path, err)
	}
	return nil
}

// EnsureDirectory creates path and durably records every newly created
// directory entry up to the first existing ancestor.
func EnsureDirectory(path string, perm os.FileMode) error {
	path = filepath.Clean(path)
	info, err := os.Stat(path)
	if err == nil {
		if !info.IsDir() {
			return fmt.Errorf("path %q is not a directory", path)
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}

	parent := filepath.Dir(path)
	if parent == path {
		return fmt.Errorf("directory root %q does not exist", path)
	}
	if err := EnsureDirectory(parent, perm); err != nil {
		return err
	}
	if err := os.Mkdir(path, perm); err != nil {
		if !os.IsExist(err) {
			return err
		}
		info, statErr := os.Stat(path)
		if statErr != nil {
			return statErr
		}
		if !info.IsDir() {
			return fmt.Errorf("path %q is not a directory", path)
		}
	}
	return SyncDirectory(parent)
}

// Remove removes path and durably records the directory change.
func Remove(path string) error {
	if err := os.Remove(path); err != nil {
		return err
	}
	if err := SyncDirectory(filepath.Dir(path)); err != nil {
		return fmt.Errorf("sync parent dir for %q: %w", path, err)
	}
	return nil
}

// SyncDirectory flushes directory-entry changes for path.
func SyncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
