package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/cognition"
	"github.com/q15co/q15/systems/agent/internal/memory"
	"github.com/q15co/q15/systems/agent/internal/memory/claims"
	"github.com/q15co/q15/systems/agent/internal/memoryrepo"
)

type adoptionFaultStore struct {
	*runtimeStore
	mode   string
	cancel context.CancelFunc
}

func (s *adoptionFaultStore) WriteFileBytes(
	ctx context.Context,
	relative string,
	data []byte,
) error {
	if err := s.runtimeStore.WriteFileBytes(ctx, relative, data); err != nil {
		return err
	}
	if s.mode == "ledger after write" && relative == "working/LEDGER.md" {
		return errors.New("injected ledger error after writing")
	}
	return nil
}

func (s *adoptionFaultStore) DeleteFile(ctx context.Context, relative string) error {
	if relative != repairRecoveryTarget {
		return s.runtimeStore.DeleteFile(ctx, relative)
	}
	switch s.mode {
	case "process exit":
		os.Exit(0)
	case "delete before write":
		return errors.New("injected delete error")
	case "cancelled delete":
		s.cancel()
		return ctx.Err()
	}
	if err := s.runtimeStore.DeleteFile(ctx, relative); err != nil {
		return err
	}
	if s.mode == "delete after write" {
		return errors.New("injected delete error after removing")
	}
	return nil
}

func (s *adoptionFaultStore) CompleteMemoryRecovery(ctx context.Context, id string) error {
	if s.mode == "completion" {
		return errors.New("injected completion error")
	}
	return s.runtimeStore.CompleteMemoryRecovery(ctx, id)
}

func TestUnreadableAdoptionRecoversEveryBuildFailure(t *testing.T) {
	for _, mode := range []string{
		"ledger after write", "delete before write", "delete after write", "cancelled delete", "completion",
	} {
		t.Run(mode, func(t *testing.T) {
			root, store := newCognitionRecoveryStore(t)
			before := prepareUnreadableAdoption(t, root, store)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			fault := &adoptionFaultStore{runtimeStore: store, mode: mode, cancel: cancel}
			job := cognition.NewSemanticMemoryExtractionRegistration(0, 0).NewJob()
			if _, err := cognition.NewRunner(nil, nil, nil, fault).Run(ctx, job, nil); err == nil {
				t.Fatal("runtime-only adoption accepted injected failure")
			}
			assertRepairRecoveryBaseline(t, root, store.memory, before)
		})
	}
}

func TestUnreadableAdoptionRecoversAfterProcessExit(t *testing.T) {
	if root := os.Getenv("Q15_UNREADABLE_ADOPTION_ROOT"); root != "" {
		mem := memory.NewStore(memoryrepo.New(root, memoryrepo.NewGitCommitter()), "test")
		fault := &adoptionFaultStore{runtimeStore: &runtimeStore{memory: mem}, mode: "process exit"}
		job := cognition.NewSemanticMemoryExtractionRegistration(0, 0).NewJob()
		if _, err := job.Build(context.Background(), fault); err != nil {
			t.Fatal(err)
		}
		t.Fatal("child did not exit between the adoption record and deletion")
	}
	root, store := newCognitionRecoveryStore(t)
	before := prepareUnreadableAdoption(t, root, store)
	child := exec.Command(os.Args[0], "-test.run=^TestUnreadableAdoptionRecoversAfterProcessExit$")
	child.Env = append(os.Environ(), "Q15_UNREADABLE_ADOPTION_ROOT="+root)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("interrupted adoption: %v (%s)", err, output)
	}
	if _, err := os.Stat(filepath.Join(root, ".git/q15-memory-recovery.json")); err != nil {
		t.Fatalf("interrupted adoption left no recovery journal: %v", err)
	}
	fresh := memory.NewStore(memoryrepo.New(root, memoryrepo.NewGitCommitter()), "test")
	if err := fresh.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertRepairRecoveryBaseline(t, root, fresh, before)
}

func TestUnreadableAdoptionRecordsRecreatedFileSeparately(t *testing.T) {
	root, store := newCognitionRecoveryStore(t)
	prepareUnreadableAdoption(t, root, store)
	for generation := range 2 {
		if generation > 0 {
			if err := store.WriteFileBytes(context.Background(), repairRecoveryTarget, []byte(strings.Repeat("j", 100*1024)+"\n")); err != nil {
				t.Fatal(err)
			}
		}
		job := cognition.NewSemanticMemoryExtractionRegistration(0, 0).NewJob()
		result, err := cognition.NewRunner(nil, nil, nil, store).Run(context.Background(), job, nil)
		if err != nil || !result.NoWork {
			t.Fatalf("runtime-only adoption = %#v, %v, want no model work", result, err)
		}
		if _, exists, err := store.ReadFileBytes(context.Background(), repairRecoveryTarget); err != nil ||
			exists {
			t.Fatalf("target remains: exists=%v error=%v", exists, err)
		}
	}
	ledger, _, err := store.ReadFileBytes(context.Background(), "working/LEDGER.md")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := claims.ParseLedger(ledger)
	if err != nil {
		t.Fatal(err)
	}
	var records []claims.LedgerEntry
	for _, entry := range entries {
		if entry.Description == repairRecoveryTarget {
			records = append(records, entry)
		}
	}
	if len(records) != 2 || records[0].Note == records[1].Note {
		t.Fatalf("recreated file records=%#v, want two distinct adoption identities", records)
	}
	assertCognitionRecoveryGitClean(t, root)
}

func prepareUnreadableAdoption(t *testing.T, root string, store *runtimeStore) map[string][]byte {
	t.Helper()
	before := prepareRepairRecoveryTarget(t, root, store)
	content := []byte(strings.Repeat("j", 100*1024) + "\n")
	if err := store.WriteFileBytes(context.Background(), repairRecoveryTarget, content); err != nil {
		t.Fatal(err)
	}
	before[repairRecoveryTarget] = content
	return before
}
