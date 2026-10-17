package cognition

import (
	"context"
	"strings"
	"testing"
)

func TestWorkingConsolidationDefersOversizedInputToBoundedRepair(t *testing.T) {
	for _, limit := range []int{0, 64} {
		effective := effectiveWorkingMemoryBudget(limit)
		for _, size := range []int{effective, effective + 1} {
			loader := &workingMemoryJobLoader{files: map[string][]byte{
				workingMemoryRelativePath: []byte(strings.Repeat("x", size)),
				openLoopsRelativePath:     []byte("# Open Loops\n"),
				workingLedgerRelativePath: []byte(repairLedgerPre),
			}}
			spec, err := NewWorkingMemoryConsolidationRegistration(
				limit,
				0,
			).NewJob().
				Build(context.Background(), loader)
			if err != nil {
				t.Fatal(err)
			}
			if size <= effective {
				if spec.NoWork != nil {
					t.Fatal("deferred an input already within its configured budget")
				}
			} else {
				if spec.NoWork == nil {
					t.Fatal("oversized input reached the model instead of bounded repair")
				}
				if spec.MemoryRecoveryID != "" || spec.ExposeTools {
					t.Fatal("deferral started a model mutation run")
				}
				if string(loader.files[workingMemoryRelativePath]) != strings.Repeat("x", size) {
					t.Fatal("deferral changed working memory")
				}
			}
		}
	}
}
