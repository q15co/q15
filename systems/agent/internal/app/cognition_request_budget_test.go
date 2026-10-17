package app

import (
	"context"
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
