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
	"github.com/q15co/q15/systems/agent/internal/fileops"
	"github.com/q15co/q15/systems/agent/internal/memory"
	"github.com/q15co/q15/systems/agent/internal/memoryrepo"
	filetools "github.com/q15co/q15/systems/agent/internal/tools/files"
)

func TestRepairRunnerCommitsTypedDecisionsWithoutModelWrites(t *testing.T) {
	for _, moved := range []bool{false, true} {
		t.Run(fmt.Sprintf("moved=%v", moved), func(t *testing.T) {
			ctx := context.Background()
			root, store := newCognitionRecoveryStore(t)
			target, before, after := "semantic/legacy.md", "Obsolete legacy content.\n", ""
			if moved {
				target = "working/WORKING_MEMORY.md"
				after = "# Working Memory\n\n## Active Tasks\n" + strings.Repeat(
					"remaining context ",
					600,
				) + "\n"
				before = strings.Replace(
					after,
					"## Active Tasks\n",
					"## Active Tasks\n- Finish the release.\n",
					1,
				)
			}
			if err := store.WriteFileBytes(ctx, target, []byte(before)); err != nil {
				t.Fatal(err)
			}
			offset := 0
			response := `{"decisions":[{"offset":0,"outcome":"dropped","reason":"Synthetic obsolete content."}]}`
			if moved {
				offset = strings.Index(before, "- Finish the release.")
				response = fmt.Sprintf(
					`{"decisions":[{"offset":%d,"outcome":"moved","loop":{"id":"l-2026-0001","trigger":"next-session","owner":"agent"}}]}`,
					offset,
				)
			}
			source := fmt.Sprintf("mem:%s:%d", target, offset)
			calls := 0
			model := recoveryModelClient{
				complete: func(context.Context) (agent.ModelClientResult, error) {
					calls++
					return agent.ModelClientResult{
						Messages: []conversation.Message{
							conversation.AssistantMessage(conversation.Text(response, "")),
						},
					}, nil
				},
			}
			executor := fileops.NewGuardedExecutor(
				fileops.Settings{MemoryLocalDir: root, MemoryRuntimeDir: "/memory"},
				store.memory,
			)
			tools, err := agent.NewToolRegistry(filetools.NewWriteFile(executor))
			if err != nil {
				t.Fatal(err)
			}
			runner := cognition.NewRunnerWithEngine(
				agent.NewEngine(model, tools, []string{"test"}),
				store,
			)
			result, err := runner.Run(
				ctx,
				cognition.NewSemanticMemoryExtractionRegistration(0, 0).NewJob(),
				nil,
			)
			if err != nil || result.Metadata["chunk_lines_exited"] != "1" || calls != 1 {
				t.Fatalf("Run() = %#v, err=%v, model calls=%d", result, err, calls)
			}
			fresh := memory.NewStore(memoryrepo.New(root, memoryrepo.NewGitCommitter()), "test")
			if err := fresh.Init(ctx); err != nil {
				t.Fatal(err)
			}
			got, exists, err := fresh.ReadFileBytes(ctx, target)
			if err != nil || exists != moved || (moved && string(got) != after) {
				t.Fatalf("restarted target = %q, exists=%v, err=%v", got, exists, err)
			}
			if moved {
				got, _, err := fresh.ReadFileBytes(ctx, "working/OPEN_LOOPS.md")
				if err != nil ||
					!strings.Contains(string(got), "- [l-2026-0001] Finish the release.") ||
					!strings.Contains(string(got), "src: "+source) {
					t.Fatalf("restarted open loops = %q, err=%v", got, err)
				}
			}
			gotLedger, _, err := fresh.ReadFileBytes(ctx, "working/LEDGER.md")
			if err != nil || !strings.Contains(string(gotLedger), source) {
				t.Fatalf("restarted ledger = %q, err=%v", gotLedger, err)
			}
			if _, err := os.Stat(filepath.Join(root, ".git/q15-memory-recovery.json")); !os.IsNotExist(
				err,
			) {
				t.Fatalf("successful repair retained its journal: %v", err)
			}
			assertCognitionRecoveryGitClean(t, root)
		})
	}
}
