package app

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/cognition"
	"github.com/q15co/q15/systems/agent/internal/fileops"
)

func TestCognitionRunnerOwnsFileMutationsUntilRecovery(t *testing.T) {
	root, store := newCognitionRecoveryStore(t)
	executor := fileops.NewGuardedExecutor(fileops.Settings{
		MemoryLocalDir: root, MemoryRuntimeDir: "/memory",
	}, store.memory)
	before, _, err := store.ReadFileBytes(context.Background(), "working/WORKING_MEMORY.md")
	if err != nil {
		t.Fatal(err)
	}
	model := recoveryModelClient{
		complete: func(ctx context.Context) (agent.ModelClientResult, error) {
			if cognition.MemoryRecoveryOwner(ctx) == "" {
				t.Fatal("runner did not pass its recovery ownership to tools")
			}
			if _, err := executor.WriteFile(context.Background(), "/memory/working/WORKING_MEMORY.md", "foreground update\n"); err == nil {
				t.Fatal("non-owner wrote a path reserved by the cognition run")
			}
			if _, err := executor.WriteFile(ctx, "/memory/working/WORKING_MEMORY.md", "owner partial update\n"); err != nil {
				t.Fatalf("owner write: %v", err)
			}
			if _, err := executor.WriteFile(context.Background(), "/memory/notes/inbox/unrelated.md", "independent note\n"); err != nil {
				t.Fatalf("unrelated write: %v", err)
			}
			if _, err := executor.ReadFile(context.Background(), "/memory/working/WORKING_MEMORY.md", 0, 0); err != nil {
				t.Fatalf("read while reserved: %v", err)
			}
			return agent.ModelClientResult{}, fmt.Errorf("injected model failure")
		},
	}
	runner := cognition.NewRunnerWithEngine(agent.NewEngine(model, nil, []string{"test"}), store)
	if _, err := runner.Run(context.Background(), cognition.NewSemanticMemoryExtractionRegistration(0, 0).NewJob(), nil); err == nil {
		t.Fatal("runner accepted injected failure")
	}
	after, _, err := store.ReadFileBytes(context.Background(), "working/WORKING_MEMORY.md")
	if err != nil || !bytes.Equal(after, before) {
		t.Fatalf("recovery = %q, %v; want original %q", after, err, before)
	}
	if got, err := os.ReadFile(filepath.Join(root, "notes/inbox/unrelated.md")); err != nil ||
		string(got) != "independent note\n" {
		t.Fatalf("recovery changed unrelated note: %q, %v", got, err)
	}
	if _, err := executor.WriteFile(context.Background(), "/memory/working/WORKING_MEMORY.md", "foreground update\n"); err == nil ||
		!strings.Contains(err.Error(), "managed by cognition jobs") {
		t.Fatalf("unowned write after recovery = %v, want framework ownership rejection", err)
	}
	if got, err := os.ReadFile(filepath.Join(root, "working/WORKING_MEMORY.md")); err != nil ||
		!bytes.Equal(got, before) {
		t.Fatalf("unowned write changed recovered working memory: %q, %v", got, err)
	}
	if _, err := executor.WriteFile(context.Background(), "/memory/notes/inbox/after-recovery.md", "independent note after recovery\n"); err != nil {
		t.Fatalf("unrelated write after recovery = %v, want released mutation lease", err)
	}
}

func TestMemoryMutationGuardChecksResolvedTargetsAndEveryPatchPath(t *testing.T) {
	for _, name := range []string{"write", "edit", "directory alias", "traversal alias", "alternate root", "patch update", "patch delete", "patch move source", "patch move destination", "new topic"} {
		t.Run(name, func(t *testing.T) {
			root, store := newCognitionRecoveryStore(t)
			if err := os.Symlink("working", filepath.Join(root, "alias")); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "notes/inbox/source.md"), []byte("source\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			before, _, err := store.ReadFileBytes(context.Background(), "working/WORKING_MEMORY.md")
			if err != nil {
				t.Fatal(err)
			}
			job := cognition.NewSemanticMemoryExtractionRegistration(0, 0).NewJob()
			spec, err := job.Build(context.Background(), store)
			if err != nil {
				t.Fatal(err)
			}
			executor := fileops.NewGuardedExecutor(fileops.Settings{
				MemoryLocalDir: root, MemoryRuntimeDir: "/memory",
				WorkspaceLocalDir: root, WorkspaceRuntimeDir: "/workspace",
			}, store.memory)
			switch name {
			case "write":
				_, err = executor.WriteFile(
					context.Background(),
					"/memory/working/WORKING_MEMORY.md",
					"changed\n",
				)
			case "edit":
				_, err = executor.EditFile(
					context.Background(),
					"/memory/working/WORKING_MEMORY.md",
					"Working Memory",
					"Changed",
				)
			case "directory alias":
				_, err = executor.WriteFile(
					context.Background(),
					"/memory/alias/WORKING_MEMORY.md",
					"changed\n",
				)
			case "traversal alias":
				_, err = executor.WriteFile(
					context.Background(),
					"/memory/notes/../working/WORKING_MEMORY.md",
					"changed\n",
				)
			case "alternate root":
				_, err = executor.WriteFile(
					context.Background(),
					"/workspace/working/WORKING_MEMORY.md",
					"changed\n",
				)
			case "patch update":
				_, err = executor.ApplyPatch(
					context.Background(),
					"*** Begin Patch\n*** Update File: /memory/working/WORKING_MEMORY.md\n@@\n-# Working Memory\n+# Changed\n*** End Patch",
				)
			case "patch delete":
				_, err = executor.ApplyPatch(
					context.Background(),
					"*** Begin Patch\n*** Delete File: /memory/working/WORKING_MEMORY.md\n*** End Patch",
				)
			case "patch move source":
				_, err = executor.ApplyPatch(
					context.Background(),
					"*** Begin Patch\n*** Update File: /memory/working/WORKING_MEMORY.md\n*** Move to: /memory/notes/inbox/moved.md\n@@\n-# Working Memory\n+# Changed\n*** End Patch",
				)
			case "patch move destination":
				_, err = executor.ApplyPatch(
					context.Background(),
					"*** Begin Patch\n*** Update File: /memory/notes/inbox/source.md\n*** Move to: /memory/working/WORKING_MEMORY.md\n@@\n-source\n+changed\n*** End Patch",
				)
			case "new topic":
				_, err = executor.WriteFile(
					context.Background(),
					"/memory/semantic/topics/new.txt",
					"changed\n",
				)
			}
			want := "managed by cognition jobs"
			if name == "new topic" {
				want = "reserved by a cognition run"
			}
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("mutation error = %v, want ownership rejection %q", err, want)
			}
			if got, err := os.ReadFile(filepath.Join(root, "working/WORKING_MEMORY.md")); err != nil ||
				!bytes.Equal(got, before) {
				t.Fatalf("rejected mutation changed target: %q, %v", got, err)
			}
			if got, err := os.ReadFile(filepath.Join(root, "notes/inbox/source.md")); err != nil ||
				string(got) != "source\n" {
				t.Fatalf("rejected patch changed source: %q, %v", got, err)
			}
			if err := store.RecoverMemory(context.Background(), spec.MemoryRecoveryID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMemoryMutationGuardProtectsRepositoryMetadata(t *testing.T) {
	root, store := newCognitionRecoveryStore(t)
	executor := fileops.NewGuardedExecutor(
		fileops.Settings{MemoryLocalDir: root, MemoryRuntimeDir: "/memory"},
		store.memory,
	)
	if err := os.Symlink(".git", filepath.Join(root, "metadata-alias")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/memory/.git/q15-memory-recovery.json", "/memory/metadata-alias/config"} {
		if _, err := executor.WriteFile(context.Background(), path, "changed\n"); err == nil ||
			!strings.Contains(err.Error(), "metadata is managed") {
			t.Fatalf("metadata mutation %s = %v", path, err)
		}
	}
}

type changingWorkingPromptStore struct {
	*runtimeStore
	root    string
	changed bool
}

func (s *changingWorkingPromptStore) LoadWorkingMemory(
	ctx context.Context,
) (agent.WorkingMemory, error) {
	working, err := s.runtimeStore.LoadWorkingMemory(ctx)
	if err == nil && !s.changed {
		err = os.WriteFile(
			filepath.Join(s.root, "working/WORKING_MEMORY.md"),
			[]byte("# Working Memory\n\n- Foreground task arrived during Build.\n"),
			0o600,
		)
		s.changed = true
	}
	return working, err
}

func TestSemanticBuildPreservesStoreWriteBetweenPromptAndSnapshot(t *testing.T) {
	root, store := newCognitionRecoveryStore(t)
	loader := &changingWorkingPromptStore{runtimeStore: store, root: root}
	job := cognition.NewSemanticMemoryExtractionRegistration(0, 0).NewJob()
	if _, err := job.Build(context.Background(), loader); err == nil ||
		!strings.Contains(err.Error(), "memory changed while building job evidence") {
		t.Fatalf("Build accepted a stale prompt: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".git/q15-memory-recovery.json")); !os.IsNotExist(
		err,
	) {
		t.Fatalf("incoherent Build created recovery journal: %v", err)
	}
	const expected = "# Working Memory\n\n- Foreground task arrived during Build.\n"
	actual, _, err := store.ReadFileBytes(context.Background(), "working/WORKING_MEMORY.md")
	if err != nil || string(actual) != expected {
		t.Fatalf("Build lost foreground write: %q, %v", actual, err)
	}
	spec, err := job.Build(context.Background(), store)
	if err != nil {
		t.Fatalf("fresh Build rejected current evidence: %v", err)
	}
	if spec.Baseline["/memory/working/WORKING_MEMORY.md"] != expected {
		t.Fatalf("fresh baseline = %q", spec.Baseline["/memory/working/WORKING_MEMORY.md"])
	}
	if err := store.RecoverMemory(context.Background(), spec.MemoryRecoveryID); err != nil {
		t.Fatal(err)
	}
	actual, _, err = store.ReadFileBytes(context.Background(), "working/WORKING_MEMORY.md")
	if err != nil || string(actual) != expected {
		t.Fatalf("fresh run adopted stale state: %q, %v", actual, err)
	}
}

type changingWorkingBuildStore struct {
	*runtimeStore
	executor *fileops.Executor
	path     string
	content  string
	changed  bool
}

func (s *changingWorkingBuildStore) change(ctx context.Context, path string) error {
	if s.changed || path != s.path {
		return nil
	}
	_, err := s.executor.WriteFile(ctx, path, s.content)
	if err == nil {
		s.changed = true
	}
	return err
}

func (s *changingWorkingBuildStore) LoadWorkingMemory(
	ctx context.Context,
) (agent.WorkingMemory, error) {
	working, err := s.runtimeStore.LoadWorkingMemory(ctx)
	if err == nil {
		err = s.change(ctx, "/memory/working/WORKING_MEMORY.md")
	}
	return working, err
}

func (s *changingWorkingBuildStore) LoadOpenLoops(ctx context.Context) (string, error) {
	loops, err := s.runtimeStore.LoadOpenLoops(ctx)
	if err == nil {
		err = s.change(ctx, "/memory/working/OPEN_LOOPS.md")
	}
	return loops, err
}

func (s *changingWorkingBuildStore) LoadWorkingLedger(ctx context.Context) (string, error) {
	ledger, err := s.runtimeStore.LoadWorkingLedger(ctx)
	if err == nil {
		err = s.change(ctx, "/memory/working/LEDGER.md")
	}
	return ledger, err
}

func TestWorkingBuildPreservesForegroundWritesBetweenPromptAndSnapshot(t *testing.T) {
	for _, test := range []struct{ name, path, content string }{
		{name: "working", path: "/memory/working/WORKING_MEMORY.md", content: "# Working Memory\n\n- Foreground task arrived during Build.\n"},
		{name: "open loops", path: "/memory/working/OPEN_LOOPS.md", content: "# Open Loops\n\n- [l-2026-0042] Check the foreground task.\n  {trigger: next-session, owner: agent, src: t42}\n"},
		{name: "ledger", path: "/memory/working/LEDGER.md", content: "# Conversion Ledger\n\n2026-10-16T04:00Z run=working_memory.consolidate src=t42 dropped \"Previous task.\" (completed)\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, store := newCognitionRecoveryStore(t)
			loader := &changingWorkingBuildStore{
				runtimeStore: store,
				executor: fileops.NewGuardedExecutor(
					fileops.Settings{MemoryLocalDir: root, MemoryRuntimeDir: "/memory"},
					store.memory,
				),
				path:    test.path,
				content: test.content,
			}
			job := cognition.NewWorkingMemoryConsolidationRegistration(0, 0).NewJob()
			if _, err := job.Build(context.Background(), loader); err == nil ||
				!strings.Contains(err.Error(), "memory changed while building job evidence") {
				t.Fatalf("Build accepted a stale %s read: %v", test.name, err)
			}
			if _, err := os.Stat(filepath.Join(root, ".git/q15-memory-recovery.json")); !os.IsNotExist(
				err,
			) {
				t.Fatalf("incoherent Build created recovery journal: %v", err)
			}
			relative := strings.TrimPrefix(test.path, "/memory/")
			actual, _, err := store.ReadFileBytes(context.Background(), relative)
			if err != nil || string(actual) != test.content {
				t.Fatalf("Build lost foreground write: %q, %v", actual, err)
			}
			spec, err := job.Build(context.Background(), store)
			if err != nil {
				t.Fatalf("fresh Build rejected current evidence: %v", err)
			}
			if spec.Baseline[test.path] != test.content {
				t.Fatalf("fresh baseline = %q, want %q", spec.Baseline[test.path], test.content)
			}
			if err := store.RecoverMemory(context.Background(), spec.MemoryRecoveryID); err != nil {
				t.Fatal(err)
			}
			actual, _, err = store.ReadFileBytes(context.Background(), relative)
			if err != nil || string(actual) != test.content {
				t.Fatalf("fresh run adopted stale state: %q, %v", actual, err)
			}
		})
	}
}
