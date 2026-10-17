package memory

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/q15co/q15/systems/agent/internal/memoryrepo"
)

// statusNow pins the instant every age in the fixtures is measured against.
var statusNow = time.Date(2026, 10, 13, 9, 12, 33, 0, time.UTC)

// statusFixtureLimits are the budgets the fixture roots are judged against:
// the documented configuration defaults.
func statusFixtureLimits() StatusLimits {
	return StatusLimits{WorkingMemoryBudgetBytes: 8192, OpenLoopsBudgetBytes: 4096}
}

// wantStatusReport is the golden report for the full fixture. It pins the
// frozen-checkpoint lesson in one string: the semantic-extraction checkpoint
// sits 83 turns behind a head that kept moving, its file line is marked over
// budget and over cap, one job has been failing twice in a row, and the
// surface section names both repairable targets with the on-disk bytes the
// trigger will judge - for working memory one byte more than the loaded
// content the file line reports, because the file on disk ends in a newline.
const wantStatusReport = `head seq 4183 at 2026-10-13T09:10:33Z
checkpoint consolidation seq 4180 lag 3 age 4m33s at 2026-10-13T09:08:00Z
checkpoint semantic-extraction seq 4100 lag 83 age 31h12m33s at 2026-10-12T02:00:00Z
job working_memory.consolidate failures 0 last-run 2026-10-12T09:04:00Z last-success 2026-10-12T09:04:00Z last-failure never
job semantic_memory.extract failures 2 last-run 2026-10-12T02:11:33Z last-success 2026-10-11T14:12:00Z last-failure 2026-10-12T02:11:33Z
job verification_review failures 0 last-run never last-success never last-failure never
file working/WORKING_MEMORY.md 9216 of 8192 bytes OVER BUDGET
file working/OPEN_LOOPS.md 512 of 4096 bytes
file semantic/facts.md 1234 of 16384 bytes
file semantic/preferences.md 16384 of 16384 bytes
file semantic/projects.md 4321 of 16384 bytes
file semantic/topics/claims.md 40000 of 32768 bytes OVER CAP
ledger entries 2
surface target semantic/topics/claims.md 40000 bytes over cap 32768
surface target working/WORKING_MEMORY.md 9217 bytes over cap 8192
surface repairable 2 targets 49217 bytes
`

// wantBareHeadReport is the golden report for a root that only has a head: a
// never-advanced checkpoint is lag with no age, a job that never ran reports
// never, a memory root with no files is not an error, and a surface with no
// repairable target says so in one line.
const wantBareHeadReport = `head seq 12 at 2026-10-13T09:10:33Z
checkpoint consolidation seq 0 lag 12 age never at never
checkpoint semantic-extraction seq 0 lag 12 age never at never
job working_memory.consolidate failures 0 last-run never last-success never last-failure never
job semantic_memory.extract failures 0 last-run never last-success never last-failure never
job verification_review failures 0 last-run never last-success never last-failure never
file working/WORKING_MEMORY.md 0 of 8192 bytes
file working/OPEN_LOOPS.md 0 of 4096 bytes
ledger entries 0
surface clean
`

// fixtureFile is one file written into a fixture memory root.
type fixtureFile struct {
	relative string
	content  string
}

// statusFixtureStore builds a store over a fixture root with the given files.
func statusFixtureStore(t *testing.T, files []fixtureFile) (*Store, string) {
	t.Helper()
	root := t.TempDir()
	for _, file := range files {
		path := filepath.Join(root, filepath.FromSlash(file.relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(file.content), 0o644); err != nil {
			t.Fatalf("write %s: %v", file.relative, err)
		}
	}
	return NewStore(memoryrepo.New(root, nil), "Q15"), root
}

// fullStatusFixtureFiles is a memory root mid-decline: head has advanced past
// a working-memory checkpoint that keeps up, a semantic-extraction checkpoint
// that froze two days ago, a job failing twice in a row, a working-memory file
// past its budget, one semantic file exactly at its cap, one past it, and a
// ledger with two entries under its seeded header.
func fullStatusFixtureFiles() []fixtureFile {
	workingPrefix := "# Working Memory\n\n## Active Tasks\n\n- fill: "
	// The file on disk carries a trailing newline the loaded content does not:
	// status measures working memory the way the overbudget rule does, as the
	// loaded content length, and the golden report pins the difference.
	working := workingPrefix + strings.Repeat("x", 9216-len(workingPrefix)) + "\n"
	ledger, err := readEmbeddedSeed(ledgerSeedPath)
	if err != nil {
		panic("read embedded ledger seed: " + err.Error())
	}
	ledger += "\n2026-10-12T02:11Z src=t4101..t4110 run=semantic_memory.extract dropped \"old item\" (stale)\n"
	ledger += "2026-10-13T05:10Z src=t4170..t4183 run=semantic_memory.extract dropped \"another item\" (stale)"

	return []fixtureFile{
		{
			relative: "history/state/head.json",
			content:  `{"last_seq":4183,"updated_at":"2026-10-13T09:10:33Z"}`,
		},
		{
			relative: "history/state/consolidation_checkpoint.json",
			content: `{"last_consolidated_turn_id":"turn-00000000000000004180",` +
				`"last_consolidated_seq":4180,"last_consolidated_at":"2026-10-13T09:08:00Z",` +
				`"updated_at":"2026-10-13T09:08:00Z"}`,
		},
		{
			relative: "cognition/state/semantic_extraction_checkpoint.json",
			content: `{"last_extracted_turn_id":"turn-00000000000000004100",` +
				`"last_extracted_seq":4100,"last_extracted_at":"2026-10-12T02:00:00Z",` +
				`"updated_at":"2026-10-12T02:00:00Z"}`,
		},
		{
			relative: "cognition/triggers/jobs/working_memory_consolidate.json",
			content: `{"last_run_at":"2026-10-12T09:04:00Z","last_success_at":` +
				`"2026-10-12T09:04:00Z","last_success_input_seq":4180}`,
		},
		{
			relative: "cognition/triggers/jobs/semantic_memory_extract.json",
			content: `{"last_run_at":"2026-10-12T02:11:33Z","last_success_at":` +
				`"2026-10-11T14:12:00Z","last_failure_at":"2026-10-12T02:11:33Z",` +
				`"consecutive_failures":2}`,
		},
		{relative: "working/WORKING_MEMORY.md", content: working},
		{
			relative: "working/OPEN_LOOPS.md",
			content:  "# Open Loops\n\n- " + strings.Repeat("o", 512-16),
		},
		{
			relative: "semantic/facts.md",
			content:  "# Semantic Facts\n" + strings.Repeat("x", 1234-17),
		},
		{
			relative: "semantic/preferences.md",
			content:  "# Semantic Preferences\n" + strings.Repeat("x", 16384-23),
		},
		{
			relative: "semantic/projects.md",
			content:  "# Semantic Projects\n" + strings.Repeat("x", 4321-20),
		},
		{
			relative: "semantic/topics/claims.md",
			content:  "# Topic: claims\n" + strings.Repeat("x", 40000-16),
		},
		{relative: "working/LEDGER.md", content: ledger},
	}
}

// TestRenderStatusGolden is the acceptance fixture: a root whose semantic
// extraction stalled while the transcript kept advancing must never render as
// healthy, and the report says so in one command.
func TestRenderStatusGolden(t *testing.T) {
	t.Parallel()

	store, root := statusFixtureStore(t, fullStatusFixtureFiles())

	// The report measures working memory as loaded content, so the fixture
	// pins why 9217 bytes on disk report as 9216 of 8192.
	info, err := os.Stat(filepath.Join(root, "working", "WORKING_MEMORY.md"))
	if err != nil {
		t.Fatalf("stat working memory file: %v", err)
	}
	if info.Size() != 9217 {
		t.Fatalf("working memory file on disk = %d, want 9217", info.Size())
	}

	status, err := CollectStatus(context.Background(), store, statusFixtureLimits(), statusNow)
	if err != nil {
		t.Fatalf("CollectStatus() error = %v", err)
	}

	var report bytes.Buffer
	if err := RenderStatus(&report, status); err != nil {
		t.Fatalf("RenderStatus() error = %v", err)
	}
	if report.String() != wantStatusReport {
		t.Fatalf("status report =\n%s\nwant =\n%s", report.String(), wantStatusReport)
	}
}

// TestStatusOnABareHeadRoot pins the never-happened shape: lag without a
// checkpoint timestamp says so instead of printing an age since the zero time.
func TestStatusOnABareHeadRoot(t *testing.T) {
	t.Parallel()

	store, _ := statusFixtureStore(t, []fixtureFile{
		{
			relative: "history/state/head.json",
			content:  `{"last_seq":12,"updated_at":"2026-10-13T09:10:33Z"}`,
		},
	})

	status, err := CollectStatus(context.Background(), store, statusFixtureLimits(), statusNow)
	if err != nil {
		t.Fatalf("CollectStatus() error = %v", err)
	}

	var report bytes.Buffer
	if err := RenderStatus(&report, status); err != nil {
		t.Fatalf("RenderStatus() error = %v", err)
	}
	if report.String() != wantBareHeadReport {
		t.Fatalf("status report =\n%s\nwant =\n%s", report.String(), wantBareHeadReport)
	}
}

// memoryRootState is one snapshot of a fixture root: every file's path, size
// and modification time. Reading must change none of the three.
type memoryRootState []string

func snapshotMemoryRoot(t *testing.T, root string) memoryRootState {
	t.Helper()
	var state memoryRootState
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		state = append(state, fmt.Sprintf(
			"%s %d %d",
			filepath.ToSlash(relative),
			info.Size(),
			info.ModTime().UnixNano(),
		))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return state
}

// TestCollectStatusNeverWritesToTheRoot is the read-only constraint as a test:
// collecting twice changes no file, no size and no modification time, and the
// store is never given a reason to commit.
func TestCollectStatusNeverWritesToTheRoot(t *testing.T) {
	t.Parallel()

	store, root := statusFixtureStore(t, fullStatusFixtureFiles())
	before := snapshotMemoryRoot(t, root)

	for range 2 {
		status, err := CollectStatus(context.Background(), store, statusFixtureLimits(), statusNow)
		if err != nil {
			t.Fatalf("CollectStatus() error = %v", err)
		}
		if err := RenderStatus(io.Discard, status); err != nil {
			t.Fatalf("RenderStatus() error = %v", err)
		}
	}
	if after := snapshotMemoryRoot(t, root); !equalStates(before, after) {
		t.Fatalf("the root changed:\nbefore:\n%s\nafter:\n%s",
			strings.Join(before, "\n"), strings.Join(after, "\n"))
	}
}

func equalStates(a, b memoryRootState) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestCollectStatusRejectsNonPositiveLimits guards the one way a caller can
// build an uninformative report: a limit of zero would mark every file over.
func TestCollectStatusRejectsNonPositiveLimits(t *testing.T) {
	t.Parallel()

	store, _ := statusFixtureStore(t, fullStatusFixtureFiles())

	_, err := CollectStatus(context.Background(), store, StatusLimits{}, statusNow)
	if err == nil {
		t.Fatal("CollectStatus() error = nil, want a rejection")
	}
	if !strings.Contains(err.Error(), "limits must be positive") {
		t.Fatalf("error = %q, want it to name the bad limits", err.Error())
	}
}

func TestCollectStatusCountsArchivedRecords(t *testing.T) {
	files := fullStatusFixtureFiles()
	files = append(
		files,
		fixtureFile{
			relative: "working/ledger/2026-Q3.md",
			content:  "# Conversion Ledger\n\n2026-09-01T04:00Z run=semantic_memory.extract src=t1 dropped \"archived item\" (stale)\n",
		},
	)
	store, _ := statusFixtureStore(t, files)
	status, err := CollectStatus(context.Background(), store, statusFixtureLimits(), statusNow)
	if err != nil {
		t.Fatal(err)
	}
	if status.LedgerEntries != 3 {
		t.Fatalf("ledger entries = %d, want 3", status.LedgerEntries)
	}
}

// TestStatusRendersUnNamedSurfaceTarget pins the adopt disposition in the
// report: a file under a content root that no schema entry owns renders with
// no cap and the un-named mark, ordered after any named over-cap target.
func TestStatusRendersUnNamedSurfaceTarget(t *testing.T) {
	t.Parallel()

	files := append(
		fullStatusFixtureFiles(),
		fixtureFile{relative: "semantic/state/head.json", content: `{"last_seq":1}`},
	)
	store, _ := statusFixtureStore(t, files)

	status, err := CollectStatus(context.Background(), store, statusFixtureLimits(), statusNow)
	if err != nil {
		t.Fatalf("CollectStatus() error = %v", err)
	}

	var report bytes.Buffer
	if err := RenderStatus(&report, status); err != nil {
		t.Fatalf("RenderStatus() error = %v", err)
	}
	for _, want := range []string{
		"surface target semantic/state/head.json 14 bytes un-named\n",
		"surface repairable 3 targets ",
	} {
		if !strings.Contains(report.String(), want) {
			t.Fatalf("status report missing %q:\n%s", want, report.String())
		}
	}
	// The un-named file sorts after both named over-cap targets.
	unnamed := strings.Index(report.String(), "surface target semantic/state/head.json")
	claims := strings.Index(report.String(), "surface target semantic/topics/claims.md")
	if unnamed < claims {
		t.Fatalf("un-named target rendered before the migration group:\n%s", report.String())
	}
}
