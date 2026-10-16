package cognition

import (
	"context"
	"fmt"
)

func checkPreservedOpenLoops(
	ctx context.Context,
	loader ContextLoader,
	baseline map[string]string,
) error {
	before, present := baseline[openLoopsRuntimePath]
	if !present {
		return fmt.Errorf("spec baseline is missing pre-run open loops")
	}
	previous, err := parseOpenLoops(before)
	if err != nil {
		return fmt.Errorf("parse pre-run open loops: %w", err)
	}
	raw, _, err := loader.ReadFileBytes(ctx, "working/OPEN_LOOPS.md")
	if err != nil {
		return fmt.Errorf("read post-run open loops: %w", err)
	}
	current, err := parseOpenLoops(string(raw))
	if err != nil {
		return fmt.Errorf("parse post-run open loops: %w", err)
	}
	items := make(map[string]string, len(current))
	for _, loop := range current {
		items[loop.ID] = loop.Item
	}
	for _, loop := range previous {
		if items[loop.ID] != loop.Item {
			return fmt.Errorf(
				"semantic-memory run modified or removed existing open loop %s; working-memory maintenance owns its lifecycle",
				loop.ID,
			)
		}
	}
	return nil
}
