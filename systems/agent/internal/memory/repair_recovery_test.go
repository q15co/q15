package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/cognition"
)

func TestMemoryRepairRecoveryAllowsOnlyItsExactExtraTarget(t *testing.T) {
	for _, target := range []string{"semantic/legacy/deep/note.md", "working/archive/note.md"} {
		t.Run(target, func(t *testing.T) {
			root, store, _ := newRecoveryTestStore(t)
			if err := os.MkdirAll(filepath.Dir(filepath.Join(root, target)), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := store.WriteFileBytes(context.Background(), target, []byte("legacy\n")); err != nil {
				t.Fatal(err)
			}
			snapshots := recoveryTestSnapshots(t, store)
			snapshots["/memory/"+target] = cognition.FileSnapshot{
				Existed: true,
				Content: []byte("legacy\n"),
			}
			if _, err := store.BeginMemoryRecovery(context.Background(), snapshots, semanticTopicsRelativePath); err == nil {
				t.Fatal("conversion recovery accepted an unnamed extra target")
			}
			id, err := store.BeginMemoryRepairRecovery(context.Background(), snapshots, target)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.AcquireFileMutation(context.Background(), []string{filepath.Join(root, target)}); err == nil {
				t.Fatal("unowned mutation acquired the reserved repair target")
			}
			if err := store.RecoverMemory(context.Background(), id); err != nil {
				t.Fatal(err)
			}
			assertRecoveryFiles(t, store, snapshots)
		})
	}
}

func TestMemoryRepairRecoveryRejectsTargetsOutsideContentAndLedgerHistory(t *testing.T) {
	_, store, _ := newRecoveryTestStore(t)
	for _, target := range []string{
		"", "../semantic/legacy.md", "/semantic/legacy.md", "semantic/../working/note.md",
		".git/config", "cognition/review.md", "working/LEDGER.md", "working/ledger/old.md",
	} {
		if _, err := store.BeginMemoryRepairRecovery(context.Background(), recoveryTestSnapshots(t, store), target); err == nil {
			t.Errorf("repair recovery accepted target %q", target)
		}
	}
}

func TestMemoryRepairRecoveryRejectsUnrelatedSnapshots(t *testing.T) {
	_, store, _ := newRecoveryTestStore(t)
	snapshots := recoveryTestSnapshots(t, store)
	snapshots["/memory/semantic/legacy.md"] = cognition.FileSnapshot{}
	snapshots["/memory/working/unrelated.md"] = cognition.FileSnapshot{}
	if _, err := store.BeginMemoryRepairRecovery(context.Background(), snapshots, "semantic/legacy.md"); err == nil {
		t.Fatal("repair recovery accepted a second unnamed target")
	}
}

func TestMemoryRepairRecoveryRejectsChangedTargetBeforeJournalCreation(t *testing.T) {
	root, store, _ := newRecoveryTestStore(t)
	const target = "semantic/legacy.md"
	if err := store.WriteFileBytes(context.Background(), target, []byte("before\n")); err != nil {
		t.Fatal(err)
	}
	snapshots := recoveryTestSnapshots(t, store)
	snapshots["/memory/"+target] = cognition.FileSnapshot{
		Existed: true,
		Content: []byte("before\n"),
	}
	if err := store.WriteFileBytes(context.Background(), target, []byte("changed\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginMemoryRepairRecovery(context.Background(), snapshots, target); err == nil ||
		!strings.Contains(err.Error(), "baseline changed") {
		t.Fatalf("BeginMemoryRepairRecovery = %v; want changed-baseline rejection", err)
	}
	if _, err := os.Stat(filepath.Join(root, memoryRecoveryPath)); !os.IsNotExist(err) {
		t.Fatalf("rejected baseline left a recovery journal: %v", err)
	}
}
