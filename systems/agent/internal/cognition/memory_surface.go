package cognition

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/memory/memcheck"
)

// MemoryFile is one regular file under a memory content root, identified by
// its repository-relative slash path ("semantic/topics/an-agent.md") and its
// on-disk byte size. Listings of MemoryFile values come from the store's
// stat-only enumeration: no file contents are ever read to build one.
type MemoryFile struct {
	Path  string
	Bytes int64
}

// MemorySurfaceTarget is one file under the content roots that does not fit
// the named schema the memory runtime maintains, classified with the
// disposition the schema assigns to its path.
type MemorySurfaceTarget struct {
	// Path is the repository-relative slash path.
	Path string
	// Bytes is the on-disk size the listing measured.
	Bytes int64
	// Cap is the byte limit the schema names for the path: the post-run cap
	// for semantic files, and the working-memory or open-loops budget for the
	// two budgeted working files. It is 0 when the path is un-named or
	// uncapped.
	Cap int
	// Named is false when no schema entry owns the path; an un-named file is
	// adopted: its lines become claims or recorded drops, then the file is
	// removed.
	Named bool
	// OverCap is Bytes > Cap and only meaningful when Cap > 0.
	OverCap bool
	// Repairable marks a file the repair pass can act on: OverCap || !Named.
	Repairable bool
}

// MemorySurface is the two content roots classified against the named schema.
type MemorySurface struct {
	// Targets holds only the repairable files, ordered: named-and-over-cap
	// first (they are the migration), then un-named, each group by descending
	// size with the path as the tie-break.
	Targets []MemorySurfaceTarget
}

// Relative spellings of the schema's named working files, so the surface and
// the conversion pass name the same paths. The semantic side of the schema is
// not spelled here on purpose: SemanticCapBytes owns it, and caps semantic
// roots and direct topic children the same way the pass does.
const (
	openLoopsRelativePath     = "working/OPEN_LOOPS.md"
	workingLedgerRelativePath = "working/LEDGER.md"
	// workingLedgerDirRelativePath is the directory the store rotates the ledger
	// into once it passes its rotation threshold. A rotated segment is ledger
	// history - read back by LoadConversionLedgerHistory and counted by the
	// status report - so it is named and uncapped exactly like the active
	// ledger, and never an un-named file to adopt and delete.
	workingLedgerDirRelativePath = "working/ledger/"
)

// ScanMemorySurface classifies a stat-only listing against the named schema
// and reports the repairable subset. Nothing outside semantic/ and working/
// is ever in scope: such paths are skipped rather than classified. The two
// budgets are the ones the budgeted working files are judged against; a
// non-positive value selects the built-in default, so the scan and the
// post-run budget check cannot disagree about the limit a file is over. The
// classification is pure: it reads nothing and touches no store.
func ScanMemorySurface(
	files []MemoryFile,
	workingMemoryBudgetBytes, openLoopsBudgetBytes int,
) MemorySurface {
	workingCap := effectiveWorkingMemoryBudget(workingMemoryBudgetBytes)
	openLoopsCap := effectiveOpenLoopsBudget(openLoopsBudgetBytes)

	var overCap []MemorySurfaceTarget
	var unNamed []MemorySurfaceTarget
	for _, file := range files {
		target, inScope := classifyMemorySurfaceFile(file, workingCap, openLoopsCap)
		if !inScope || !target.Repairable {
			continue
		}
		if target.Named {
			overCap = append(overCap, target)
		} else {
			unNamed = append(unNamed, target)
		}
	}
	sortMemorySurfaceTargets(overCap)
	sortMemorySurfaceTargets(unNamed)
	return MemorySurface{Targets: append(overCap, unNamed...)}
}

// classifyMemorySurfaceFile assigns one listed file its place in the named
// schema. The second return is false for paths outside the two content
// roots: they are not part of the surface at all.
func classifyMemorySurfaceFile(
	file MemoryFile,
	workingCap, openLoopsCap int,
) (MemorySurfaceTarget, bool) {
	relative := strings.TrimPrefix(file.Path, "/")
	if !strings.HasPrefix(relative, "semantic/") &&
		!strings.HasPrefix(relative, "working/") {
		return MemorySurfaceTarget{}, false
	}

	if strings.HasPrefix(relative, workingLedgerDirRelativePath) {
		return MemorySurfaceTarget{Path: relative, Bytes: file.Bytes, Named: true}, true
	}

	target := MemorySurfaceTarget{Path: relative, Bytes: file.Bytes, Named: true}
	switch relative {
	case workingMemoryRelativePath:
		target.Cap = workingCap
	case openLoopsRelativePath:
		target.Cap = openLoopsCap
	case workingLedgerRelativePath:
		// The active ledger is named but carries no byte limit.
	default:
		// 0 from SemanticCapBytes means no schema entry owns the path: an
		// un-named file, which the repair pass adopts and then removes.
		target.Cap = SemanticCapBytes(relative)
		target.Named = target.Cap > 0
	}
	if target.Cap > 0 {
		target.OverCap = memcheck.OverLimit(file.Bytes, int64(target.Cap))
	}
	target.Repairable = target.OverCap || !target.Named
	return target, true
}

// sortMemorySurfaceTargets orders one group by descending size with the path
// as the tie-break, so a surface never depends on the listing order it was
// built from.
func sortMemorySurfaceTargets(targets []MemorySurfaceTarget) {
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].Bytes != targets[j].Bytes {
			return targets[i].Bytes > targets[j].Bytes
		}
		return targets[i].Path < targets[j].Path
	})
}

// evaluateMemorySurfaceRepairable is the semantic job's cheap surface
// trigger: it fires when the stat-only scan sees at least one repairable
// target that no durable repair refusal suppresses. Like every state rule it
// only reads; on top of the scan - which stats every file and opens none -
// it reads the one small refusal artifact, so evaluating it never turns into
// hidden file I/O beyond that one record. The refusal filter is what stops
// the rule from firing forever on a target no repair run can drain. A clean
// or fully refused surface changes nothing about the job's other triggers.
func evaluateMemorySurfaceRepairable(
	ctx context.Context,
	snapshot Snapshot,
	budgets memoryFileBudgets,
) (bool, string, error) {
	if snapshot.Loader == nil {
		return false, "", nil
	}
	files, err := snapshot.Loader.ListMemoryFiles(ctx)
	if err != nil {
		return false, "", fmt.Errorf("list memory files: %w", err)
	}
	surface := ScanMemorySurface(files, budgets.working, budgets.openLoops)
	if len(surface.Targets) == 0 {
		return false, "", nil
	}
	refusals, err := loadRepairRefusals(ctx, snapshot.Loader)
	if err != nil {
		return false, "", fmt.Errorf("load repair refusals: %w", err)
	}
	targets := withoutRefusedRepairTargets(surface.Targets, refusals)
	if len(targets) == 0 {
		return false, "", nil
	}
	first := targets[0]
	return true, fmt.Sprintf(
		"repairable_targets=%d first_path=%s first_bytes=%d",
		len(targets),
		first.Path,
		first.Bytes,
	), nil
}
