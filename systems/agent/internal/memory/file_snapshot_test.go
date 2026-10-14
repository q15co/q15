package memory

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
)

func TestStoreReadWriteDeleteFileBytesRoundTrip(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "memory")
	store := newTestStore(root, &fakeCommitter{})
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	// A byte-exact payload with leading/trailing whitespace and no trailing
	// newline, which a trimmed loader would mangle. The rollback contract
	// needs these bytes back verbatim.
	payload := []byte("  leading\n\t\n trailing without newline")
	relative := "working/WORKING_MEMORY.md"

	if _, existed, err := store.ReadFileBytes(context.Background(), relative); err != nil ||
		!existed {
		t.Fatalf("ReadFileBytes before = existed=%v err=%v, want true nil", existed, err)
	}
	if err := store.WriteFileBytes(context.Background(), relative, payload); err != nil {
		t.Fatalf("WriteFileBytes() error = %v", err)
	}
	got, existed, err := store.ReadFileBytes(context.Background(), relative)
	if err != nil || !existed {
		t.Fatalf("ReadFileBytes after write = existed=%v err=%v, want true nil", existed, err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("round-trip bytes = %q, want %q", got, payload)
	}

	if err := store.DeleteFile(context.Background(), relative); err != nil {
		t.Fatalf("DeleteFile() error = %v", err)
	}
	if _, existed, err := store.ReadFileBytes(context.Background(), relative); err != nil ||
		existed {
		t.Fatalf("ReadFileBytes after delete = existed=%v err=%v, want false nil", existed, err)
	}
	// A second delete of a missing file is a no-op.
	if err := store.DeleteFile(context.Background(), relative); err != nil {
		t.Fatalf("second DeleteFile() error = %v", err)
	}
}

func TestResolveMemoryFilePathRejectsEscapes(t *testing.T) {
	t.Parallel()

	for _, tc := range []string{
		"",
		".",
		"../x",
		"a/../../x",
		"/abs/path",
	} {
		if _, err := resolveMemoryFilePath(tc); err == nil {
			t.Fatalf("resolveMemoryFilePath(%q) = nil error, want rejection", tc)
		}
	}
	for _, tc := range []string{
		"working/WORKING_MEMORY.md",
		"semantic/topics/q15.md",
	} {
		if _, err := resolveMemoryFilePath(tc); err != nil {
			t.Fatalf("resolveMemoryFilePath(%q) error = %v, want nil", tc, err)
		}
	}
}
