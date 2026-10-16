package cognition

import (
	"context"
	"strings"
	"testing"
)

type budgetTestLoader struct {
	*spyLoader
	files map[string][]byte
}

func (l budgetTestLoader) ReadFileBytes(_ context.Context, path string) ([]byte, bool, error) {
	data, ok := l.files[path]
	return data, ok, nil
}

func TestAppliedMemoryBudgetsUseRawBytesAndConfiguredLimits(t *testing.T) {
	for _, path := range []string{workingMemoryRuntimePath, openLoopsRuntimePath} {
		t.Run(path, func(t *testing.T) {
			l := budgetTestLoader{
				spyLoader: &spyLoader{},
				files: map[string][]byte{
					strings.TrimPrefix(path, memoryRuntimePrefix): []byte("  x "),
				},
			}
			if err := checkWorkingFileBudgets(context.Background(), l, memoryFileBudgets{working: 4, openLoops: 4}); err != nil {
				t.Fatal(err)
			}
			l.files[strings.TrimPrefix(path, memoryRuntimePrefix)] = []byte("  x  ")
			if err := checkWorkingFileBudgets(context.Background(), l, memoryFileBudgets{working: 4, openLoops: 4}); err == nil ||
				!strings.Contains(err.Error(), path) {
				t.Fatalf("over-budget apply error = %v", err)
			}
		})
	}
}

func TestBothMemoryJobsRejectOverBudgetResults(t *testing.T) {
	l := budgetTestLoader{
		spyLoader: &spyLoader{},
		files: map[string][]byte{
			strings.TrimPrefix(workingMemoryRuntimePath, memoryRuntimePrefix): []byte(
				"over budget",
			),
		},
	}
	working := NewWorkingMemoryConsolidationRegistration(4, 4).NewJob()
	if _, err := working.ApplyResult(context.Background(), l, JobOutput{
		Spec: Spec{Baseline: map[string]string{
			workingMemoryRuntimePath: "over budget",
			openLoopsRuntimePath:     "",
			workingLedgerRuntimePath: "",
		}},
		Messages: conversionRunMessages(workingMemoryRuntimePath),
	}); err == nil ||
		!strings.Contains(err.Error(), "byte budget") {
		t.Fatalf("working ApplyResult = %v", err)
	}
	conversion := NewSemanticMemoryExtractionRegistration(4, 4).NewJob()
	if _, err := conversion.ApplyResult(context.Background(), l, JobOutput{Spec: Spec{Baseline: emptyConversionBaseline()}, Messages: conversionRunMessages()}); err == nil ||
		!strings.Contains(err.Error(), "byte budget") {
		t.Fatalf("conversion ApplyResult = %v", err)
	}
}
