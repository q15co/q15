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
			"/memory/working/WORKING_MEMORY.md",
			"foreground task\n",
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
	if raw, err := os.ReadFile(filepath.Join(root, "working/WORKING_MEMORY.md")); err != nil ||
		string(raw) != "foreground task\n" {
		t.Fatalf("foreground task lost: %q, %v", raw, err)
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
