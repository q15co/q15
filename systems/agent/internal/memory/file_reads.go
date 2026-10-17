package memory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func resolveMemoryFilePath(relativePath string) (string, error) {
	normalized := filepath.ToSlash(strings.TrimSpace(relativePath))
	if normalized == "" || normalized == "." || filepath.IsAbs(filepath.FromSlash(normalized)) {
		return "", fmt.Errorf("memory file path %q must be relative", relativePath)
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(normalized)))
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("memory file path %q escapes the memory root", relativePath)
	}
	return clean, nil
}

// ReadFileBytes preserves the exact bytes used to validate and recover a run.
func (s *Store) ReadFileBytes(ctx context.Context, relativePath string) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	relative, err := resolveMemoryFilePath(relativePath)
	if err != nil {
		return nil, false, err
	}
	release := s.repository.Acquire()
	defer release()
	root, err := os.OpenRoot(s.root())
	if err != nil {
		return nil, false, fmt.Errorf("open memory root: %w", err)
	}
	defer root.Close()
	data, err := root.ReadFile(filepath.FromSlash(relative))
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read memory file %q: %w", relative, err)
	}
	return data, true, nil
}

// LoadConversionLedgerHistory returns only archived ledger records. The active
// ledger is read separately so its append-only prefix can be checked exactly.
func (s *Store) LoadConversionLedgerHistory(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	release := s.repository.Acquire()
	defer release()
	files, err := filepath.Glob(filepath.Join(s.root(), ledgerDirPath, "*.md"))
	if err != nil {
		return "", fmt.Errorf("list archived ledgers: %w", err)
	}
	var content strings.Builder
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("read archived ledger %q: %w", file, err)
		}
		content.Write(raw)
		if len(raw) > 0 && raw[len(raw)-1] != '\n' {
			content.WriteByte('\n')
		}
	}
	return content.String(), nil
}
