package cognition

import (
	"context"
	"fmt"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/agent"
)

const defaultOpenLoopsBudgetBytes = 4096

type memoryFileBudgets struct {
	working   int
	openLoops int
}

func checkWorkingFileBudgets(
	ctx context.Context,
	loader ContextLoader,
	budgets memoryFileBudgets,
) error {
	if loader == nil {
		return fmt.Errorf("context loader is required")
	}
	limits := []struct {
		path  string
		limit int
	}{
		{workingMemoryRuntimePath, effectiveWorkingMemoryBudget(budgets.working)},
		{openLoopsRuntimePath, effectiveOpenLoopsBudget(budgets.openLoops)},
	}
	for _, file := range limits {
		raw, _, err := loader.ReadFileBytes(ctx, strings.TrimPrefix(file.path, memoryRuntimePrefix))
		if err != nil {
			return fmt.Errorf("read budgeted memory file %s: %w", file.path, err)
		}
		if len(raw) > file.limit {
			return fmt.Errorf(
				"memory run left %s over byte budget (%d>%d)",
				file.path,
				len(raw),
				file.limit,
			)
		}
	}
	return nil
}

func memoryBudgetPromptSection(budgets memoryFileBudgets) agent.PromptSection {
	loops := effectiveOpenLoopsBudget(budgets.openLoops)
	return agent.PromptSection{
		Name: "memory_byte_budgets",
		Body: fmt.Sprintf(
			"Keep %s at or under %d bytes and %s at or under %d bytes. The complete file bytes, including whitespace, are checked when this run is applied; an over-budget result fails without truncation.",
			workingMemoryRuntimePath,
			effectiveWorkingMemoryBudget(budgets.working),
			openLoopsRuntimePath,
			loops,
		),
	}
}
