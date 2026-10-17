package memory

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/cognition"
)

// TestListMemoryFilesCoversExactlyTheTwoContentRoots pins the perimeter and
// the contract: every regular file under semantic/ and working/ is listed
// with its on-disk size, directories and symlinks are not files, paths stay
// repository-relative and sorted, and nothing outside the two roots ever
// appears, however large or tempting.
func TestListMemoryFilesCoversExactlyTheTwoContentRoots(t *testing.T) {
	t.Parallel()

	store, root := statusFixtureStore(t, []fixtureFile{
		{relative: "semantic/facts.md", content: "facts"},
		{relative: "semantic/topics/a.md", content: "topic a"},
		{relative: "semantic/topics/nested/deep.md", content: "nested"},
		{relative: "working/WORKING_MEMORY.md", content: "working"},
		{relative: "working/LEDGER.md", content: "ledger"},
		{relative: "working/ledger/2026-Q3.md", content: "archived"},
		{relative: "core/AGENT.md", content: "core"},
		{relative: "history/state/head.json", content: "{}"},
		{relative: "notes/inbox/todo.md", content: "note"},
		{relative: "README.md", content: "readme"},
	})
	if err := os.Symlink("LEDGER.md", filepath.Join(root, "working", "linked.md")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "working", "empty"), 0o755); err != nil {
		t.Fatalf("create empty dir: %v", err)
	}

	files, err := store.ListMemoryFiles(context.Background())
	if err != nil {
		t.Fatalf("ListMemoryFiles() error = %v", err)
	}

	want := []cognition.MemoryFile{
		{Path: "semantic/facts.md", Bytes: 5},
		{Path: "semantic/topics/a.md", Bytes: 7},
		{Path: "semantic/topics/nested/deep.md", Bytes: 6},
		{Path: "working/LEDGER.md", Bytes: 6},
		{Path: "working/WORKING_MEMORY.md", Bytes: 7},
		{Path: "working/ledger/2026-Q3.md", Bytes: 8},
	}
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("ListMemoryFiles() = %#v, want %#v", files, want)
	}
	for _, file := range files {
		if strings.HasPrefix(file.Path, "core/") ||
			strings.HasPrefix(file.Path, "history/") ||
			strings.HasPrefix(file.Path, "notes/") ||
			file.Path == "README.md" {
			t.Fatalf("listing left the two content roots: %q", file.Path)
		}
	}
}

// TestListMemoryFilesSizesMatchDisk pins that sizes are the file's on-disk
// bytes as stat reports them, not a derived or truncated measure.
func TestListMemoryFilesSizesMatchDisk(t *testing.T) {
	t.Parallel()

	store, root := statusFixtureStore(t, []fixtureFile{
		{relative: "semantic/facts.md", content: strings.Repeat("x", 123)},
		{relative: "working/WORKING_MEMORY.md", content: strings.Repeat("y\n", 57)},
	})

	files, err := store.ListMemoryFiles(context.Background())
	if err != nil {
		t.Fatalf("ListMemoryFiles() error = %v", err)
	}
	for _, file := range files {
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(file.Path)))
		if err != nil {
			t.Fatalf("stat %s: %v", file.Path, err)
		}
		if file.Bytes != info.Size() {
			t.Fatalf(
				"Bytes for %s = %d, want the on-disk size %d",
				file.Path,
				file.Bytes,
				info.Size(),
			)
		}
	}
}

// TestListMemoryFilesMissingRootsListEmpty pins the fresh-root shape: a root
// that has never been initialized has no content roots, and that is an empty
// listing, not an error.
func TestListMemoryFilesMissingRootsListEmpty(t *testing.T) {
	t.Parallel()

	store := newTestStore(filepath.Join(t.TempDir(), "memory"), &fakeCommitter{})
	files, err := store.ListMemoryFiles(context.Background())
	if err != nil {
		t.Fatalf("ListMemoryFiles() error = %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("ListMemoryFiles() = %#v, want empty on a bare root", files)
	}
}

// TestListMemoryFilesHonoursContext pins the one precondition the caller
// controls: a cancelled context stops the listing before it walks.
func TestListMemoryFilesHonoursContext(t *testing.T) {
	t.Parallel()

	store, _ := statusFixtureStore(t, []fixtureFile{
		{relative: "semantic/facts.md", content: "facts"},
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.ListMemoryFiles(ctx); err == nil {
		t.Fatal("ListMemoryFiles() error = nil, want the cancelled context back")
	}
}
