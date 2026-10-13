package claims

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestAppendLedgerIsConcurrencySafeUnderParallelWriters is the T1 ledger
// property: many writers appending at once must produce exactly one whole
// line each, with no interleaving, no lost entries and no torn lines.
func TestAppendLedgerIsConcurrencySafeUnderParallelWriters(t *testing.T) {
	const (
		writers          = 16
		entriesPerWriter = 32
	)

	path := filepath.Join(t.TempDir(), "working", "LEDGER.md")

	var wait sync.WaitGroup
	for writer := 0; writer < writers; writer++ {
		wait.Add(1)
		go func(writer int) {
			defer wait.Done()
			for entry := 0; entry < entriesPerWriter; entry++ {
				err := AppendLedger(path, []LedgerEntry{{
					Run:         "semantic_memory.extract",
					SourceSeq:   "t1..t16",
					Outcome:     OutcomeConverted,
					Description: writerDescription(writer, entry),
					TargetID:    "c-2026-0001",
					TargetPath:  "semantic/topics/writer.md",
				}})
				if err != nil {
					t.Errorf("AppendLedger(writer=%d, entry=%d) error = %v", writer, entry, err)
					return
				}
			}
		}(writer)
	}
	wait.Wait()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(ledger) error = %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")

	wantLines := writers * entriesPerWriter
	if len(lines) != wantLines {
		t.Fatalf("ledger line count = %d, want %d", len(lines), wantLines)
	}

	seen := make(map[string]struct{}, wantLines)
	for _, line := range lines {
		if !strings.HasPrefix(line, "20") || !strings.Contains(line, " run=semantic_memory.extract ") {
			t.Fatalf("torn or malformed ledger line: %q", line)
		}
		if !strings.Contains(line, " → c-2026-0001 (semantic/topics/writer.md)") {
			t.Fatalf("ledger line lost its target: %q", line)
		}
		description := ledgerDescription(line)
		if description == "" {
			t.Fatalf("ledger line lost its description: %q", line)
		}
		if _, duplicate := seen[description]; duplicate {
			t.Fatalf("duplicate ledger description %q", description)
		}
		seen[description] = struct{}{}
	}

	for writer := 0; writer < writers; writer++ {
		for entry := 0; entry < entriesPerWriter; entry++ {
			if _, ok := seen[writerDescription(writer, entry)]; !ok {
				t.Fatalf("entry %q was lost", writerDescription(writer, entry))
			}
		}
	}

	count, err := LedgerEntryCount(path)
	if err != nil {
		t.Fatalf("LedgerEntryCount() error = %v", err)
	}
	if count != wantLines {
		t.Fatalf("LedgerEntryCount() = %d, want %d", count, wantLines)
	}
}

func writerDescription(writer, entry int) string {
	return "item-" + itoa(writer) + "-" + itoa(entry)
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var digits []byte
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

// ledgerDescription reads the item description out of a ledger line. The
// format is one line per exit, so the description is framed by the first quote
// after the outcome keyword and the quote that closes it, whichever way the
// description happens to contain punctuation.
func ledgerDescription(line string) string {
	_, after, ok := strings.Cut(line, ` converted "`)
	if !ok {
		return ""
	}
	description, _, ok := strings.Cut(after, `" `)
	if !ok {
		return ""
	}
	return description
}

func TestAppendLedgerCreatesFileAndParents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deep", "working", "ledger", "LEDGER.md")

	count, err := LedgerEntryCount(path)
	if err != nil {
		t.Fatalf("LedgerEntryCount(missing) error = %v", err)
	}
	if count != 0 {
		t.Fatalf("LedgerEntryCount(missing) = %d, want 0", count)
	}

	err = AppendLedger(path, []LedgerEntry{
		{Outcome: OutcomeMoved, Description: "follow up on thing", TargetID: "l-2026-0031"},
		{Outcome: OutcomeDropped, Description: "transient staging noise", Note: "(no durable content)"},
	})
	if err != nil {
		t.Fatalf("AppendLedger() error = %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("ledger lines = %d, want 2", len(lines))
	}
	if !strings.Contains(lines[0], `moved "follow up on thing" → l-2026-0031`) {
		t.Fatalf("first line = %q", lines[0])
	}
	if !strings.Contains(lines[1], `dropped "transient staging noise" (no durable content)`) {
		t.Fatalf("second line = %q", lines[1])
	}

	// Appending again must not rewrite what is already there.
	if err := AppendLedger(path, []LedgerEntry{{Outcome: OutcomeClosed, TargetID: "l-2026-0031"}}); err != nil {
		t.Fatalf("second AppendLedger() error = %v", err)
	}
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !strings.Contains(string(raw), lines[0]) || !strings.Contains(string(raw), lines[1]) {
		t.Fatalf("append rewrote existing lines:\n%s", raw)
	}
}

func TestAppendLedgerEmptyBatchIsANoop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "LEDGER.md")
	if err := AppendLedger(path, nil); err != nil {
		t.Fatalf("AppendLedger(nil) error = %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("AppendLedger(nil) created a file, stat err = %v", err)
	}
}

func TestFormatLedgerEntryKeepsOneLinePerExit(t *testing.T) {
	got := FormatLedgerEntry(LedgerEntry{
		Outcome:     OutcomeConverted,
		Description: "multi\nline\ritem",
		Note:        "note\nwith break",
	})
	if strings.Count(got, "\n") != 1 {
		t.Fatalf("FormatLedgerEntry() = %q, want exactly one newline", got)
	}
	if !strings.Contains(got, `converted "multi line item"`) {
		t.Fatalf("FormatLedgerEntry() = %q, want a collapsed description", got)
	}
	if !strings.Contains(got, "note with break") {
		t.Fatalf("FormatLedgerEntry() = %q, want a collapsed note", got)
	}
}
