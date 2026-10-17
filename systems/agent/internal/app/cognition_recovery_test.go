package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/cognition"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/memory"
	"github.com/q15co/q15/systems/agent/internal/memoryrepo"
)

type recoveryModelClient struct {
	complete func(context.Context) (agent.ModelClientResult, error)
}

func (m recoveryModelClient) Complete(
	ctx context.Context,
	_ string,
	_ []conversation.Message,
	_ []agent.ToolDefinition,
) (agent.ModelClientResult, error) {
	return m.complete(ctx)
}

type recoveryJob struct {
	cognition.JobDefinition
	build func(context.Context, cognition.ContextLoader) (cognition.Spec, error)
	apply func(context.Context) error
}

func (j recoveryJob) Build(
	ctx context.Context,
	loader cognition.ContextLoader,
) (cognition.Spec, error) {
	if j.build != nil {
		return j.build(ctx, loader)
	}
	return j.JobDefinition.Build(ctx, loader)
}

func (j recoveryJob) ApplyResult(
	ctx context.Context,
	_ cognition.ContextLoader,
	_ cognition.JobOutput,
) (cognition.ParsedResult, error) {
	if j.apply != nil {
		return cognition.ParsedResult{}, j.apply(ctx)
	}
	return cognition.ParsedResult{}, nil
}

func TestCognitionRunnerRecoversRealStoreAcrossFailureExits(t *testing.T) {
	for _, failure := range []string{"cancelled model", "model error", "apply error", "render error", "completion error"} {
		t.Run(failure, func(t *testing.T) {
			root, store := newCognitionRecoveryStore(t)
			before := cognitionRecoveryBytes(t, store)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			delegate := cognition.NewSemanticMemoryExtractionRegistration(0, 0).NewJob()
			job := recoveryJob{JobDefinition: delegate}
			if failure == "apply error" {
				job.apply = func(context.Context) error { return fmt.Errorf("injected apply failure") }
			}
			if failure == "render error" {
				job.build = func(ctx context.Context, loader cognition.ContextLoader) (cognition.Spec, error) {
					spec, err := delegate.Build(ctx, loader)
					if err == nil {
						corruptCognitionRecoveryBytes(t, root, before)
						spec.Objective = ""
					}
					return spec, err
				}
			}
			model := recoveryModelClient{
				complete: func(context.Context) (agent.ModelClientResult, error) {
					corruptCognitionRecoveryBytes(t, root, before)
					switch failure {
					case "cancelled model":
						cancel()
						return agent.ModelClientResult{}, context.Canceled
					case "model error":
						return agent.ModelClientResult{}, fmt.Errorf("injected model failure")
					case "completion error":
						// Cancel after producing a result: completion must fail in Git,
						// then cleanup must use its own live context.
						cancel()
					}
					return agent.ModelClientResult{Messages: []conversation.Message{
						conversation.AssistantMessage(conversation.Text("complete", "")),
					}}, nil
				},
			}
			engine := agent.NewEngine(model, nil, []string{"test"})
			runner := cognition.NewRunnerWithEngine(engine, store)
			if _, err := runner.Run(ctx, job, nil); err == nil {
				t.Fatal("Run accepted an injected failure")
			} else if failure == "cancelled model" && !errors.Is(err, context.Canceled) {
				t.Fatalf("Run lost cancellation cause: %v", err)
			}
			for relative, want := range before {
				got, exists, err := store.ReadFileBytes(context.Background(), relative)
				if err != nil || !exists || !bytes.Equal(got, want) {
					t.Errorf(
						"recovered %s = %q, exists=%v, err=%v; want %q",
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
				t.Fatalf("new topic survived failed run: exists=%v err=%v", exists, err)
			}
			if _, err := os.Stat(filepath.Join(root, ".git/q15-memory-recovery.json")); !os.IsNotExist(
				err,
			) {
				t.Fatalf("successful rollback retained journal: %v", err)
			}
			assertCognitionRecoveryGitClean(t, root)
		})
	}
}

func TestCognitionRunnerCommitsSuccessfulStoreAndRetiresRecovery(t *testing.T) {
	root, store := newCognitionRecoveryStore(t)
	const relative = "semantic/facts.md"
	validated := []byte("# Validated facts\n")
	model := recoveryModelClient{complete: func(context.Context) (agent.ModelClientResult, error) {
		if err := os.WriteFile(filepath.Join(root, relative), validated, 0o600); err != nil {
			t.Fatal(err)
		}
		return agent.ModelClientResult{Messages: []conversation.Message{
			conversation.AssistantMessage(conversation.Text("complete", "")),
		}}, nil
	}}
	runner := cognition.NewRunnerWithEngine(agent.NewEngine(model, nil, []string{"test"}), store)
	job := recoveryJob{
		JobDefinition: cognition.NewSemanticMemoryExtractionRegistration(0, 0).NewJob(),
	}
	if _, err := runner.Run(context.Background(), job, nil); err != nil {
		t.Fatal(err)
	}
	fresh := memory.NewStore(memoryrepo.New(root, memoryrepo.NewGitCommitter()), "test")
	if err := fresh.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _, err := fresh.ReadFileBytes(context.Background(), relative)
	if err != nil || !bytes.Equal(got, validated) {
		t.Fatalf("restart lost validated output: %q, %v", got, err)
	}
	assertCognitionRecoveryGitClean(t, root)
}

func TestConversionBuildRejectsPendingRecoveryBeforeAnotherRun(t *testing.T) {
	root, store := newCognitionRecoveryStore(t)
	job := cognition.NewSemanticMemoryExtractionRegistration(0, 0).NewJob()
	spec, err := job.Build(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "working/WORKING_MEMORY.md"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := job.Build(context.Background(), store); err == nil ||
		!strings.Contains(err.Error(), "recovery is pending") {
		t.Fatalf("Build accepted partial state with a pending journal: %v", err)
	}
	if err := store.RecoverMemory(context.Background(), spec.MemoryRecoveryID); err != nil {
		t.Fatal(err)
	}
}

func newCognitionRecoveryStore(t *testing.T) (string, *runtimeStore) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "memory")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	mem := memory.NewStore(memoryrepo.New(root, memoryrepo.NewGitCommitter()), "test")
	if err := mem.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := mem.WriteFileBytes(context.Background(), "semantic/topics/existing.md", []byte("existing topic\n")); err != nil {
		t.Fatal(err)
	}
	return root, &runtimeStore{memory: mem}
}

func cognitionRecoveryBytes(t *testing.T, store *runtimeStore) map[string][]byte {
	t.Helper()
	before := make(map[string][]byte)
	for _, relative := range []string{
		"working/WORKING_MEMORY.md", "working/OPEN_LOOPS.md", "working/LEDGER.md",
		"semantic/facts.md", "semantic/preferences.md", "semantic/projects.md", "semantic/topics/existing.md",
	} {
		data, exists, err := store.ReadFileBytes(context.Background(), relative)
		if err != nil || !exists {
			t.Fatalf("read baseline %s: exists=%v err=%v", relative, exists, err)
		}
		before[relative] = data
	}
	return before
}

func corruptCognitionRecoveryBytes(t *testing.T, root string, before map[string][]byte) {
	t.Helper()
	for relative := range before {
		if err := os.WriteFile(filepath.Join(root, relative), []byte("partial\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "semantic/topics/new.md"), []byte("partial new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertCognitionRecoveryGitClean(t *testing.T, root string) {
	t.Helper()
	output, err := exec.Command("git", "-C", root, "status", "--porcelain").CombinedOutput()
	if err != nil || len(bytes.TrimSpace(output)) != 0 {
		t.Fatalf("recovered Git worktree is dirty: %s, %v", output, err)
	}
}
