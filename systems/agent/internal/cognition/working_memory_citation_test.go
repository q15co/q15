package cognition

import (
	"context"
	"strings"
	"testing"
)

func TestWorkingMemoryConsolidationContractKeepsMemoryCitationsPointOnly(t *testing.T) {
	spec := buildWorkingMemorySpec(t, &workingMemoryJobLoader{})
	for _, rule := range []string{
		"t<seq>", "t<a>..t<b>", "mem:<relative-path>:<byte-offset>",
		"one decimal byte offset", "never a range", "Ranges apply only to transcript markers",
		"mem:working/WORKING_MEMORY.md:115",
	} {
		if !strings.Contains(spec.CompletionContract, rule) {
			t.Errorf("consolidation contract omits %q", rule)
		}
	}
}

func TestWorkingMemoryConsolidationPostRepairExitRequiresPointCitation(t *testing.T) {
	for _, source := range []string{"mem:working/WORKING_MEMORY.md:37", "mem:working/WORKING_MEMORY.md:1..115"} {
		t.Run(source, func(t *testing.T) {
			before := map[string]string{
				workingMemoryRuntimePath: "# Working Memory\n\n## Temporary Context\n\n- Check the drained workspace.\n",
				openLoopsRuntimePath: "# Open Loops\n\n- [l-2026-0001] Check the release.\n" +
					"  {trigger: next-session, owner: agent, src: t42}\n",
				workingLedgerRuntimePath: "# Ledger\n\n" +
					`2026-10-18T08:00Z run=semantic_memory.extract src=mem:working/WORKING_MEMORY.md:0 converted "The user maintains q15." → c-2026-0001 (semantic/topics/q15.md)` + "\n\n",
			}
			loader := &workingMemoryJobLoader{files: make(map[string][]byte)}
			for path, content := range before {
				loader.files[relativeMemoryPath(path)] = []byte(content)
			}
			spec := buildWorkingMemorySpec(t, loader)
			after := "# Working Memory\n\n## Temporary Context\n\n- None\n"
			ledger := before[workingLedgerRuntimePath] +
				`2026-10-18T09:00Z run=working_memory.consolidate src=` + source +
				` dropped "Check the drained workspace." (check completed)` + "\n"
			loader.files[workingMemoryRelativePath] = []byte(after)
			loader.files["working/LEDGER.md"] = []byte(ledger)
			rangeCitation := strings.Contains(source, "..")
			if rangeCitation {
				loader.files["working/OPEN_LOOPS.md"] = []byte("# Open Loops\n")
			}
			_, err := (workingMemoryConsolidationJob{}).ApplyResult(
				context.Background(),
				loader,
				JobOutput{
					Spec: spec, Messages: workingMemoryTargetToolCall("write_file", workingMemoryRuntimePath),
				},
			)
			if rangeCitation {
				if err == nil || !strings.Contains(err.Error(), "invalid or duplicate ledger src") {
					t.Fatalf("range citation error = %v", err)
				}
				for path, content := range before {
					assertFileBytesRestored(t, loader, path, []byte(content))
				}
				return
			}
			if err != nil {
				t.Fatalf("post-repair point citation refused: %v", err)
			}
			assertFileBytesRestored(t, loader, workingMemoryRuntimePath, []byte(after))
			assertFileBytesRestored(t, loader, workingLedgerRuntimePath, []byte(ledger))
			assertFileBytesRestored(
				t,
				loader,
				openLoopsRuntimePath,
				[]byte(before[openLoopsRuntimePath]),
			)
		})
	}
}
