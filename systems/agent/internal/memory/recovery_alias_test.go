package memory

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/fileops"
	"github.com/q15co/q15/systems/agent/internal/memoryrepo"
)

func TestMutationGuardUsesAtomicReplacementPath(t *testing.T) {
	for _, reserved := range []bool{false, true} {
		name := "unrelated leaf points to reserved file"
		if reserved {
			name = "reserved leaf points to unrelated file"
		}
		t.Run(name, func(t *testing.T) {
			root, store, _ := newRecoveryTestStore(t)
			before, _, err := store.ReadFileBytes(context.Background(), "working/WORKING_MEMORY.md")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.BeginMemoryRecovery(context.Background(), recoveryTestSnapshots(t, store), semanticTopicsRelativePath); err != nil {
				t.Fatal(err)
			}
			relative := "notes/inbox/alias.md"
			if reserved {
				if err := os.WriteFile(filepath.Join(root, "notes/inbox/target.md"), before, 0o600); err != nil {
					t.Fatal(err)
				}
				relative = "working/WORKING_MEMORY.md"
				if err := os.Remove(filepath.Join(root, relative)); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("../notes/inbox/target.md", filepath.Join(root, relative)); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Symlink("../../working/WORKING_MEMORY.md", filepath.Join(root, relative)); err != nil {
				t.Fatal(err)
			}
			executor := fileops.NewGuardedExecutor(
				fileops.Settings{MemoryLocalDir: root, MemoryRuntimeDir: "/memory"},
				store,
			)
			_, err = executor.WriteFile(
				context.Background(),
				"/memory/"+relative,
				"independent note\n",
			)
			if reserved {
				if !isRecoveryOwnershipRejection(err) {
					t.Fatalf("reserved leaf mutation=%v; want ownership rejection", err)
				}
			} else if err != nil {
				t.Fatalf("replacing unrelated alias: %v", err)
			}
			after, _, err := store.ReadFileBytes(context.Background(), "working/WORKING_MEMORY.md")
			if err != nil || !bytes.Equal(after, before) {
				t.Fatalf("reserved bytes changed: %q, %v", after, err)
			}
			if !reserved {
				if got, err := os.ReadFile(filepath.Join(root, relative)); err != nil ||
					string(got) != "independent note\n" {
					t.Fatalf("unrelated alias replacement=%q, %v", got, err)
				}
			}
		})
	}
}

func TestBeginMemoryRecoveryRejectsReservedPathAliasesWithoutJournal(t *testing.T) {
	for _, relative := range []string{"working/WORKING_MEMORY.md", "working", "semantic", "semantic/topics"} {
		t.Run(relative, func(t *testing.T) {
			root, store, _ := newRecoveryTestStore(t)
			source := filepath.Join(root, filepath.FromSlash(relative))
			target := filepath.Join(root, "notes/inbox/reserved-target")
			if err := os.Rename(source, target); err != nil {
				t.Fatal(err)
			}
			linkTarget, err := filepath.Rel(filepath.Dir(source), target)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(linkTarget, source); err != nil {
				t.Fatal(err)
			}
			if _, err := store.BeginMemoryRecovery(context.Background(), recoveryTestSnapshots(t, store), semanticTopicsRelativePath); err == nil ||
				!strings.Contains(err.Error(), "contains a symlink") {
				t.Fatalf("BeginMemoryRecovery()=%v; want reserved alias rejection", err)
			}
			if _, err := os.Stat(store.memoryRecoveryFile()); !os.IsNotExist(err) {
				t.Fatalf("rejected alias created journal: %v", err)
			}
		})
	}
}

func TestBeginMemoryRecoveryRejectsDanglingTopicDirectoryAlias(t *testing.T) {
	root, store, _ := newRecoveryTestStore(t)
	topics := filepath.Join(root, filepath.FromSlash(semanticTopicsRelativePath))
	if err := os.Remove(topics); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../notes/missing-topics", topics); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginMemoryRecovery(context.Background(), recoveryTestSnapshots(t, store), semanticTopicsRelativePath); err == nil ||
		!strings.Contains(err.Error(), "contains a symlink") {
		t.Fatalf("BeginMemoryRecovery()=%v; want dangling topic alias rejection", err)
	}
	if _, err := os.Stat(store.memoryRecoveryFile()); !os.IsNotExist(err) {
		t.Fatalf("rejected topic alias created journal: %v", err)
	}
}

func TestBeginMemoryRecoveryAllowsRootAliasAndMissingTopicDirectory(t *testing.T) {
	root, _, committer := newRecoveryTestStore(t)
	alias := filepath.Join(t.TempDir(), "memory-alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	store := NewStore(memoryrepo.New(alias, committer), "test")
	if err := store.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, filepath.FromSlash(semanticTopicsRelativePath))); err != nil {
		t.Fatal(err)
	}
	snapshots := recoveryTestSnapshots(t, store)
	delete(snapshots, "/memory/semantic/topics/missing.md")
	id, err := store.BeginMemoryRecovery(
		context.Background(),
		snapshots,
		semanticTopicsRelativePath,
	)
	if err != nil {
		t.Fatalf("BeginMemoryRecovery(root alias, missing topics): %v", err)
	}
	executor := fileops.NewGuardedExecutor(
		fileops.Settings{MemoryLocalDir: alias, MemoryRuntimeDir: "/memory"},
		store,
	)
	if _, err := executor.WriteFile(context.Background(), "/memory/working/WORKING_MEMORY.md", "unowned write\n"); !isRecoveryOwnershipRejection(
		err,
	) {
		t.Fatalf("root alias bypassed ownership: %v", err)
	}
	if _, err := executor.WriteFile(context.Background(), "/memory/notes/inbox/independent.md", "independent note\n"); err != nil {
		t.Fatalf("unrelated write through root alias: %v", err)
	}
	if err := store.RecoverMemory(context.Background(), id); err != nil {
		t.Fatal(err)
	}
}

func isRecoveryOwnershipRejection(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "reserved by a cognition run") ||
		strings.Contains(err.Error(), "managed by cognition jobs"))
}
