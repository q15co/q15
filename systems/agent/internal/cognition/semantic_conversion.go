package cognition

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	filetools "github.com/q15co/q15/systems/agent/internal/tools/files"
)

const (
	// semanticRootCapBytes and semanticTopicCapBytes are the post-run caps
	// for semantic files the conversion pass writes (DESIGN §2.3, §6.3).
	semanticRootCapBytes  = 16 * 1024
	semanticTopicCapBytes = 32 * 1024
	// workingMemoryBudgetBytes is the working-memory budget whose overrun
	// triggers a conversion run (DESIGN §6.3).
	workingMemoryBudgetBytes = 8 * 1024
)

// Runtime paths derived from the semantic root constant so every path the
// conversion pass is scoped to shares one runtime prefix.
var (
	memoryRuntimePrefix = strings.TrimSuffix(
		semanticFactsRuntimePath,
		semanticFactsRelativePath,
	)
	semanticTopicsRuntimeDir = memoryRuntimePrefix + "semantic/topics"
	openLoopsRuntimePath     = memoryRuntimePrefix + "working/OPEN_LOOPS.md"
	workingLedgerRuntimePath = memoryRuntimePrefix + "working/LEDGER.md"
)

// semanticConversionPolicy scopes the conversion pass to the semantic roots,
// direct children of semantic/topics, and the three working-level files it
// converts from and records into.
func semanticConversionPolicy() filetools.PathAccessPolicy {
	paths := append(
		semanticRuntimePaths(),
		workingMemoryRuntimePath,
		openLoopsRuntimePath,
		workingLedgerRuntimePath,
	)
	return filetools.PathAccessPolicy{
		ReadPaths:  paths,
		WritePaths: append([]string(nil), paths...),
		ReadDirs:   []string{semanticTopicsRuntimeDir},
		WriteDirs:  []string{semanticTopicsRuntimeDir},
	}
}

// evaluateWorkingMemoryOverbudget fires a conversion run when working memory
// exceeds its budget. Like every state rule it is only evaluated once head has
// advanced past the last run's input (shouldEvaluateStateRules), so it fires on
// the next turn after the file goes over budget, never on file size alone.
func evaluateWorkingMemoryOverbudget(
	ctx context.Context,
	snapshot Snapshot,
	_ JobState,
) (bool, string, error) {
	if snapshot.Loader == nil {
		return false, "", nil
	}
	working, err := snapshot.Loader.LoadWorkingMemory(ctx)
	if err != nil {
		return false, "", fmt.Errorf("load working memory: %w", err)
	}
	size := len(working.Content)
	if size <= workingMemoryBudgetBytes {
		return false, "", nil
	}
	return true, fmt.Sprintf(
		"working_memory_bytes=%d budget=%d",
		size,
		workingMemoryBudgetBytes,
	), nil
}

// checkSemanticCaps fails when a semantic file this run wrote is over its cap
// afterwards. Files the run did not write are not judged, so a file that was
// already oversized does not fail every run until it is migrated.
func checkSemanticCaps(
	ctx context.Context,
	loader ContextLoader,
	messages []conversation.Message,
) error {
	written := successfulWritePaths(messages)
	if len(written) == 0 {
		return nil
	}
	sizes, err := loader.LoadSemanticFileSizes(ctx)
	if err != nil {
		return fmt.Errorf("load semantic file sizes: %w", err)
	}
	var over []string
	for _, runtimePath := range written {
		limit := semanticCapFor(runtimePath)
		if limit <= 0 {
			continue
		}
		size, ok := sizes[strings.TrimPrefix(runtimePath, memoryRuntimePrefix)]
		if ok && size > int64(limit) {
			over = append(over, fmt.Sprintf("%s=%d>%d", runtimePath, size, limit))
		}
	}
	if len(over) > 0 {
		return fmt.Errorf(
			"semantic-memory run left written files over cap (%s)",
			strings.Join(over, ", "),
		)
	}
	return nil
}

func semanticCapFor(runtimePath string) int {
	for _, root := range semanticRuntimePaths() {
		if runtimePath == root {
			return semanticRootCapBytes
		}
	}
	if path.Dir(runtimePath) == semanticTopicsRuntimeDir {
		return semanticTopicCapBytes
	}
	return 0
}

// successfulWritePaths returns the cleaned paths of file-tool writes whose
// tool result was not an error, in first-seen order.
func successfulWritePaths(messages []conversation.Message) []string {
	calls := make(map[string]filetools.FileToolAccess)
	for _, call := range conversation.ToolCalls(messages) {
		access, ok, err := filetools.InspectToolCallAccess(agent.ToolCall{
			ID:        call.ID,
			Name:      call.Name,
			Arguments: call.Arguments,
		})
		if err != nil || !ok {
			continue
		}
		calls[strings.TrimSpace(call.ID)] = access
	}
	seen := make(map[string]struct{})
	var out []string
	for _, msg := range messages {
		if msg.Role != conversation.ToolRole {
			continue
		}
		for _, part := range msg.Parts {
			if part.Type != conversation.ToolResultPartType || part.IsError {
				continue
			}
			access, ok := calls[strings.TrimSpace(part.ToolCallID)]
			if !ok {
				continue
			}
			for _, written := range access.WritePaths {
				if _, dup := seen[written]; dup {
					continue
				}
				seen[written] = struct{}{}
				out = append(out, written)
			}
		}
	}
	return out
}

// checkLedgerCompleteness diffs working memory against the Build baseline and
// requires one appended ledger line per removed working-memory bullet. It also
// requires the pre-run ledger to survive as a prefix, because file tools
// rewrite the whole file instead of appending with O_APPEND.
func checkLedgerCompleteness(
	ctx context.Context,
	loader ContextLoader,
	baseline map[string]string,
) (int, int, error) {
	before, ok := baseline[workingMemoryRuntimePath]
	if !ok {
		return 0, 0, fmt.Errorf("spec baseline is missing pre-run working memory")
	}
	beforeLedger := baseline[workingLedgerRuntimePath]

	working, err := loader.LoadWorkingMemory(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("load post-run working memory: %w", err)
	}
	afterLedger, err := loader.LoadWorkingLedger(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("load post-run working ledger: %w", err)
	}
	afterLedger = strings.TrimSpace(afterLedger)
	if !strings.HasPrefix(afterLedger, beforeLedger) {
		return 0, 0, fmt.Errorf(
			"semantic-memory run rewrote existing ledger lines in %s; the ledger is append-only",
			workingLedgerRuntimePath,
		)
	}
	appended := countNonEmptyLines(afterLedger[len(beforeLedger):])
	removed := removedWorkingItems(before, strings.TrimSpace(working.Content))
	if len(removed) > appended {
		return 0, 0, fmt.Errorf(
			"semantic-memory run removed %d working-memory items but appended %d ledger lines to %s (first unrecorded: %q)",
			len(removed),
			appended,
			workingLedgerRuntimePath,
			compactSemanticMemoryText(removed[0]),
		)
	}
	return len(removed), appended, nil
}

// removedWorkingItems returns the bullet lines of before that are missing from
// after, counted as a multiset. A rewritten bullet counts as removed.
// Empty-section placeholders such as "- None" are not items.
func removedWorkingItems(before, after string) []string {
	remaining := make(map[string]int)
	for _, item := range workingItems(after) {
		remaining[item]++
	}
	var removed []string
	for _, item := range workingItems(before) {
		if remaining[item] > 0 {
			remaining[item]--
			continue
		}
		removed = append(removed, item)
	}
	return removed
}

func workingItems(content string) []string {
	var items []string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		text, ok := strings.CutPrefix(line, "- ")
		if !ok {
			text, ok = strings.CutPrefix(line, "* ")
		}
		if !ok {
			continue
		}
		text = strings.TrimSpace(text)
		if text == "" || strings.EqualFold(strings.Trim(text, "()."), "none") {
			continue
		}
		items = append(items, line)
	}
	return items
}

func countNonEmptyLines(text string) int {
	count := 0
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}
