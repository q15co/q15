package memory

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestStoreConversionReadsSeeSizesOpenLoopsAndLedger(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory")
	store := newTestStore(root, &fakeCommitter{})
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	topic := filepath.Join(root, "semantic", "topics", "q15.md")
	if err := os.WriteFile(topic, []byte("0123456789"), 0o644); err != nil {
		t.Fatalf("WriteFile(topic) error = %v", err)
	}
	nested := filepath.Join(root, "semantic", "topics", "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("MkdirAll(nested) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(nested, "x.md"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile(nested) error = %v", err)
	}

	sizes, err := store.LoadSemanticFileSizes(ctx)
	if err != nil {
		t.Fatalf("LoadSemanticFileSizes() error = %v", err)
	}
	if got, want := sizes["semantic/topics/q15.md"], int64(10); got != want {
		t.Fatalf("topic size = %d, want %d (sizes=%v)", got, want, sizes)
	}
	for _, root := range []string{"semantic/facts.md", "semantic/preferences.md", "semantic/projects.md"} {
		if sizes[root] <= 0 {
			t.Fatalf("size[%s] = %d, want > 0 (sizes=%v)", root, sizes[root], sizes)
		}
	}
	if _, ok := sizes["semantic/topics/nested/x.md"]; ok {
		t.Fatalf("sizes include nested topic file: %v", sizes)
	}
	if got, want := len(sizes), 4; got != want {
		t.Fatalf("len(sizes) = %d, want %d (%v)", got, want, sizes)
	}

	loopsPath := filepath.Join(root, "working", "OPEN_LOOPS.md")
	if err := os.WriteFile(loopsPath, []byte("\n- [l-2026-0001] loop\n\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(open loops) error = %v", err)
	}
	loops, err := store.LoadOpenLoops(ctx)
	if err != nil {
		t.Fatalf("LoadOpenLoops() error = %v", err)
	}
	if got, want := loops, "- [l-2026-0001] loop"; got != want {
		t.Fatalf("LoadOpenLoops() = %q, want %q", got, want)
	}

	if err := os.Remove(loopsPath); err != nil {
		t.Fatalf("Remove(open loops) error = %v", err)
	}
	if loops, err := store.LoadOpenLoops(ctx); err != nil || loops != "" {
		t.Fatalf("LoadOpenLoops(missing) = %q, %v; want empty, nil", loops, err)
	}
}
