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
	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/fileops"
	filetools "github.com/q15co/q15/systems/agent/internal/tools/files"
)

type recoveryFileExecutor struct {
	stubFileExecutor
	settings fileops.Settings
}

func (e recoveryFileExecutor) ReadFile(
	_ context.Context,
	path string,
	offset, limit int,
) (fileops.ReadResult, error) {
	return fileops.ReadFile(e.settings, path, offset, limit)
}

func (e recoveryFileExecutor) WriteFile(
	_ context.Context,
	path, content string,
) (fileops.WriteResult, error) {
	return fileops.WriteFile(e.settings, path, content)
}

func TestCognitionRunnerRestoresRealStoreAfterConflictValidationFailure(t *testing.T) {
	root, store := newCognitionRecoveryStore(t)
	ctx := context.Background()
	facts := "# Semantic Facts\n\n## Confirmed Facts\n\n" +
		"- [c-2026-0001] Deploy to production; conflicts with c-2026-0002.\n" +
		"  {status: conflicted, kind: fact, confidence: confirmed, src: t11, rev: 2026-10-14}\n\n" +
		"## Grounded Inferences\n\n- None\n"
	topic := "# Deployment\n\n- [c-2026-0002] Deploy to staging; conflicts with c-2026-0001.\n" +
		"  {status: conflicted, kind: fact, confidence: likely, src: t8, rev: 2026-10-13}\n"
	for relative, content := range map[string]string{
		"semantic/facts.md": facts, "semantic/topics/existing.md": topic,
	} {
		if err := store.WriteFileBytes(ctx, relative, []byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	review := cognition.NewVerificationReviewRegistration().NewJob()
	reviewSpec, err := review.Build(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := review.ApplyResult(ctx, store, cognition.JobOutput{
		Spec:      reviewSpec,
		FinalText: `resolution claims=c-2026-0001,c-2026-0002 winner=c-2026-0001 reason="The newer direct statement wins."`,
	}); err != nil {
		t.Fatal(err)
	}
	before := cognitionRecoveryBytes(t, store)
	ledger := string(before["working/LEDGER.md"]) +
		"2026-10-15T05:00Z run=semantic_memory.extract src=t8..t11 resolved " +
		`"Deploy to staging; conflicts with c-2026-0001." → c-2026-0002 (winner=c-2026-9999; incorrect winner)` + "\n"
	writes := []struct{ path, content string }{
		{"semantic/facts.md", strings.Replace(facts, "status: conflicted", "status: active", 1)},
		{"semantic/topics/existing.md", "# Deployment\n"},
		{"working/LEDGER.md", ledger},
		{
			"semantic/topics/new.md",
			"# New topic\n\n- [c-2026-0003] An independent deployment fact.\n" +
				"  {status: active, kind: fact, confidence: likely, src: t11, rev: 2026-10-15}\n",
		},
	}
	calls := 0
	model := recoveryModelClient{complete: func(context.Context) (agent.ModelClientResult, error) {
		calls++
		var parts []conversation.Part
		switch calls {
		case 1:
			for i, relative := range []string{"semantic/facts.md", "semantic/preferences.md", "semantic/projects.md"} {
				parts = append(
					parts,
					conversation.ToolCall(
						fmt.Sprintf("read-%d", i),
						"read_file",
						fmt.Sprintf(`{"path":%q}`, "/memory/"+relative),
					),
				)
			}
		case 2:
			for i, write := range writes {
				parts = append(
					parts,
					conversation.ToolCall(
						fmt.Sprintf("write-%d", i),
						"write_file",
						fmt.Sprintf(
							`{"path":%q,"content":%q}`,
							"/memory/"+write.path,
							write.content,
						),
					),
				)
			}
		default:
			parts = append(parts, conversation.Text("Applied the reviewed resolution.", ""))
		}
		return agent.ModelClientResult{
			Messages: []conversation.Message{conversation.AssistantMessage(parts...)},
		}, nil
	}}
	executor := recoveryFileExecutor{
		settings: fileops.Settings{MemoryLocalDir: root, MemoryRuntimeDir: "/memory"},
	}
	tools, err := agent.NewToolRegistry(
		filetools.NewReadFile(executor),
		filetools.NewWriteFile(executor),
	)
	if err != nil {
		t.Fatal(err)
	}
	runner := cognition.NewRunnerWithEngine(agent.NewEngine(model, tools, []string{"test"}), store)
	_, err = runner.Run(ctx, cognition.NewSemanticMemoryExtractionRegistration(0, 0).NewJob(), nil)
	if err == nil || !strings.Contains(err.Error(), "wrong winner or reason") {
		t.Fatalf("Run error = %v, want the real conflict ledger validation failure", err)
	}
	for relative, want := range before {
		got, exists, err := store.ReadFileBytes(ctx, relative)
		if err != nil || !exists || !bytes.Equal(got, want) {
			t.Errorf(
				"restored %s = %q, exists=%v, error=%v; want %q",
				relative,
				got,
				exists,
				err,
				want,
			)
		}
	}
	if _, exists, err := store.ReadFileBytes(ctx, "semantic/topics/new.md"); err != nil || exists {
		t.Fatalf("run-created topic survived: exists=%v error=%v", exists, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".git/q15-memory-recovery.json")); !os.IsNotExist(
		err,
	) {
		t.Fatalf("completed rollback retained the recovery journal: %v", err)
	}
	assertCognitionRecoveryGitClean(t, root)
}
