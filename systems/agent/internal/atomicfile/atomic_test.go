package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteBytesAtomicallyReplacesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "state.json")
	if err := WriteBytes(path, []byte("before\n")); err != nil {
		t.Fatalf("WriteBytes(before) error = %v", err)
	}
	if err := WriteBytes(path, []byte("after\n")); err != nil {
		t.Fatalf("WriteBytes(after) error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(data) != "after\n" {
		t.Fatalf("content = %q, want after", data)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "state.json" {
		t.Fatalf("directory entries = %#v, want final file only", entries)
	}
}

func TestRemoveDurablyDeletesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := WriteBytes(path, []byte("{}\n")); err != nil {
		t.Fatalf("WriteBytes() error = %v", err)
	}
	if err := Remove(path); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("Stat() error = %v, want not exist", err)
	}
}
