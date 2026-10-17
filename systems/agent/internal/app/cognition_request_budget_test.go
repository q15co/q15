package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/cognition"
	"github.com/q15co/q15/systems/agent/internal/conversation"
)

type oversizedRepairEvidenceTool struct{}

func (oversizedRepairEvidenceTool) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{Name: "embed_search"}
}
func (oversizedRepairEvidenceTool) Run(context.Context, string) (string, error) {
	return strings.Repeat("synthetic evidence ", 400000), nil
}

func TestRepairCommitsAfterOversizedOptionalEvidenceIsOmitted(t *testing.T) {
	ctx := context.Background()
	root, store := newCognitionRecoveryStore(t)
	if err := store.WriteFileBytes(ctx, "semantic/synthetic.md", []byte("Synthetic obsolete content.\n")); err != nil {
		t.Fatal(err)
	}
	calls := 0
	model := recoveryModelClient{complete: func(context.Context) (agent.ModelClientResult, error) {
		calls++
		if calls == 1 {
			return agent.ModelClientResult{
				Messages: []conversation.Message{
					conversation.AssistantMessage(
						conversation.ToolCall("search-1", "embed_search", `{"query":"synthetic"}`),
					),
				},
			}, nil
		}
		return agent.ModelClientResult{
			Messages: []conversation.Message{
				conversation.AssistantMessage(
					conversation.Text(
						`{"decisions":[{"offset":0,"outcome":"dropped","reason":"Synthetic obsolete content."}]}`,
						"",
					),
				),
			},
		}, nil
	}}
	tools, err := agent.NewToolRegistry(oversizedRepairEvidenceTool{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := cognition.NewRunner(model, tools, []string{"synthetic"}, store).
		Run(ctx, cognition.NewSemanticMemoryExtractionRegistration(0, 0).NewJob(), nil)
	if err != nil || calls != 2 || result.Metadata["chunk_lines_exited"] != "1" {
		t.Fatalf("repair did not drain: calls=%d result=%#v error=%v", calls, result, err)
	}
	omitted := false
	for _, m := range result.Messages {
		for _, p := range m.Parts {
			if len(p.Content) > 32*1024 {
				t.Fatal("oversized result persisted in run context")
			}
			omitted = omitted || (p.IsError && strings.Contains(p.Content, "omitted"))
		}
	}
	if !omitted {
		t.Fatal("oversized evidence was not explicitly marked omitted")
	}
	if _, exists, err := store.ReadFileBytes(ctx, "semantic/synthetic.md"); err != nil || exists {
		t.Fatalf("target not drained: exists=%v error=%v", exists, err)
	}
	ledger, _, err := store.ReadFileBytes(ctx, "working/LEDGER.md")
	if err != nil || !strings.Contains(string(ledger), "mem:semantic/synthetic.md:0") {
		t.Fatalf("missing ledger exit: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".git/q15-memory-recovery.json")); !os.IsNotExist(
		err,
	) {
		t.Fatalf("journal retained after committed repair: %v", err)
	}
	assertCognitionRecoveryGitClean(t, root)
}

func TestBoundedRepairDrainsOversizedRootWithLargeUnrelatedEvidence(t *testing.T) {
	ctx := context.Background()
	root, store := newCognitionRecoveryStore(t)
	prefix := "# Semantic Facts\n\n## Confirmed Facts\n\n"
	linePrefix := "- Synthetic obsolete evidence "
	line := linePrefix + strings.Repeat("x", 4096-len(linePrefix)-1) + "\n"
	facts := prefix + strings.Repeat(line, 16) + "\n## Grounded Inferences\n\n- None\n"
	working := "# Working Memory\n\n## Active Tasks\n\n" + strings.Repeat(line, 3)
	for path, content := range map[string]string{"semantic/facts.md": facts, "working/WORKING_MEMORY.md": working} {
		if err := store.WriteFileBytes(ctx, path, []byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.memory.AppendTurn(ctx, []conversation.Message{conversation.UserMessage(strings.Repeat("synthetic history ", 120000))}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	model := recoveryModelClient{complete: func(context.Context) (agent.ModelClientResult, error) {
		calls++
		if calls == 1 {
			return agent.ModelClientResult{
				Messages: []conversation.Message{
					conversation.AssistantMessage(
						conversation.ToolCall("search-1", "embed_search", `{"query":"synthetic"}`),
					),
				},
			}, nil
		}
		response := fmt.Sprintf(
			`{"decisions":[{"offset":%d,"outcome":"dropped","reason":"Synthetic obsolete content."},{"offset":%d,"outcome":"dropped","reason":"Synthetic obsolete content."}]}`,
			len(prefix),
			len(prefix)+len(line),
		)
		return agent.ModelClientResult{
			Messages: []conversation.Message{
				conversation.AssistantMessage(conversation.Text(response, "")),
			},
		}, nil
	}}
	tools, err := agent.NewToolRegistry(oversizedRepairEvidenceTool{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := cognition.NewRunner(model, tools, []string{"synthetic"}, store).
		Run(ctx, cognition.NewSemanticMemoryExtractionRegistration(0, 0).NewJob(), nil)
	if err != nil || calls != 2 || result.Metadata["chunk_lines_exited"] != "2" ||
		result.Metadata["repair_target"] != "semantic/facts.md" {
		t.Fatalf("bounded root repair: calls=%d metadata=%v error=%v", calls, result.Metadata, err)
	}
	got, _, err := store.ReadFileBytes(ctx, "semantic/facts.md")
	want := prefix + strings.Repeat(line, 14) + "\n## Grounded Inferences\n\n- None\n"
	if err != nil || string(got) != want {
		t.Fatalf(
			"root did not shrink by exactly the cut: bytes=%d want=%d error=%v",
			len(got),
			len(want),
			err,
		)
	}
	got, _, err = store.ReadFileBytes(ctx, "working/WORKING_MEMORY.md")
	if err != nil || string(got) != working {
		t.Fatal("repair changed unrelated oversized working memory")
	}
	ledger, _, err := store.ReadFileBytes(ctx, "working/LEDGER.md")
	for _, offset := range []int{len(prefix), len(prefix) + len(line)} {
		if err != nil ||
			!strings.Contains(
				string(ledger),
				fmt.Sprintf("src=mem:semantic/facts.md:%d ", offset),
			) {
			t.Fatalf("missing covered exit %d: %v", offset, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".git/q15-memory-recovery.json")); !os.IsNotExist(
		err,
	) {
		t.Fatalf("journal retained after committed root repair: %v", err)
	}
	assertCognitionRecoveryGitClean(t, root)
}
