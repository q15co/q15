package app

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/cognition"
	"github.com/q15co/q15/systems/agent/internal/fileops"
	"github.com/q15co/q15/systems/agent/internal/memory"
	"github.com/q15co/q15/systems/agent/internal/memoryrepo"
)

const repairRecoveryTarget = "semantic/legacy/nested/note.md"

func TestRepairRunnerOwnsGuardedLedgerAndRecoversFailedWrites(t *testing.T) {
	root, store := newCognitionRecoveryStore(t)
	before := prepareRepairRecoveryTarget(t, root, store)
	executor := fileops.NewGuardedExecutor(fileops.Settings{
		MemoryLocalDir: root, MemoryRuntimeDir: "/memory",
	}, store.memory)
	called := false
	model := recoveryModelClient{
		complete: func(ctx context.Context) (agent.ModelClientResult, error) {
			called = true
			if cognition.MemoryRecoveryOwner(ctx) == "" {
				t.Fatal("repair runner supplied no recovery ownership")
			}
			for _, relative := range []string{repairRecoveryTarget, "working/LEDGER.md", "semantic/topics/new.md"} {
				if _, err := executor.WriteFile(ctx, "/memory/"+relative, "partial repair\n"); err != nil {
					t.Fatalf("guarded repair write %s: %v", relative, err)
				}
			}
			if _, err := executor.WriteFile(context.Background(), "/memory/"+repairRecoveryTarget, "unowned edit\n"); err == nil {
				t.Fatal("concurrent unowned tool changed the repair target")
			}
			return agent.ModelClientResult{}, fmt.Errorf("injected repair failure")
		},
	}
	runner := cognition.NewRunnerWithEngine(agent.NewEngine(model, nil, []string{"test"}), store)
	if _, err := runner.Run(context.Background(), cognition.NewSemanticMemoryExtractionRegistration(0, 0).NewJob(), nil); err == nil {
		t.Fatal("runner accepted injected failure")
	}
	if !called {
		t.Fatal("repair never reached the model")
	}
	assertRepairRecoveryBaseline(t, root, store.memory, before)
}

func TestRepairRecoveryStartupAfterProcessExit(t *testing.T) {
	if root := os.Getenv("Q15_REPAIR_RECOVERY_TEST_ROOT"); root != "" {
		mem := memory.NewStore(memoryrepo.New(root, memoryrepo.NewGitCommitter()), "test")
		store := &runtimeStore{memory: mem}
		executor := fileops.NewGuardedExecutor(fileops.Settings{
			MemoryLocalDir: root, MemoryRuntimeDir: "/memory",
		}, mem)
		model := recoveryModelClient{
			complete: func(ctx context.Context) (agent.ModelClientResult, error) {
				for _, relative := range []string{repairRecoveryTarget, "working/LEDGER.md", "semantic/topics/new.md"} {
					if _, err := executor.WriteFile(ctx, "/memory/"+relative, "interrupted repair\n"); err != nil {
						t.Fatal(err)
					}
				}
				if os.Getenv("Q15_REPAIR_RECOVERY_TEST_DELETE") == "yes" {
					if err := store.DeleteFile(ctx, repairRecoveryTarget); err != nil {
						t.Fatal(err)
					}
				}
				os.Exit(0)
				return agent.ModelClientResult{}, nil
			},
		}
		runner := cognition.NewRunnerWithEngine(
			agent.NewEngine(model, nil, []string{"test"}),
			store,
		)
		if _, err := runner.Run(context.Background(), cognition.NewSemanticMemoryExtractionRegistration(0, 0).NewJob(), nil); err != nil {
			t.Fatal(err)
		}
		t.Fatal("child did not exit in the interrupted run")
	}
	for _, mode := range []string{"edited", "deleted"} {
		t.Run(mode, func(t *testing.T) {
			root, store := newCognitionRecoveryStore(t)
			before := prepareRepairRecoveryTarget(t, root, store)
			child := exec.Command(
				os.Args[0],
				"-test.run=^TestRepairRecoveryStartupAfterProcessExit$",
			)
			child.Env = append(os.Environ(), "Q15_REPAIR_RECOVERY_TEST_ROOT="+root)
			if mode == "deleted" {
				child.Env = append(child.Env, "Q15_REPAIR_RECOVERY_TEST_DELETE=yes")
			}
			if output, err := child.CombinedOutput(); err != nil {
				t.Fatalf("interrupted repair child: %v (%s)", err, output)
			}
			if _, err := os.Stat(filepath.Join(root, ".git/q15-memory-recovery.json")); err != nil {
				t.Fatalf("interrupted repair left no journal: %v", err)
			}
			fresh := memory.NewStore(memoryrepo.New(root, memoryrepo.NewGitCommitter()), "test")
			if err := fresh.Init(context.Background()); err != nil {
				t.Fatalf("fresh-store recovery: %v", err)
			}
			assertRepairRecoveryBaseline(t, root, fresh, before)
		})
	}
}

func prepareRepairRecoveryTarget(t *testing.T, root string, store *runtimeStore) map[string][]byte {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, repairRecoveryTarget)), 0o700); err != nil {
		t.Fatal(err)
	}
	content := []byte("A durable legacy fact.\n")
	if err := store.WriteFileBytes(context.Background(), repairRecoveryTarget, content); err != nil {
		t.Fatal(err)
	}
	before := cognitionRecoveryBytes(t, store)
	before[repairRecoveryTarget] = content
	return before
}

func assertRepairRecoveryBaseline(
	t *testing.T,
	root string,
	store *memory.Store,
	before map[string][]byte,
) {
	t.Helper()
	for relative, want := range before {
		got, exists, err := store.ReadFileBytes(context.Background(), relative)
		if err != nil || !exists || !bytes.Equal(got, want) {
			t.Errorf(
				"recovered %s = %q, exists=%v err=%v; want %q",
				relative,
				got,
				exists,
				err,
				want,
			)
		}
	}
	if _, exists, err := store.ReadFileBytes(context.Background(), "semantic/topics/new.md"); err != nil ||
		exists {
		t.Fatalf("new topic survived repair recovery: exists=%v err=%v", exists, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".git/q15-memory-recovery.json")); !os.IsNotExist(
		err,
	) {
		t.Fatalf("successful repair recovery retained its journal: %v", err)
	}
	assertCognitionRecoveryGitClean(t, root)
}
