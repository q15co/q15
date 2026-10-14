package cognition

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/memory/memcheck"
	filetools "github.com/q15co/q15/systems/agent/internal/tools/files"
)

const (
	// semanticRootCapBytes and semanticTopicCapBytes are the post-run caps
	// for semantic files the conversion pass writes (DESIGN §2.3, §6.3).
	semanticRootCapBytes  = 16 * 1024
	semanticTopicCapBytes = 32 * 1024
	// defaultWorkingMemoryBudgetBytes is the working-memory budget whose
	// overrun triggers a conversion run (DESIGN §6.3); a registration built
	// with a configured budget replaces it.
	defaultWorkingMemoryBudgetBytes = 8 * 1024
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

// semanticConversionWritePaths returns the fixed runtime paths the conversion
// pass may write: the three semantic roots and the three working-level files
// it converts from and records into. Topic files under semantic/topics/ are
// covered by the write dir and are enumerated separately when snapshotted.
func semanticConversionWritePaths() []string {
	return append(
		semanticRuntimePaths(),
		workingMemoryRuntimePath,
		openLoopsRuntimePath,
		workingLedgerRuntimePath,
	)
}

// semanticConversionPolicy scopes the conversion pass to the semantic roots,
// direct children of semantic/topics, and the three working-level files it
// converts from and records into.
func semanticConversionPolicy() filetools.PathAccessPolicy {
	paths := semanticConversionWritePaths()
	return filetools.PathAccessPolicy{
		ReadPaths:  paths,
		WritePaths: append([]string(nil), paths...),
		ReadDirs:   []string{semanticTopicsRuntimeDir},
		WriteDirs:  []string{semanticTopicsRuntimeDir},
	}
}

// relativeMemoryPath strips the runtime prefix from a runtime path to recover
// the repository-relative slash path the loader accessors use.
func relativeMemoryPath(runtimePath string) string {
	return strings.TrimPrefix(runtimePath, memoryRuntimePrefix)
}

// evaluateWorkingMemoryOverbudget fires a conversion run when working memory
// exceeds its budget. Like every state rule it is only evaluated once head has
// advanced past the last run's input (shouldEvaluateStateRules), so it fires on
// the next turn after the file goes over budget, never on file size alone.
// The rule only reads: it never truncates or rewrites the file.
func evaluateWorkingMemoryOverbudget(
	ctx context.Context,
	snapshot Snapshot,
	budget int,
) (bool, string, error) {
	if snapshot.Loader == nil {
		return false, "", nil
	}
	effectiveBudget := effectiveWorkingMemoryBudget(budget)
	working, err := snapshot.Loader.LoadWorkingMemory(ctx)
	if err != nil {
		return false, "", fmt.Errorf("load working memory: %w", err)
	}
	size := len(working.Content)
	if !memcheck.OverLimit(int64(size), int64(effectiveBudget)) {
		return false, "", nil
	}
	return true, fmt.Sprintf(
		"working_memory_bytes=%d budget=%d",
		size,
		effectiveBudget,
	), nil
}

// effectiveWorkingMemoryBudget resolves the working-memory budget a rule
// enforces. A non-positive value selects defaultWorkingMemoryBudgetBytes:
// zero is the shape of a registration built without a budget, and a negative
// one is a configuration error caught at load, so neither can silently turn
// the budget into one the rule always or never trips.
func effectiveWorkingMemoryBudget(budget int) int {
	if budget <= 0 {
		return defaultWorkingMemoryBudgetBytes
	}
	return budget
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
		if ok && memcheck.OverLimit(size, int64(limit)) {
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

// SemanticCapBytes returns the post-run byte cap for a repository-relative
// semantic file path ("semantic/facts.md", "semantic/topics/an-agent.md"), and
// 0 when the path is not one the conversion pass caps. Read-only status
// reporting calls it so a report and this pass cannot disagree about the cap a
// file is judged against.
func SemanticCapBytes(relativePath string) int {
	return semanticCapFor(memoryRuntimePrefix + strings.TrimPrefix(relativePath, "/"))
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

// snapshotConversionFiles captures the pre-run on-disk state of every path the
// conversion pass may write, so a failed run can be rolled back byte-for-byte.
// It covers the fixed write paths and every existing topic file under the
// topics write dir. Topic files created by the run have no snapshot here;
// rollback deletes them by re-listing the dir afterwards.
func snapshotConversionFiles(
	ctx context.Context,
	loader ContextLoader,
) (map[string]FileSnapshot, error) {
	snapshots := make(map[string]FileSnapshot, len(semanticConversionWritePaths()))
	for _, runtimePath := range semanticConversionWritePaths() {
		raw, existed, err := loader.ReadFileBytes(ctx, relativeMemoryPath(runtimePath))
		if err != nil {
			return nil, fmt.Errorf("snapshot %s: %w", runtimePath, err)
		}
		snapshots[runtimePath] = FileSnapshot{Existed: existed, Content: raw}
	}

	sizes, err := loader.LoadSemanticFileSizes(ctx)
	if err != nil {
		return nil, fmt.Errorf("list semantic files to snapshot: %w", err)
	}
	for relative := range sizes {
		if path.Dir(relative) != relativeMemoryPath(semanticTopicsRuntimeDir) {
			continue
		}
		runtimePath := memoryRuntimePrefix + relative
		raw, existed, err := loader.ReadFileBytes(ctx, relative)
		if err != nil {
			return nil, fmt.Errorf("snapshot %s: %w", runtimePath, err)
		}
		snapshots[runtimePath] = FileSnapshot{Existed: existed, Content: raw}
	}
	return snapshots, nil
}

// restoreConversionFiles rolls a failed conversion run back to the snapshotted
// pre-run state: it writes every snapshotted file's bytes back verbatim and
// deletes any path that had no snapshot but exists now (a file the run
// created). The first failure is returned with the offending path named.
func restoreConversionFiles(
	ctx context.Context,
	loader ContextLoader,
	snapshots map[string]FileSnapshot,
) error {
	if len(snapshots) == 0 {
		return nil
	}
	for runtimePath, snap := range snapshots {
		relative := relativeMemoryPath(runtimePath)
		if snap.Existed {
			if err := loader.WriteFileBytes(ctx, relative, snap.Content); err != nil {
				return fmt.Errorf("restore %s: %w", runtimePath, err)
			}
			continue
		}
		if err := loader.DeleteFile(ctx, relative); err != nil {
			return fmt.Errorf("restore %s: %w", runtimePath, err)
		}
	}

	sizes, err := loader.LoadSemanticFileSizes(ctx)
	if err != nil {
		return fmt.Errorf("list semantic files to restore: %w", err)
	}
	for relative := range sizes {
		if path.Dir(relative) != relativeMemoryPath(semanticTopicsRuntimeDir) {
			continue
		}
		runtimePath := memoryRuntimePrefix + relative
		if snap, ok := snapshots[runtimePath]; ok && snap.Existed {
			continue
		}
		if err := loader.DeleteFile(ctx, relative); err != nil {
			return fmt.Errorf("restore %s: %w", runtimePath, err)
		}
	}
	return nil
}

// rollbackFailedRun restores the snapshotted pre-run state after a validation
// failure and returns an error carrying both the original failure and any
// rollback failure, so neither is lost.
func rollbackFailedRun(
	ctx context.Context,
	loader ContextLoader,
	snapshots map[string]FileSnapshot,
	validationErr error,
) error {
	if restoreErr := restoreConversionFiles(ctx, loader, snapshots); restoreErr != nil {
		return fmt.Errorf("%w (rollback also failed: %v)", validationErr, restoreErr)
	}
	return validationErr
}
