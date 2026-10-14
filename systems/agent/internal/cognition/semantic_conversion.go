package cognition

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/memory/claims"

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
// already oversized does not fail a run that leaves it untouched.
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
		if !ok {
			_, existed, readErr := loader.ReadFileBytes(
				ctx,
				strings.TrimPrefix(runtimePath, memoryRuntimePrefix),
			)
			if readErr != nil {
				return fmt.Errorf("inspect written semantic file %s: %w", runtimePath, readErr)
			}
			if !existed {
				continue
			}
			return fmt.Errorf(
				"written semantic file %s is missing from the file inventory",
				runtimePath,
			)
		}
		if memcheck.OverLimit(size, int64(limit)) {
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

// checkLedgerCompleteness validates structured exit evidence against complete
// working-memory items. Descriptions must preserve the entire item's text.
func checkLedgerCompleteness(
	ctx context.Context,
	loader ContextLoader,
	baseline map[string]string,
) (int, int, error) {
	before, ok := baseline[workingMemoryRuntimePath]
	if !ok {
		return 0, 0, fmt.Errorf("spec baseline is missing pre-run working memory")
	}
	working, _, err := loader.ReadFileBytes(ctx, workingMemoryRelativePath)
	if err != nil {
		return 0, 0, fmt.Errorf("load post-run working memory: %w", err)
	}
	entries, err := appendedWorkingLedgerEntries(ctx, loader, baseline, "semantic-memory run")
	if err != nil {
		return 0, 0, err
	}
	removed := removedWorkingItems(before, string(working))
	unmatched := make(map[string]int, len(removed))
	for _, item := range removed {
		unmatched[workingItemDescription(item)]++
	}
	exits := 0
	for _, entry := range entries {
		if entry.Run != semanticMemoryExtractionJobType {
			return 0, 0, fmt.Errorf("semantic-memory ledger entry has wrong run %q", entry.Run)
		}
		switch entry.Outcome {
		case claims.OutcomeConverted, claims.OutcomeMoved, claims.OutcomeDropped:
			description := normalizeExitDescription(entry.Description)
			if unmatched[description] == 0 {
				return 0, 0, fmt.Errorf(
					"ledger exit does not match an unrecorded removed working-memory item: %q",
					entry.Description,
				)
			}
			unmatched[description]--
			exits++
		}
	}
	if exits != len(removed) {
		for _, item := range removed {
			if unmatched[workingItemDescription(item)] > 0 {
				return 0, 0, fmt.Errorf(
					"semantic-memory run removed %d working-memory items but recorded %d matching exits in %s (first unrecorded: %q)",
					len(removed),
					exits,
					workingLedgerRuntimePath,
					compactSemanticMemoryText(item),
				)
			}
		}
	}
	if err := validateExitTargets(ctx, loader, entries); err != nil {
		return 0, 0, err
	}
	if err := checkPreservedOpenLoops(ctx, loader, baseline); err != nil {
		return 0, 0, err
	}
	return len(removed), len(entries), nil
}

// appendedWorkingLedgerEntries compares the raw bytes before parsing only the
// appended suffix; trimming would conceal edits to historical evidence.
func appendedWorkingLedgerEntries(
	ctx context.Context,
	loader ContextLoader,
	baseline map[string]string,
	runLabel string,
) ([]claims.LedgerEntry, error) {
	before, ok := baseline[workingLedgerRuntimePath]
	if !ok {
		return nil, fmt.Errorf("spec baseline is missing pre-run working ledger")
	}
	after, _, err := loader.ReadFileBytes(ctx, "working/LEDGER.md")
	if err != nil {
		return nil, fmt.Errorf("load post-run working ledger: %w", err)
	}
	if !strings.HasPrefix(string(after), before) {
		return nil, fmt.Errorf(
			"%s rewrote existing ledger bytes in %s; the ledger is append-only",
			runLabel,
			workingLedgerRuntimePath,
		)
	}
	suffix := string(after[len(before):])
	if suffix == "" {
		return nil, nil
	}
	if before != "" && !strings.HasSuffix(before, "\n") {
		if !strings.HasPrefix(suffix, "\n") {
			return nil, fmt.Errorf("%s appended inside an existing ledger line", runLabel)
		}
		suffix = strings.TrimPrefix(suffix, "\n")
		if suffix == "" {
			return nil, nil
		}
	}
	if !strings.HasSuffix(suffix, "\n") {
		return nil, fmt.Errorf("%s appended an unterminated ledger line", runLabel)
	}
	var entries []claims.LedgerEntry
	for _, line := range strings.Split(strings.TrimSuffix(suffix, "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		entry, err := claims.ParseLedgerEntry(line)
		if err != nil {
			return nil, fmt.Errorf("invalid appended ledger entry: %w", err)
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func removedWorkingItems(before, after string) []string {
	remaining := make(map[string]int)
	for _, item := range workingExitItems(after) {
		remaining[item]++
	}
	var removed []string
	for _, item := range workingExitItems(before) {
		if remaining[item] > 0 {
			remaining[item]--
			continue
		}
		removed = append(removed, item)
	}
	return removed
}

// workingExitItems retains every nonempty block outside the exact working-memory
// scaffold. Unlike claim and loop parsing, exit accounting cannot ignore prose,
// examples, or comments that may contain the only copy of working context.
func workingExitItems(content string) []string {
	var items []string
	var current []string
	listItem, inComment := false, false
	fence := ""
	flush := func() {
		item := strings.TrimRight(strings.Join(current, "\n"), "\n")
		if strings.TrimSpace(item) != "" && item != "- None" {
			items = append(items, item)
		}
		current = nil
		listItem = false
	}
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if inComment {
			current = append(current, line)
			if closeIndex := strings.LastIndex(line, "-->"); closeIndex >= 0 {
				inComment = strings.LastIndex(line, "<!--") > closeIndex
			}
			if !inComment && !listItem {
				flush()
			}
			continue
		}
		if fence != "" {
			current = append(current, line)
			if strings.HasPrefix(trimmed, fence) && strings.Trim(trimmed, fence[:1]) == "" {
				fence = ""
				if !listItem {
					flush()
				}
			}
			continue
		}
		if trimmed == "" {
			if listItem {
				current = append(current, line)
			} else {
				flush()
			}
			continue
		}
		if workingScaffoldHeading(line) {
			flush()
			continue
		}
		indented := line != strings.TrimLeft(line, " \t")
		if !indented &&
			(strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") || strings.HasPrefix(line, "+ ")) {
			flush()
			listItem = true
		} else if listItem && !indented && strings.TrimSpace(current[len(current)-1]) == "" {
			flush()
		}
		block := strings.HasPrefix(trimmed, "<!--") || strings.HasPrefix(trimmed, "```") ||
			strings.HasPrefix(trimmed, "~~~")
		if block && (!listItem || !indented) {
			flush()
		}
		current = append(current, line)
		if strings.Contains(line, "<!--") {
			inComment = strings.LastIndex(line, "<!--") > strings.LastIndex(line, "-->")
		}
		fenceText := workingItemDescription(trimmed)
		if strings.HasPrefix(fenceText, "```") || strings.HasPrefix(fenceText, "~~~") {
			fence = fenceText[:len(fenceText)-len(strings.TrimLeft(fenceText, fenceText[:1]))]
		}
		if block && !listItem && !inComment && fence == "" {
			flush()
		}
	}
	flush()
	return items
}

func workingScaffoldHeading(line string) bool {
	switch line {
	case "# Working Memory", "## Current Priorities", "## Active Tasks", "## Open Threads",
		"## Recent Progress", "## Pending Checks", "## Temporary Context":
		return true
	default:
		return false
	}
}

// workingItems extracts structured claims and loops with their continuation
// lines, ignoring documentation blocks and empty seed entries.
func workingItems(content string) []string {
	var items []string
	var current []string
	flush := func() {
		if len(current) == 0 {
			return
		}
		item := strings.TrimRight(strings.Join(current, "\n"), "\n")
		description := workingItemDescription(item)
		if description != "" && !strings.EqualFold(strings.Trim(description, "()."), "none") {
			items = append(items, item)
		}
		current = nil
	}
	inComment := false
	fence := ""
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if inComment {
			if strings.Contains(line, "-->") {
				inComment = false
			}
			continue
		}
		if strings.HasPrefix(trimmed, "<!--") {
			flush()
			inComment = !strings.Contains(trimmed, "-->")
			continue
		}
		if fence != "" {
			if strings.HasPrefix(line, fence) {
				fence = ""
			}
			continue
		}
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			flush()
			fence = line[:3]
			continue
		}
		if line == strings.TrimLeft(line, " \t") &&
			(strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") || strings.HasPrefix(line, "+ ")) {
			flush()
			current = []string{line}
			continue
		}
		if len(current) > 0 &&
			(strings.TrimSpace(line) == "" || line != strings.TrimLeft(line, " \t") ||
				(strings.TrimSpace(current[len(current)-1]) != "" && !strings.HasPrefix(line, "#"))) {
			current = append(current, line)
			continue
		}
		flush()
	}
	flush()
	return items
}

func workingItemDescription(item string) string {
	if strings.HasPrefix(item, "- ") || strings.HasPrefix(item, "* ") ||
		strings.HasPrefix(item, "+ ") {
		item = item[2:]
	}
	return normalizeExitDescription(item)
}

func normalizeExitDescription(text string) string { return strings.Join(strings.Fields(text), " ") }
