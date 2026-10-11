package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestArchiveWorkingMemoryWritesAndPrunesCopies(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory")
	store := newTestStore(root, &fakeCommitter{})
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	archiveDir := filepath.Join(root, workingDirPath, workingMemoryArchiveDir)
	if err := os.MkdirAll(archiveDir, 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	handWritten := filepath.Join(archiveDir, "WORKING_MEMORY-2026-09-30-full.md")
	if err := os.WriteFile(handWritten, []byte("hand written"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	var (
		lastPath    string
		lastContent string
	)
	for i := 0; i < workingMemoryArchiveLimit+2; i++ {
		content := "state " + string(rune('a'+i))
		path, err := store.ArchiveWorkingMemory(context.Background(), content)
		if err != nil {
			t.Fatalf("ArchiveWorkingMemory() error = %v", err)
		}
		if !strings.HasPrefix(path, "/memory/working/archive/") {
			t.Fatalf("archive path = %q, want a path under /memory/working/archive/", path)
		}
		lastPath = path
		lastContent = content
		// The copy names carry a timestamp; keep them distinct so the
		// retention assertions below cannot be satisfied by a collision.
		time.Sleep(2 * time.Millisecond)
	}

	entries, err := os.ReadDir(archiveDir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	automatic := 0
	for _, entry := range entries {
		if _, ok := workingMemoryArchiveStamp(entry.Name()); ok {
			automatic++
		}
	}
	if automatic != workingMemoryArchiveLimit {
		t.Fatalf("automatic copies = %d, want %d", automatic, workingMemoryArchiveLimit)
	}

	if _, err := os.Stat(handWritten); err != nil {
		t.Fatalf("hand-written archive was pruned: %v", err)
	}

	written := filepath.Join(
		root,
		filepath.FromSlash(strings.TrimPrefix(lastPath, "/memory/")),
	)
	data, err := os.ReadFile(written)
	if err != nil {
		t.Fatalf("ReadFile(last copy) error = %v", err)
	}
	if string(data) != lastContent {
		t.Fatalf("archived content = %q, want %q", string(data), lastContent)
	}
}

func TestArchiveWorkingMemoryRejectsEmptyContent(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory")
	store := newTestStore(root, &fakeCommitter{})
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	if _, err := store.ArchiveWorkingMemory(context.Background(), "   \n"); err == nil {
		t.Fatal("ArchiveWorkingMemory() error = nil, want an empty-content failure")
	}
}

func TestWorkingMemoryArchiveStampSkipsHandWrittenNames(t *testing.T) {
	t.Parallel()

	for _, name := range []string{
		"WORKING_MEMORY-2026-09-30-full.md",
		"WORKING_MEMORY-2026-10-11-pre-demotion-full.md",
		"README.md",
		"WORKING_MEMORY-.md",
	} {
		if _, ok := workingMemoryArchiveStamp(name); ok {
			t.Fatalf("workingMemoryArchiveStamp(%q) = true, want false", name)
		}
	}
	if _, ok := workingMemoryArchiveStamp("WORKING_MEMORY-20261011T231500.000000000.md"); !ok {
		t.Fatal("workingMemoryArchiveStamp() = false for one of this store's own copies")
	}
}
