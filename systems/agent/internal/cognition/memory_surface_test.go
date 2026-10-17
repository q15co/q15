package cognition

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// TestScanMemorySurfaceExactlyAtCapIsNotRepairable pins the shared boundary
// rule: exactly at a cap or budget is within it, so none of the named files
// below is a target, and the uncapped ledger never is no matter its size.
func TestScanMemorySurfaceExactlyAtCapIsNotRepairable(t *testing.T) {
	t.Parallel()

	surface := ScanMemorySurface([]MemoryFile{
		{Path: "semantic/facts.md", Bytes: 16384},
		{Path: "semantic/preferences.md", Bytes: 100},
		{Path: "semantic/topics/an-agent.md", Bytes: 32768},
		{Path: "working/WORKING_MEMORY.md", Bytes: 8192},
		{Path: "working/OPEN_LOOPS.md", Bytes: 4096},
		{Path: "working/LEDGER.md", Bytes: 1024 * 1024},
	}, 8192, 4096)
	if len(surface.Targets) != 0 {
		t.Fatalf("targets = %#v, want none", surface.Targets)
	}
}

// TestScanMemorySurfaceNamedOverCapIsRepairable covers the migration group:
// a named file one byte over its limit is repairable, judged against the cap
// the schema names for it - the semantic cap for semantic files and the
// configured budget for the two budgeted working files.
func TestScanMemorySurfaceNamedOverCapIsRepairable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		file    MemoryFile
		wantCap int
	}{
		{"root over cap", MemoryFile{Path: "semantic/facts.md", Bytes: 16385}, 16384},
		{"working over budget", MemoryFile{Path: "working/WORKING_MEMORY.md", Bytes: 8193}, 8192},
		{"open loops over budget", MemoryFile{Path: "working/OPEN_LOOPS.md", Bytes: 4097}, 4096},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			surface := ScanMemorySurface([]MemoryFile{test.file}, 8192, 4096)
			if len(surface.Targets) != 1 {
				t.Fatalf("targets = %#v, want exactly one", surface.Targets)
			}
			target := surface.Targets[0]
			want := MemorySurfaceTarget{
				Path:       test.file.Path,
				Bytes:      test.file.Bytes,
				Cap:        test.wantCap,
				Named:      true,
				OverCap:    true,
				Repairable: true,
			}
			if target != want {
				t.Fatalf("target = %#v, want %#v", target, want)
			}
		})
	}
}

// TestScanMemorySurfaceNonPositiveBudgetsSelectDefaults pins that the scan
// and the post-run budget check agree on limits when the job holds no
// configured budget: a non-positive value selects the built-in default.
func TestScanMemorySurfaceNonPositiveBudgetsSelectDefaults(t *testing.T) {
	t.Parallel()

	surface := ScanMemorySurface([]MemoryFile{
		{Path: "working/WORKING_MEMORY.md", Bytes: 8193},
		{Path: "working/OPEN_LOOPS.md", Bytes: 4096},
	}, 0, 0)
	if len(surface.Targets) != 1 {
		t.Fatalf("targets = %#v, want only working memory", surface.Targets)
	}
	target := surface.Targets[0]
	if target.Path != "working/WORKING_MEMORY.md" || target.Cap != 8192 {
		t.Fatalf("target = %#v, want working memory judged against the default budget", target)
	}
}

// TestScanMemorySurfaceTopicAtThirtyThreeKBIsRepairable pins the topic-file
// case from the ticket: a topic file past its 32 KB cap is a repairable
// target judged against that cap.
func TestScanMemorySurfaceTopicAtThirtyThreeKBIsRepairable(t *testing.T) {
	t.Parallel()

	surface := ScanMemorySurface([]MemoryFile{
		{Path: "semantic/topics/an-agent.md", Bytes: 33 * 1024},
	}, 8192, 4096)
	if len(surface.Targets) != 1 {
		t.Fatalf("targets = %#v, want exactly one", surface.Targets)
	}
	target := surface.Targets[0]
	if !target.Repairable || !target.OverCap || !target.Named {
		t.Fatalf("target = %#v, want named, over cap and repairable", target)
	}
	if target.Cap != 32*1024 {
		t.Fatalf("target.Cap = %d, want %d", target.Cap, 32*1024)
	}
}

// TestScanMemorySurfaceUnNamedPathIsRepairable covers the adopt group: a
// path no schema entry owns is repairable, marked un-named with no cap, and
// never over a cap it does not have.
func TestScanMemorySurfaceUnNamedPathIsRepairable(t *testing.T) {
	t.Parallel()

	surface := ScanMemorySurface([]MemoryFile{
		{Path: "semantic/state/head.json", Bytes: 512},
		{Path: "working/scratch.tmp", Bytes: 12},
		{Path: "semantic/topics/nested/deep.md", Bytes: 99},
	}, 8192, 4096)
	if len(surface.Targets) != 3 {
		t.Fatalf("targets = %#v, want all three un-named files", surface.Targets)
	}
	for _, target := range surface.Targets {
		if target.Named || !target.Repairable || target.OverCap || target.Cap != 0 {
			t.Fatalf(
				"target = %#v, want un-named, repairable, never over cap, cap 0",
				target,
			)
		}
	}
}

// TestScanMemorySurfaceOrderingIsDeterministic pins the target order: the
// named-and-over-cap migration group first, then the un-named adopt group,
// each by descending size with the path as tie-break, independent of the
// listing order the scan was handed.
func TestScanMemorySurfaceOrderingIsDeterministic(t *testing.T) {
	t.Parallel()

	files := []MemoryFile{
		{Path: "working/ledger/2026-Q3.md", Bytes: 9000},
		{Path: "semantic/facts.md", Bytes: 20000},
		{Path: "working/WORKING_MEMORY.md", Bytes: 100},
		{Path: "semantic/topics/zeta.md", Bytes: 40000},
		{Path: "semantic/state/stale.json", Bytes: 9000},
		{Path: "semantic/topics/alpha.md", Bytes: 40000},
	}

	surface := ScanMemorySurface(files, 8192, 4096)
	wantPaths := []string{
		// Named over cap, descending size, path tie-break at 40,000.
		"semantic/topics/alpha.md",
		"semantic/topics/zeta.md",
		"semantic/facts.md",
		// Un-named, descending size, path tie-break at 9,000. The rotated
		// ledger segment in the listing is named ledger history, so it never
		// appears among the targets at all.
		"semantic/state/stale.json",
	}
	gotPaths := make([]string, 0, len(surface.Targets))
	for _, target := range surface.Targets {
		gotPaths = append(gotPaths, target.Path)
	}
	if !reflect.DeepEqual(gotPaths, wantPaths) {
		t.Fatalf("target order = %v, want %v", gotPaths, wantPaths)
	}

	again := ScanMemorySurface(files, 8192, 4096)
	if !reflect.DeepEqual(again, surface) {
		t.Fatalf("two scans disagree:\nfirst: %#v\nsecond: %#v", surface, again)
	}
}

// TestScanMemorySurfaceSkipsPathsOutsideContentRoots pins the perimeter:
// nothing outside semantic/ and working/ is ever in scope, not even as
// un-named.
func TestScanMemorySurfaceSkipsPathsOutsideContentRoots(t *testing.T) {
	t.Parallel()

	surface := ScanMemorySurface([]MemoryFile{
		{Path: "core/AGENT.md", Bytes: 100000},
		{Path: "history/state/head.json", Bytes: 512},
		{Path: "notes/inbox/todo.md", Bytes: 64},
		{Path: "cognition/state/semantic_extraction_checkpoint.json", Bytes: 128},
		{Path: "README.md", Bytes: 32},
	}, 8192, 4096)
	if len(surface.Targets) != 0 {
		t.Fatalf("targets = %#v, want none outside the content roots", surface.Targets)
	}
}

// surfaceProbeLoader drives the surface trigger with a canned listing or a
// canned listing error.
type surfaceProbeLoader struct {
	semanticMemoryJobLoader
	memoryFiles []MemoryFile
	listErr     error
}

func (l *surfaceProbeLoader) ListMemoryFiles(context.Context) ([]MemoryFile, error) {
	return l.memoryFiles, l.listErr
}

var _ ContextLoader = (*surfaceProbeLoader)(nil)

func TestEvaluateMemorySurfaceRepairable(t *testing.T) {
	t.Parallel()

	t.Run("nil loader does not fire", func(t *testing.T) {
		t.Parallel()
		run, reason, err := evaluateMemorySurfaceRepairable(
			context.Background(),
			Snapshot{},
			memoryFileBudgets{},
		)
		if err != nil || run || reason != "" {
			t.Fatalf("run=%v reason=%q err=%v, want false \"\" nil", run, reason, err)
		}
	})

	t.Run("clean surface does not fire", func(t *testing.T) {
		t.Parallel()
		loader := &surfaceProbeLoader{memoryFiles: []MemoryFile{
			{Path: "semantic/facts.md", Bytes: 100},
			{Path: "working/WORKING_MEMORY.md", Bytes: 8192},
			{Path: "working/LEDGER.md", Bytes: 4096},
		}}
		run, reason, err := evaluateMemorySurfaceRepairable(
			context.Background(),
			Snapshot{Loader: loader},
			memoryFileBudgets{working: 8192, openLoops: 4096},
		)
		if err != nil || run || reason != "" {
			t.Fatalf("run=%v reason=%q err=%v, want false \"\" nil", run, reason, err)
		}
	})

	t.Run("a repairable target fires", func(t *testing.T) {
		t.Parallel()
		loader := &surfaceProbeLoader{memoryFiles: []MemoryFile{
			{Path: "semantic/state/head.json", Bytes: 512},
		}}
		run, reason, err := evaluateMemorySurfaceRepairable(
			context.Background(),
			Snapshot{Loader: loader},
			memoryFileBudgets{working: 8192, openLoops: 4096},
		)
		if err != nil || !run {
			t.Fatalf("run=%v err=%v, want true nil", run, err)
		}
		for _, want := range []string{"repairable_targets=1", "semantic/state/head.json"} {
			if !strings.Contains(reason, want) {
				t.Fatalf("reason %q does not contain %q", reason, want)
			}
		}
	})

	t.Run("a listing error is returned", func(t *testing.T) {
		t.Parallel()
		loader := &surfaceProbeLoader{listErr: errors.New("walk failed")}
		run, _, err := evaluateMemorySurfaceRepairable(
			context.Background(),
			Snapshot{Loader: loader},
			memoryFileBudgets{},
		)
		if err == nil || run {
			t.Fatalf("err=%v run=%v, want an error and no run", err, run)
		}
		if !strings.Contains(err.Error(), "list memory files") {
			t.Fatalf("error %q does not name the listing step", err)
		}
	})
}

// TestScanMemorySurfaceRotatedLedgerSegmentIsNotRepairable pins the store's
// own rotation archive: a segment under working/ledger/ is ledger history,
// named and uncapped like the active ledger, so it is never a repair target
// however large it grows. Classifying it as un-named would hand the repair
// pass a licence to drain and delete the store's conversion history.
func TestScanMemorySurfaceRotatedLedgerSegmentIsNotRepairable(t *testing.T) {
	t.Parallel()

	surface := ScanMemorySurface([]MemoryFile{
		{Path: "working/ledger/2026-Q3.md", Bytes: 9000},
		{Path: "working/ledger/2026-Q4.md", Bytes: 512 * 1024},
		{Path: "working/LEDGER.md", Bytes: 4096},
	}, 8192, 4096)
	if len(surface.Targets) != 0 {
		t.Fatalf(
			"targets = %#v, want none: a rotated ledger segment is not a repair target",
			surface.Targets,
		)
	}
}

// TestSemanticRegistrationSurfaceRule pins the wiring: the semantic job
// carries a memory_surface_repairable state rule, and it judges the budgeted
// working files against the budgets the registration was built with.
func TestSemanticRegistrationSurfaceRule(t *testing.T) {
	t.Parallel()

	var rule StateRule
	found := false
	for _, candidate := range NewSemanticMemoryExtractionRegistration(8192, 4096).Policy.State {
		if candidate.ID == "memory_surface_repairable" {
			rule = candidate
			found = true
			break
		}
	}
	if !found {
		t.Fatal("registration has no memory_surface_repairable state rule")
	}

	clean := &surfaceProbeLoader{memoryFiles: []MemoryFile{
		{Path: "working/WORKING_MEMORY.md", Bytes: 8192},
	}}
	run, _, err := rule.Evaluate(context.Background(), Snapshot{Loader: clean}, JobState{})
	if err != nil || run {
		t.Fatalf("at-budget working memory: run=%v err=%v, want false nil", run, err)
	}

	over := &surfaceProbeLoader{memoryFiles: []MemoryFile{
		{Path: "working/WORKING_MEMORY.md", Bytes: 8193},
	}}
	run, _, err = rule.Evaluate(context.Background(), Snapshot{Loader: over}, JobState{})
	if err != nil || !run {
		t.Fatalf("over-budget working memory: run=%v err=%v, want true nil", run, err)
	}
}
