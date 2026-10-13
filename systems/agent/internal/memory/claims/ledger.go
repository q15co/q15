package claims

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/q15co/q15/systems/agent/internal/atomicfile"
)

// ledgerTimestampLayout is the UTC timestamp prefix every ledger line carries.
const ledgerTimestampLayout = "2006-01-02T15:04Z"

// LedgerOutcome is the fate of one working-memory item that left the control
// surface during a conversion pass (DESIGN.md §3.2 and §3.3).
type LedgerOutcome string

const (
	// OutcomeConverted marks an item rewritten as a durable claim.
	OutcomeConverted LedgerOutcome = "converted"
	// OutcomeMoved marks an item moved to the prospective open-loops file.
	OutcomeMoved LedgerOutcome = "moved"
	// OutcomeDropped marks an item with no durable content left.
	OutcomeDropped LedgerOutcome = "dropped"
	// OutcomeSuperseded marks an old claim replaced by a newer one.
	OutcomeSuperseded LedgerOutcome = "superseded"
	// OutcomeRetracted marks a claim removed because its evidence was false.
	OutcomeRetracted LedgerOutcome = "retracted"
	// OutcomeResolved marks a conflict settled in favour of one claim.
	OutcomeResolved LedgerOutcome = "resolved"
	// OutcomeClosed marks an open loop that finished, with an outcome seq.
	OutcomeClosed LedgerOutcome = "closed"
	// OutcomeMigrated marks a legacy artifact converted during migration.
	OutcomeMigrated LedgerOutcome = "migrated"
)

// LedgerEntry is one append-only line of the conversion ledger.
type LedgerEntry struct {
	// Timestamp is the moment of the exit. A zero value means "now", resolved
	// at append time so a caller never has to guess the clock.
	Timestamp time.Time
	// Run names the cognition job or process that recorded the exit, for
	// example "semantic_memory.extract".
	Run string
	// SourceSeq is the transcript range the exit covers ("t4391..t4414"), or
	// empty when the originating seq is unrecoverable.
	SourceSeq string
	// Outcome is what became of the item.
	Outcome LedgerOutcome
	// Description names the item as it appeared in working memory.
	Description string
	// TargetID is the claim or loop ID the item became, or the losing claim
	// ID for resolved/retracted/superseded outcomes.
	TargetID string
	// TargetPath is the repository-relative file the target lives in.
	TargetPath string
	// Note carries any remaining detail, such as the reason for a drop.
	Note string
}

var (
	ledgerAppendMu sync.Mutex
	ledgerNow      = func() time.Time { return time.Now().UTC() }
)

// FormatLedgerEntry renders one ledger line, single-line and newline
// terminated, so the ledger stays greppable and several writers can append
// without interleaving within a line.
func FormatLedgerEntry(entry LedgerEntry) string {
	timestamp := entry.Timestamp.UTC()
	if entry.Timestamp.IsZero() {
		timestamp = ledgerNow()
	}

	var builder strings.Builder
	builder.WriteString(timestamp.Format(ledgerTimestampLayout))

	if run := strings.TrimSpace(entry.Run); run != "" {
		builder.WriteString(" run=")
		builder.WriteString(run)
	}
	if src := strings.TrimSpace(entry.SourceSeq); src != "" {
		builder.WriteString(" src=")
		builder.WriteString(src)
	}
	builder.WriteString(" ")
	builder.WriteString(string(entry.Outcome))

	description := normalizeLedgerField(entry.Description)
	if description != "" {
		builder.WriteString(" \"")
		builder.WriteString(description)
		builder.WriteString("\"")
	}
	if target := strings.TrimSpace(entry.TargetID); target != "" {
		if strings.Contains(target, " ") {
			builder.WriteString(" → \"")
			builder.WriteString(target)
			builder.WriteString("\"")
		} else {
			builder.WriteString(" → ")
			builder.WriteString(target)
		}
		if targetPath := strings.TrimSpace(entry.TargetPath); targetPath != "" {
			builder.WriteString(" (")
			builder.WriteString(targetPath)
			builder.WriteString(")")
		}
	}
	note := normalizeLedgerField(entry.Note)
	if note != "" {
		note = strings.Trim(note, "()")
		note = strings.TrimSpace(note)
	}
	if note != "" {
		builder.WriteString(" (")
		builder.WriteString(note)
		builder.WriteString(")")
	}
	builder.WriteString("\n")
	return builder.String()
}

// normalizeLedgerField strips the newline and carriage return runs that would
// break the one-line-per-exit invariant, and collapses them to spaces.
func normalizeLedgerField(raw string) string {
	raw = strings.ReplaceAll(raw, "\r\n", " ")
	raw = strings.ReplaceAll(raw, "\n", " ")
	raw = strings.ReplaceAll(raw, "\r", " ")
	return strings.TrimSpace(strings.Join(strings.Fields(raw), " "))
}

// AppendLedger appends entries to the append-only conversion ledger at path,
// creating the file and its parent directories when missing.
//
// This is the one memory write that is deliberately *not* an atomic rename:
// the ledger is append-only evidence, so entries are added with a single
// O_APPEND write of the whole batch. Two properties carry the concurrency
// safety:
//
//   - The whole batch is one buffer, so a single write(2) emits every entry
//     with no torn or interleaved lines.
//   - The open and the write happen under a package mutex, because O_APPEND
//     only makes the kernel's write position atomic once the file *is* open.
//     Concurrent OpenFile(O_APPEND) followed by WriteFile races: a process
//     that truncates or recreates the path between them resets the shared
//     append offset, and entries land on top of one another. Serialising the
//     file mutation is what removes that window.
//
// The mutex only orders writes within this process. Cross-process safety is
// still provided by O_APPEND, which keeps each write position-atomic; the
// in-process serialisation is what stops two goroutines from racing the open.
func AppendLedger(path string, entries []LedgerEntry) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("ledger path is required")
	}
	if len(entries) == 0 {
		return nil
	}

	dir := filepath.Dir(path)
	if err := atomicfile.EnsureDirectory(dir, 0o755); err != nil {
		return fmt.Errorf("create ledger dir %q: %w", dir, err)
	}

	var batch strings.Builder
	for _, entry := range entries {
		batch.WriteString(FormatLedgerEntry(entry))
	}
	payload := []byte(batch.String())

	ledgerAppendMu.Lock()
	defer ledgerAppendMu.Unlock()

	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open ledger %q: %w", path, err)
	}
	written, err := file.Write(payload)
	if err != nil {
		_ = file.Close()
		return fmt.Errorf("append ledger %q: %w", path, err)
	}
	if written != len(payload) {
		_ = file.Close()
		return fmt.Errorf(
			"append ledger %q: short write (%d of %d bytes)", path, written, len(payload),
		)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync ledger %q: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close ledger %q: %w", path, err)
	}
	return nil
}

// LedgerEntryCount returns the number of ledger lines in the file at path.
// A missing file counts as zero.
func LedgerEntryCount(path string) (int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read ledger %q: %w", path, err)
	}
	count := 0
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		count++
	}
	return count, nil
}
