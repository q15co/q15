package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/q15co/q15/systems/agent/internal/fileops"
)

type pausedMutationGuard struct {
	store    *Store
	acquired chan struct{}
	proceed  chan struct{}
}

func (g pausedMutationGuard) AcquireFileMutation(
	ctx context.Context,
	paths []string,
) (func(), error) {
	release, err := g.store.AcquireFileMutation(ctx, paths)
	if err != nil {
		return nil, err
	}
	close(g.acquired)
	<-g.proceed
	return release, nil
}

func TestMutationLeaseSerializesJournalCreationWithActualWrite(t *testing.T) {
	root, store, _ := newRecoveryTestStore(t)
	snapshots := recoveryTestSnapshots(t, store)
	guard := pausedMutationGuard{
		store:    store,
		acquired: make(chan struct{}),
		proceed:  make(chan struct{}),
	}
	executor := fileops.NewGuardedExecutor(
		fileops.Settings{MemoryLocalDir: root, MemoryRuntimeDir: "/memory"},
		guard,
	)
	writeDone := make(chan error, 1)
	go func() {
		_, err := executor.WriteFile(
			context.Background(),
			"/memory/semantic/facts.md",
			"foreground semantic update\n",
		)
		writeDone <- err
	}()
	<-guard.acquired
	beginDone := make(chan error, 1)
	go func() {
		_, err := store.BeginMemoryRecovery(
			context.Background(),
			snapshots,
			semanticTopicsRelativePath,
		)
		beginDone <- err
	}()
	select {
	case err := <-beginDone:
		t.Fatalf("journal creation ran inside a file mutation: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(guard.proceed)
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
	if err := <-beginDone; err == nil || !strings.Contains(err.Error(), "baseline changed") {
		t.Fatalf("BeginMemoryRecovery = %v, want changed-baseline rejection", err)
	}
	if raw, err := os.ReadFile(filepath.Join(root, "semantic/facts.md")); err != nil ||
		string(raw) != "foreground semantic update\n" {
		t.Fatalf("foreground semantic update lost: %q, %v", raw, err)
	}
	if _, err := os.Stat(store.memoryRecoveryFile()); !os.IsNotExist(err) {
		t.Fatalf("rejected baseline created journal: %v", err)
	}
}

func TestMutationLeaseSerializesRecoveryWithUnrelatedWrite(t *testing.T) {
	root, store, _ := newRecoveryTestStore(t)
	snapshots := recoveryTestSnapshots(t, store)
	id, err := store.BeginMemoryRecovery(
		context.Background(),
		snapshots,
		semanticTopicsRelativePath,
	)
	if err != nil {
		t.Fatal(err)
	}
	corruptRecoveryFiles(t, root, snapshots)
	guard := pausedMutationGuard{
		store:    store,
		acquired: make(chan struct{}),
		proceed:  make(chan struct{}),
	}
	executor := fileops.NewGuardedExecutor(
		fileops.Settings{MemoryLocalDir: root, MemoryRuntimeDir: "/memory"},
		guard,
	)
	writeDone := make(chan error, 1)
	go func() {
		_, err := executor.WriteFile(
			context.Background(),
			"/memory/notes/inbox/concurrent.md",
			"independent note\n",
		)
		writeDone <- err
	}()
	<-guard.acquired
	recoverDone := make(chan error, 1)
	go func() { recoverDone <- store.RecoverMemory(context.Background(), id) }()
	select {
	case err := <-recoverDone:
		t.Fatalf("recovery ran inside a file mutation: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(guard.proceed)
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
	if err := <-recoverDone; err != nil {
		t.Fatal(err)
	}
	assertRecoveryFiles(t, store, snapshots)
	if raw, err := os.ReadFile(filepath.Join(root, "notes/inbox/concurrent.md")); err != nil ||
		string(raw) != "independent note\n" {
		t.Fatalf("unrelated note lost: %q, %v", raw, err)
	}
}

func TestMutationGuardLeavesPathsOutsideWorkingJournalWritable(t *testing.T) {
	root, store, _ := newRecoveryTestStore(t)
	snapshots := recoveryTestSnapshots(t, store)
	for runtimePath := range snapshots {
		if !strings.HasPrefix(runtimePath, "/memory/working/") {
			delete(snapshots, runtimePath)
		}
	}
	id, err := store.BeginMemoryRecovery(context.Background(), snapshots, "")
	if err != nil {
		t.Fatal(err)
	}
	executor := fileops.NewGuardedExecutor(
		fileops.Settings{MemoryLocalDir: root, MemoryRuntimeDir: "/memory"},
		store,
	)
	if _, err := executor.WriteFile(context.Background(), "/memory/semantic/topics/unrelated.txt", "independent semantic update\n"); err != nil {
		t.Fatal(err)
	}
	if err := store.RecoverMemory(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(filepath.Join(root, "semantic/topics/unrelated.txt")); err != nil ||
		string(raw) != "independent semantic update\n" {
		t.Fatalf("working recovery changed independent topic: %q, %v", raw, err)
	}
}

func TestWorkingFilesRejectUnownedMutationsEvenWhileIdle(t *testing.T) {
	root, store, _ := newRecoveryTestStore(t)
	for relative, content := range map[string]string{
		"working/WORKING_MEMORY.md": "# Working Memory\n\n- Preserve the active task.\n",
		"working/OPEN_LOOPS.md":     "# Open Loops\n\n- [l-2026-0042] Preserve the follow-up.\n  {trigger: next-session, owner: agent, src: t42}\n",
		"working/LEDGER.md":         "# Conversion Ledger\n\n2026-10-16T04:00Z run=working_memory.consolidate src=t42 dropped \"Previous task.\" (completed)\n",
	} {
		if err := store.WriteFileBytes(context.Background(), relative, []byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	executor := fileops.NewGuardedExecutor(
		fileops.Settings{MemoryLocalDir: root, MemoryRuntimeDir: "/memory"},
		store,
	)
	for _, relative := range []string{"working/WORKING_MEMORY.md", "working/OPEN_LOOPS.md", "working/LEDGER.md"} {
		before, err := os.ReadFile(filepath.Join(root, relative))
		if err != nil {
			t.Fatal(err)
		}
		for _, operation := range []string{"overwrite", "delete"} {
			var err error
			if operation == "overwrite" {
				_, err = executor.WriteFile(context.Background(), "/memory/"+relative, "")
			} else {
				_, err = executor.ApplyPatch(context.Background(), "*** Begin Patch\n*** Delete File: /memory/"+relative+"\n*** End Patch")
			}
			if err == nil || !strings.Contains(err.Error(), "managed by cognition jobs") {
				t.Fatalf("%s %s = %v, want framework ownership rejection", operation, relative, err)
			}
			after, readErr := os.ReadFile(filepath.Join(root, relative))
			if readErr != nil || string(after) != string(before) {
				t.Fatalf("rejected %s changed %s: %q, %v", operation, relative, after, readErr)
			}
		}
	}
	for _, relative := range []string{"semantic/facts.md", "notes/inbox/editable.md"} {
		if _, err := executor.WriteFile(context.Background(), "/memory/"+relative, "editable memory\n"); err != nil {
			t.Fatalf("idle write %s = %v", relative, err)
		}
	}
}
