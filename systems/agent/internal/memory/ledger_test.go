package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStoreInitCreatesWorkingMemoryFiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory")
	store := newTestStore(root, &fakeCommitter{})

	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	for _, path := range []string{
		filepath.Join(root, "semantic", "topics"),
		filepath.Join(root, "working", "OPEN_LOOPS.md"),
		filepath.Join(root, "working", "LEDGER.md"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected path %q to exist: %v", path, err)
		}
	}

	info, err := os.Stat(filepath.Join(root, "semantic", "topics"))
	if err != nil {
		t.Fatalf("Stat(semantic/topics) error = %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("semantic/topics is not a directory")
	}

	loops, err := os.ReadFile(filepath.Join(root, "working", "OPEN_LOOPS.md"))
	if err != nil {
		t.Fatalf("ReadFile(OPEN_LOOPS.md) error = %v", err)
	}
	for _, want := range []string{
		"# Open Loops",
		"l-YYYY-NNNN",
		"trigger: date:YYYY-MM-DD | event:",
		"owner: agent|user",
	} {
		if !strings.Contains(string(loops), want) {
			t.Fatalf("OPEN_LOOPS.md missing %q:\n%s", want, string(loops))
		}
	}

	ledger, err := os.ReadFile(filepath.Join(root, "working", "LEDGER.md"))
	if err != nil {
		t.Fatalf("ReadFile(LEDGER.md) error = %v", err)
	}
	if !strings.Contains(string(ledger), "Conversion Ledger") {
		t.Fatalf("LEDGER.md missing header:\n%s", string(ledger))
	}
	if paragraphs := strings.Split(strings.TrimSpace(string(ledger)), "\n\n"); len(
		paragraphs,
	) != 2 {
		t.Fatalf(
			"LEDGER.md = %d paragraphs, want title plus one-line header:\n%s",
			len(paragraphs),
			string(ledger),
		)
	}
}

func TestStoreInitIsIdempotentForWorkingMemoryFiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory")
	store := newTestStore(root, &fakeCommitter{})
	ctx := context.Background()

	if err := store.Init(ctx); err != nil {
		t.Fatalf("first Init() error = %v", err)
	}

	// Mutate every new file so a re-run that overwrites would be visible.
	const marker = "preserved-by-rerun-marker\n"
	for _, path := range []string{
		filepath.Join(root, "working", "OPEN_LOOPS.md"),
		filepath.Join(root, "working", "LEDGER.md"),
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile(%q) error = %v", path, err)
		}
		if err := os.WriteFile(path, append(raw, []byte(marker)...), 0o644); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", path, err)
		}
	}

	before := snapshotNewMemoryPaths(t, root)

	if err := store.Init(ctx); err != nil {
		t.Fatalf("second Init() error = %v", err)
	}

	after := snapshotNewMemoryPaths(t, root)
	for path, content := range before {
		if after[path] != content {
			t.Fatalf("second Init changed %q:\nbefore=%q\nafter=%q", path, content, after[path])
		}
	}
	if !strings.Contains(after[filepath.Join(root, "working", "OPEN_LOOPS.md")], marker) {
		t.Fatalf(
			"second Init overwrote OPEN_LOOPS.md:\n%s",
			after[filepath.Join(root, "working", "OPEN_LOOPS.md")],
		)
	}
	if !strings.Contains(after[filepath.Join(root, "working", "LEDGER.md")], marker) {
		t.Fatalf(
			"second Init overwrote LEDGER.md:\n%s",
			after[filepath.Join(root, "working", "LEDGER.md")],
		)
	}
}

func snapshotNewMemoryPaths(t *testing.T, root string) map[string]string {
	t.Helper()

	out := map[string]string{}
	for _, path := range []string{
		filepath.Join(root, "working", "OPEN_LOOPS.md"),
		filepath.Join(root, "working", "LEDGER.md"),
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile(%q) error = %v", path, err)
		}
		out[path] = string(raw)
	}
	return out
}

func TestStoreRotatesOversizedLedger(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory")
	store := newTestStore(root, &fakeCommitter{})
	ctx := context.Background()

	if err := store.Init(ctx); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	ledgerPath := filepath.Join(root, "working", "LEDGER.md")
	oversized := strings.Repeat(
		"2026-10-14T05:02Z src=t4391..t4414 run=semantic_memory.extract\n",
		6144,
	)
	if int64(len(oversized)) <= ledgerRotationThresholdByte {
		t.Fatalf("fixture is %d bytes, want > %d", len(oversized), ledgerRotationThresholdByte)
	}
	if err := os.WriteFile(ledgerPath, []byte(oversized), 0o644); err != nil {
		t.Fatalf("WriteFile(ledger fixture) error = %v", err)
	}

	rotatedAt := time.Date(2026, time.October, 14, 5, 2, 0, 0, time.UTC)
	if err := store.rotateWorkingLedger(rotatedAt); err != nil {
		t.Fatalf("rotateWorkingLedger() error = %v", err)
	}

	archivePath := filepath.Join(root, "working", "ledger", "2026-Q4.md")
	archived, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", archivePath, err)
	}
	if string(archived) != oversized {
		t.Fatalf("archived ledger length = %d, want %d", len(archived), len(oversized))
	}

	fresh, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", ledgerPath, err)
	}
	seeded, err := os.ReadFile(filepath.Join(root, "working", "LEDGER.md"))
	if err != nil {
		t.Fatalf("ReadFile(LEDGER.md) error = %v", err)
	}
	if strings.Contains(string(fresh), "src=t4391") {
		t.Fatalf("fresh ledger retained rotated entries:\n%s", string(fresh))
	}
	if !strings.Contains(string(fresh), "Conversion Ledger") {
		t.Fatalf("fresh ledger lost its header:\n%s", string(fresh))
	}
	if string(fresh) != string(seeded) {
		t.Fatalf("fresh ledger = %q, want seeded header %q", string(fresh), string(seeded))
	}

	info, err := os.Stat(ledgerPath)
	if err != nil {
		t.Fatalf("Stat(LEDGER.md) error = %v", err)
	}
	if info.Size() > ledgerRotationThresholdByte {
		t.Fatalf("fresh ledger size = %d, want <= %d", info.Size(), ledgerRotationThresholdByte)
	}

	// A small ledger must not rotate on the next run.
	if err := store.rotateWorkingLedger(rotatedAt); err != nil {
		t.Fatalf("second rotateWorkingLedger() error = %v", err)
	}
	afterSecond, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", archivePath, err)
	}
	if string(afterSecond) != oversized {
		t.Fatalf(
			"second rotation changed the archive, length = %d, want %d",
			len(afterSecond),
			len(oversized),
		)
	}
}

func TestStoreInitRotatesOversizedLedger(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory")
	store := newTestStore(root, &fakeCommitter{})
	ctx := context.Background()

	if err := store.Init(ctx); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	ledgerPath := filepath.Join(root, "working", "LEDGER.md")
	oversized := strings.Repeat("moved \"follow up on <thing>\" -> l-2026-0031\n", 8192)
	if err := os.WriteFile(ledgerPath, []byte(oversized), 0o644); err != nil {
		t.Fatalf("WriteFile(ledger fixture) error = %v", err)
	}

	if err := store.Init(ctx); err != nil {
		t.Fatalf("second Init() error = %v", err)
	}

	matches, err := filepath.Glob(filepath.Join(root, "working", "ledger", "*.md"))
	if err != nil {
		t.Fatalf("Glob(ledger archives) error = %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("ledger archives = %v, want exactly one", matches)
	}
	archived, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", matches[0], err)
	}
	if string(archived) != oversized {
		t.Fatalf("archived ledger length = %d, want %d", len(archived), len(oversized))
	}

	fresh, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatalf("ReadFile(LEDGER.md) error = %v", err)
	}
	if strings.Contains(string(fresh), "l-2026-0031") {
		t.Fatalf("Init left rotated entries in the fresh ledger:\n%s", string(fresh))
	}
}
