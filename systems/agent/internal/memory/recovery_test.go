package memory

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/cognition"
	"github.com/q15co/q15/systems/agent/internal/memoryrepo"
)

type recoveryCommitter struct {
	memoryrepo.Committer
	fail bool
}

func (c *recoveryCommitter) CommitPaths(
	ctx context.Context,
	root, message string,
	paths []string,
) error {
	if c.fail {
		return fmt.Errorf("injected Git failure")
	}
	return c.Committer.CommitPaths(ctx, root, message, paths)
}

func TestMemoryRecoveryRestoresEveryFileAfterGitFailureAndRetries(t *testing.T) {
	root, store, committer := newRecoveryTestStore(t)
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
	committer.fail = true
	if err := store.RecoverMemory(context.Background(), id); err == nil ||
		!strings.Contains(err.Error(), "injected Git failure") {
		t.Fatalf("RecoverMemory error = %v, want Git failure", err)
	}
	assertRecoveryFiles(t, store, snapshots)
	if _, err := store.BeginMemoryRecovery(context.Background(), snapshots, semanticTopicsRelativePath); err == nil {
		t.Fatal("BeginMemoryRecovery accepted a pending failed recovery")
	}
	if _, err := os.Stat(filepath.Join(root, memoryRecoveryPath)); err != nil {
		t.Fatalf("failed recovery lost its journal: %v", err)
	}
	committer.fail = false
	if err := store.RecoverMemory(context.Background(), id); err != nil {
		t.Fatalf("retry recovery: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, memoryRecoveryPath)); !os.IsNotExist(err) {
		t.Fatalf("successful recovery retained journal: %v", err)
	}
	assertRecoveryFiles(t, store, snapshots)
	assertRecoveryGitClean(t, root)
}

func TestMemoryRecoveryContinuesAfterFilesystemFailure(t *testing.T) {
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
	blocked := filepath.Join(root, semanticFactsRelativePath)
	if err := os.Remove(blocked); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(blocked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blocked, "obstacle"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.RecoverMemory(context.Background(), id); err == nil ||
		!strings.Contains(err.Error(), semanticFactsRelativePath) {
		t.Fatalf("RecoverMemory error = %v, want blocked path", err)
	}
	for runtimePath, snapshot := range snapshots {
		if runtimePath == "/memory/"+semanticFactsRelativePath {
			continue
		}
		assertRecoveryFiles(t, store, map[string]cognition.FileSnapshot{runtimePath: snapshot})
	}
	if err := os.RemoveAll(blocked); err != nil {
		t.Fatal(err)
	}
	if err := store.RecoverMemory(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	assertRecoveryFiles(t, store, snapshots)
	assertRecoveryGitClean(t, root)
}

func TestMemoryRecoveryRetriesStagedDeletionAfterCommitHookFailure(t *testing.T) {
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
	const newTopic = "semantic/topics/created.md"
	if err := store.WriteFileBytes(context.Background(), newTopic, []byte("partial topic\n")); err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(root, ".git/hooks/pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := store.RecoverMemory(context.Background(), id); err == nil {
		t.Fatal("recovery ignored failing Git commit hook")
	}
	if _, err := os.Stat(filepath.Join(root, newTopic)); !os.IsNotExist(err) {
		t.Fatalf("failed commit prevented filesystem deletion: %v", err)
	}
	if err := os.Remove(hook); err != nil {
		t.Fatal(err)
	}
	if err := store.RecoverMemory(context.Background(), id); err != nil {
		t.Fatalf("retry staged deletion: %v", err)
	}
	assertRecoveryGitClean(t, root)
	if err := exec.Command("git", "-C", root, "cat-file", "-e", "HEAD:"+newTopic).Run(); err == nil {
		t.Fatal("recovery retired its journal with the created topic still in Git HEAD")
	}
}

func TestMemoryRecoveryStartupAfterProcessExit(t *testing.T) {
	if root := os.Getenv("Q15_RECOVERY_TEST_ROOT"); root != "" {
		store := newTestStore(root, memoryrepo.NewGitCommitter())
		snapshots := recoveryTestSnapshots(t, store)
		if _, err := store.BeginMemoryRecovery(context.Background(), snapshots, semanticTopicsRelativePath); err != nil {
			t.Fatal(err)
		}
		corruptRecoveryFiles(t, root, snapshots)
		os.Exit(0)
	}
	root, store, _ := newRecoveryTestStore(t)
	snapshots := recoveryTestSnapshots(t, store)
	child := exec.Command(os.Args[0], "-test.run=^TestMemoryRecoveryStartupAfterProcessExit$")
	child.Env = append(os.Environ(), "Q15_RECOVERY_TEST_ROOT="+root)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("interrupted child: %v (%s)", err, output)
	}
	if _, err := os.Stat(filepath.Join(root, memoryRecoveryPath)); err != nil {
		t.Fatalf("child left no durable journal: %v", err)
	}
	fresh := newTestStore(root, memoryrepo.NewGitCommitter())
	if err := fresh.Init(context.Background()); err != nil {
		t.Fatalf("restart recovery: %v", err)
	}
	assertRecoveryFiles(t, fresh, snapshots)
	if _, err := os.Stat(filepath.Join(root, "semantic/topics/new-topic.txt")); !os.IsNotExist(
		err,
	) {
		t.Fatalf("new topic survived recovery: %v", err)
	}
	assertRecoveryGitClean(t, root)
}

func TestMemoryRecoveryCompletionKeepsValidatedFilesOnRestart(t *testing.T) {
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
	validated := []byte("# Validated facts\n")
	if err := os.WriteFile(filepath.Join(root, semanticFactsRelativePath), validated, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteMemoryRecovery(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	fresh := newTestStore(root, memoryrepo.NewGitCommitter())
	if err := fresh.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	data, _, err := fresh.ReadFileBytes(context.Background(), semanticFactsRelativePath)
	if err != nil || !bytes.Equal(data, validated) {
		t.Fatalf("validated data after restart = %q, error = %v", data, err)
	}
	assertRecoveryGitClean(t, root)
}

func TestMemoryRecoveryMalformedJournalBlocksStartup(t *testing.T) {
	root, store, _ := newRecoveryTestStore(t)
	before, _, err := store.ReadFileBytes(context.Background(), "working/WORKING_MEMORY.md")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, memoryRecoveryPath), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Init(context.Background()); err == nil {
		t.Fatal("Init accepted an unreadable recovery journal")
	}
	after, _, err := store.ReadFileBytes(context.Background(), "working/WORKING_MEMORY.md")
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("failed Init changed working memory: %q, %v", after, err)
	}
}

func newRecoveryTestStore(t *testing.T) (string, *Store, *recoveryCommitter) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "memory")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	committer := &recoveryCommitter{Committer: memoryrepo.NewGitCommitter()}
	store := newTestStore(root, committer)
	if err := store.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	return root, store, committer
}

func recoveryTestSnapshots(t *testing.T, store *Store) map[string]cognition.FileSnapshot {
	t.Helper()
	snapshots := make(map[string]cognition.FileSnapshot)
	for _, relative := range []string{
		"working/WORKING_MEMORY.md", "working/OPEN_LOOPS.md", "working/LEDGER.md",
		semanticFactsRelativePath, semanticPreferencesRelativePath, semanticProjectsRelativePath,
		"semantic/topics/missing.md",
	} {
		content, exists, err := store.ReadFileBytes(context.Background(), relative)
		if err != nil {
			t.Fatal(err)
		}
		snapshots["/memory/"+relative] = cognition.FileSnapshot{Existed: exists, Content: content}
	}
	return snapshots
}

func corruptRecoveryFiles(t *testing.T, root string, snapshots map[string]cognition.FileSnapshot) {
	t.Helper()
	for runtimePath := range snapshots {
		relative := strings.TrimPrefix(runtimePath, "/memory/")
		if err := os.WriteFile(filepath.Join(root, relative), []byte("partial\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "semantic/topics/new-topic.txt"), []byte("partial topic\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertRecoveryFiles(t *testing.T, store *Store, snapshots map[string]cognition.FileSnapshot) {
	t.Helper()
	for runtimePath, snapshot := range snapshots {
		data, exists, err := store.ReadFileBytes(
			context.Background(),
			strings.TrimPrefix(runtimePath, "/memory/"),
		)
		if err != nil || exists != snapshot.Existed || !bytes.Equal(data, snapshot.Content) {
			t.Errorf("restored %s = %q, exists=%v, err=%v; want %q, exists=%v",
				runtimePath, data, exists, err, snapshot.Content, snapshot.Existed)
		}
	}
}

func assertRecoveryGitClean(t *testing.T, root string) {
	t.Helper()
	output, err := exec.Command("git", "-C", root, "status", "--porcelain").CombinedOutput()
	if err != nil || len(bytes.TrimSpace(output)) != 0 {
		t.Fatalf("recovered Git worktree is dirty: %s, %v", output, err)
	}
}
