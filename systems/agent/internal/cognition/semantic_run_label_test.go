package cognition

import (
	"fmt"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/agent"
)

func TestSemanticConversionRejectsExitRecordsFromAnotherRun(t *testing.T) {
	t.Parallel()

	for _, run := range []string{workingMemoryConsolidationJobType, "manual"} {
		t.Run(run, func(t *testing.T) {
			t.Parallel()
			loader := &semanticMemoryJobLoader{
				working: agent.WorkingMemory{RelativePath: workingMemoryRelativePath},
				ledger: fmt.Sprintf(
					"2026-10-15T05:00Z run=%s src=t11 dropped \"A resolved working item.\" (no future value)\n",
					run,
				),
			}
			_, err := applyConversion(t, loader, map[string]string{
				workingMemoryRuntimePath: "# Working Memory\n\n- A resolved working item.\n",
				workingLedgerRuntimePath: "",
				openLoopsRuntimePath:     "",
			}, workingMemoryRuntimePath, workingLedgerRuntimePath)
			if err == nil || !strings.Contains(err.Error(), "wrong run") {
				t.Fatalf("ApplyResult error = %v, want rejection of run %q", err, run)
			}
		})
	}
}
