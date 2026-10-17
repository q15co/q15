package cognition

import (
	"context"
	"fmt"
	"strings"
	"time"
)

func checkOpenLoopLifecycle(ctx context.Context, loader ContextLoader, spec Spec) error {
	if loader == nil {
		return fmt.Errorf("context loader is required")
	}
	before, err := parseOpenLoops(spec.Baseline[openLoopsRuntimePath])
	if err != nil {
		return fmt.Errorf("parse pre-run open loops: %w", err)
	}
	afterRaw, _, err := loader.ReadFileBytes(ctx, "working/OPEN_LOOPS.md")
	if err != nil {
		return fmt.Errorf("read post-run open loops: %w", err)
	}
	after, err := parseOpenLoops(string(afterRaw))
	if err != nil {
		return fmt.Errorf("parse post-run open loops: %w", err)
	}
	remaining := make(map[string]bool, len(after))
	for _, loop := range after {
		remaining[loop.ID] = true
	}
	working, _, err := loader.ReadFileBytes(ctx, workingMemoryRelativePath)
	if err != nil {
		return fmt.Errorf("read promoted working memory: %w", err)
	}
	tasks := activeWorkingItems(string(working))
	for _, loop := range before {
		date, isDate := strings.CutPrefix(loop.Trigger, "date:")
		if !isDate {
			continue
		}
		if spec.RunAt.IsZero() {
			return fmt.Errorf("date-trigger validation requires run time")
		}
		if date > spec.RunAt.UTC().Format(time.DateOnly) {
			continue
		}
		if remaining[loop.ID] {
			return fmt.Errorf("due open loop %s was not promoted", loop.ID)
		}
		found := false
		for _, task := range tasks {
			if strings.Contains(task, "["+loop.ID+"]") &&
				strings.Contains(
					normalizeExitDescription(task),
					normalizeExitDescription(loop.Text),
				) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf(
				"due open loop %s is missing from Active Tasks or Pending Checks",
				loop.ID,
			)
		}
	}
	return nil
}

func activeWorkingItems(content string) []string {
	var selected strings.Builder
	active := false
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "#") {
			active = line == "## Active Tasks" || line == "## Pending Checks"
			continue
		}
		if active {
			selected.WriteString(line)
			selected.WriteByte('\n')
		}
	}
	return workingItems(selected.String())
}
