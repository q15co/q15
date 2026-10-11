package memory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// workingMemoryArchiveDir holds recovery copies of the prompt-visible
	// working-memory artifact, written before a run that is likely to shorten
	// it.
	workingMemoryArchiveDir = "archive"
	// workingMemoryArchivePrefix and workingMemoryArchiveLayout name those
	// copies. The prefix alone does not identify one: pruning also requires
	// the name to parse as this layout, so a hand-written archive sitting
	// beside them is never removed.
	workingMemoryArchivePrefix = "WORKING_MEMORY-"
	workingMemoryArchiveLayout = "20060102T150405.000000000"
	// workingMemoryArchiveLimit bounds how many automatic copies are kept.
	workingMemoryArchiveLimit = 3
)

// ArchiveWorkingMemory writes one verbatim copy of the working-memory artifact
// under /memory/working/archive/ and returns the runtime path of the copy.
//
// The artifact is injected into every prompt in full, so it has a size ceiling;
// the consolidation job calls this before a run that is likely to shorten the
// file, which is what lets the ceiling be enforced by demotion instead of by
// truncation. The copies are deliberately not committed to the memory
// repository: they are recovery material rather than canonical state, and one
// full copy per demotion would bloat history for nothing.
func (s *Store) ArchiveWorkingMemory(ctx context.Context, content string) (string, error) {
	_ = ctx

	release := s.repository.Acquire()
	defer release()

	if strings.TrimSpace(content) == "" {
		return "", fmt.Errorf("archive working memory: content is empty")
	}

	dir := filepath.Join(s.root(), workingDirPath, workingMemoryArchiveDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create working-memory archive directory %q: %w", dir, err)
	}

	name := workingMemoryArchivePrefix +
		time.Now().UTC().Format(workingMemoryArchiveLayout) + ".md"
	path := filepath.Join(dir, name)
	if err := writeBytesFileAtomic(path, []byte(content)); err != nil {
		return "", fmt.Errorf("write working-memory archive %q: %w", path, err)
	}
	if err := pruneWorkingMemoryArchive(dir); err != nil {
		return "", err
	}

	relative, err := filepath.Rel(s.root(), path)
	if err != nil {
		return "", fmt.Errorf("resolve relative working-memory archive path %q: %w", path, err)
	}
	return "/memory/" + filepath.ToSlash(relative), nil
}

// pruneWorkingMemoryArchive keeps the newest workingMemoryArchiveLimit
// automatic copies and deletes the rest. A file whose name does not parse as
// this store's own archive layout is left alone.
func pruneWorkingMemoryArchive(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read working-memory archive directory %q: %w", dir, err)
	}

	type archivedCopy struct {
		name string
		at   time.Time
	}
	copies := make([]archivedCopy, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		at, ok := workingMemoryArchiveStamp(entry.Name())
		if !ok {
			continue
		}
		copies = append(copies, archivedCopy{name: entry.Name(), at: at})
	}
	if len(copies) <= workingMemoryArchiveLimit {
		return nil
	}

	sort.Slice(copies, func(i, j int) bool { return copies[i].at.After(copies[j].at) })
	for _, expired := range copies[workingMemoryArchiveLimit:] {
		if err := os.Remove(filepath.Join(dir, expired.name)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("prune working-memory archive %q: %w", expired.name, err)
		}
	}
	return nil
}

func workingMemoryArchiveStamp(name string) (time.Time, bool) {
	if !strings.HasPrefix(name, workingMemoryArchivePrefix) ||
		!strings.HasSuffix(name, ".md") {
		return time.Time{}, false
	}
	stamp := strings.TrimSuffix(strings.TrimPrefix(name, workingMemoryArchivePrefix), ".md")
	at, err := time.Parse(workingMemoryArchiveLayout, stamp)
	if err != nil {
		return time.Time{}, false
	}
	return at, true
}
