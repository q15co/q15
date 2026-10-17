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
	"github.com/q15co/q15/systems/agent/internal/memory/claims"
	"github.com/q15co/q15/systems/agent/internal/memoryrepo"
	filetools "github.com/q15co/q15/systems/agent/internal/tools/files"
)

func TestRepairRunnerRotatesLedgerWithoutReopeningStore(t *testing.T) {
	ctx := context.Background()
	root, store := newCognitionRecoveryStore(t)
	seed, _, err := store.ReadFileBytes(ctx, "working/LEDGER.md")
	if err != nil {
		t.Fatal(err)
	}
	const threshold = 256 * 1024
	baseline := string(seed) + "<!--" + strings.Repeat("x", threshold-len(seed)-200) + "-->\n"
	if err := store.WriteFileBytes(ctx, "working/LEDGER.md", []byte(baseline)); err != nil {
		t.Fatal(err)
	}
	model := recoveryModelClient{complete: func(context.Context) (agent.ModelClientResult, error) {
		return agent.ModelClientResult{Messages: []conversation.Message{
			conversation.AssistantMessage(conversation.Text(
				`{"decisions":[{"offset":0,"outcome":"dropped","reason":"Obsolete content."}]}`,
				"",
			)),
		}}, nil
	}}
	tools, err := agent.NewToolRegistry()
	if err != nil {
		t.Fatal(err)
	}
	runner := cognition.NewRunner(model, tools, []string{"test"}, store)
	for run := range 2 {
		target := fmt.Sprintf("semantic/legacy-%d.md", run)
		if err := store.WriteFileBytes(ctx, target, []byte(strings.Repeat("obsolete ", 128)+"\n")); err != nil {
			t.Fatal(err)
		}
		result, err := runner.Run(
			ctx,
			cognition.NewSemanticMemoryExtractionRegistration(0, 0).NewJob(),
			nil,
		)
		if err != nil || result.Metadata["chunk_lines_exited"] != "1" {
			t.Fatalf("repair %d: metadata=%v error=%v", run, result.Metadata, err)
		}
		if _, exists, err := store.ReadFileBytes(ctx, target); err != nil || exists {
			t.Fatalf("repair target survived: exists=%v error=%v", exists, err)
		}
		active, _, err := store.ReadFileBytes(ctx, "working/LEDGER.md")
		if err != nil || len(active) >= threshold {
			t.Fatalf("active ledger = %d bytes, error=%v", len(active), err)
		}
		history, err := store.LoadConversionLedgerHistory(ctx)
		if err != nil || !strings.HasPrefix(history, baseline) {
			t.Fatalf("ledger history lost its exact prefix: %v", err)
		}
		entries, err := claims.ParseLedger([]byte(history + "\n" + string(active)))
		if err != nil || len(entries) != 2*(run+1) {
			t.Fatalf("history and active ledger: entries=%d error=%v", len(entries), err)
		}
		if entries[2*run].SourceSeq != "mem:"+target+":0" {
			t.Fatalf("ledger entries out of order: %#v", entries)
		}
		assertCognitionRecoveryGitClean(t, root)
	}
}

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
