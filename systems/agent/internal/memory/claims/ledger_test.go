package claims

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
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
		if !strings.HasPrefix(line, "20") ||
			!strings.Contains(line, " run=semantic_memory.extract ") {
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
		{Run: "semantic_memory.extract", SourceSeq: "t1", Outcome: OutcomeMoved,
			Description: "follow up on thing", TargetID: "l-2026-0031", TargetPath: "working/OPEN_LOOPS.md"},
		{
			Run:         "semantic_memory.extract",
			SourceSeq:   "t1",
			Outcome:     OutcomeDropped,
			Description: "transient staging noise",
			Note:        "(no durable content)",
		},
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
	if err := AppendLedger(path, []LedgerEntry{{Run: "working_memory.refresh", SourceSeq: "t2",
		Outcome: OutcomeClosed, Description: "follow up on thing", TargetID: "l-2026-0031", Note: "completed in t2"}}); err != nil {
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

func TestParseLedgerEntryReadsIndependentExamples(t *testing.T) {
	timestamp := time.Date(2026, time.October, 11, 9, 30, 0, 0, time.UTC)
	tests := []struct {
		name string
		line string
		want LedgerEntry
	}{
		{
			name: "converted with escaped quotes and backslash",
			line: `2026-10-11T09:30Z run=semantic_memory.extract src=t1..t9 converted "Use \"q15\" in C:\\work" → c-2026-0001 (semantic/topics/q15.md) (direct observation)`,
			want: LedgerEntry{
				Timestamp:   timestamp,
				Run:         "semantic_memory.extract",
				SourceSeq:   "t1..t9",
				Outcome:     OutcomeConverted,
				Description: `Use "q15" in C:\work`,
				TargetID:    "c-2026-0001",
				TargetPath:  "semantic/topics/q15.md",
				Note:        "direct observation",
			},
		},
		{
			name: "moved accepts source first",
			line: `2026-10-11T09:30Z src=t9 run=working_memory.refresh moved "Review the result" → l-2026-0001 (working/OPEN_LOOPS.md)`,
			want: LedgerEntry{Timestamp: timestamp, Run: "working_memory.refresh", SourceSeq: "t9",
				Outcome: OutcomeMoved, Description: "Review the result", TargetID: "l-2026-0001",
				TargetPath: "working/OPEN_LOOPS.md"},
		},
		{
			name: "dropped with nested parentheses in reason",
			line: `2026-10-11T09:30Z run=semantic_memory.extract src=t9 dropped "Transient status" (already complete (t8))`,
			want: LedgerEntry{Timestamp: timestamp, Run: "semantic_memory.extract", SourceSeq: "t9",
				Outcome: OutcomeDropped, Description: "Transient status", Note: "already complete (t8)"},
		},
		{
			name: "closed reason is not a target path",
			line: `2026-10-11T09:30Z run=working_memory.refresh src=t9 closed "Review the result" → l-2026-0001 (completed in t9)`,
			want: LedgerEntry{Timestamp: timestamp, Run: "working_memory.refresh", SourceSeq: "t9",
				Outcome: OutcomeClosed, Description: "Review the result", TargetID: "l-2026-0001", Note: "completed in t9"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParseLedgerEntry(test.line)
			if err != nil {
				t.Fatalf("ParseLedgerEntry() error = %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("ParseLedgerEntry() = %#v, want %#v", got, test.want)
			}
			if got := strings.TrimSuffix(FormatLedgerEntry(test.want), "\n"); test.name != "moved accepts source first" &&
				got != test.line {
				t.Fatalf("FormatLedgerEntry() = %q, want %q", got, test.line)
			}
		})
	}
}

func TestParseLedgerEntryRejectsMalformedRecords(t *testing.T) {
	const prefix = "2026-10-11T09:30Z run=semantic_memory.extract src=t1 "
	for _, line := range []string{
		"arbitrary text pretending to record an exit",
		`2026-02-30T09:30Z run=semantic_memory.extract src=t1 dropped "item" (noise)`,
		`2026-10-11T09:30Z src=t1 dropped "item" (noise)`,
		`2026-10-11T09:30Z run=semantic_memory.extract dropped "item" (noise)`,
		`2026-10-11T09:30Z run=first run=second dropped "item" (noise)`,
		`2026-10-11T09:30Z run=semantic_memory.extract src=untraceable dropped "item" (noise)`,
		`2026-10-11T09:30Z run=semantic_memory.extract src=t9..t1 dropped "item" (noise)`,
		prefix + `migrated "item"`,
		prefix + `invented "item"`,
		prefix + `dropped unquoted item`,
		prefix + `dropped ""`,
		prefix + `dropped "item`,
		prefix + `dropped "item\nnext line"`,
		prefix + `dropped "item" arbitrary suffix`,
		prefix + `dropped "item" (unclosed reason`,
		prefix + `dropped "item" (one) (two)`,
		prefix + `converted "item" → (semantic/topics/file.md)`,
		prefix + "dropped \"item\"\n" + prefix + `dropped "another"`,
	} {
		t.Run(line, func(t *testing.T) {
			if got, err := ParseLedgerEntry(line); err == nil {
				t.Fatalf("ParseLedgerEntry(%q) = %#v, want error", line, got)
			}
		})
	}
}

func TestLedgerEntryCountCountsRecordsAndRejectsMalformedEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "LEDGER.md")
	const record = `2026-10-11T09:30Z run=semantic_memory.extract src=t1 dropped "item" (transient noise)`
	raw := "# Conversion ledger\n\nLegacy explanatory header prose.\n<!-- One line per exit. -->\n" + record + "\n"
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if count, err := LedgerEntryCount(path); err != nil || count != 1 {
		t.Fatalf("LedgerEntryCount() = %d, %v; want 1, nil", count, err)
	}
	if err := os.WriteFile(path, []byte(raw+"2026-10-11 malformed record\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if count, err := LedgerEntryCount(path); err == nil {
		t.Fatalf("LedgerEntryCount() = %d, nil; want malformed-record error", count)
	}
}

func TestAppendLedgerRejectsMalformedEntryWithoutChangingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "LEDGER.md")
	const before = "# Conversion ledger\n"
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	err := AppendLedger(path, []LedgerEntry{{Outcome: OutcomeDropped, Description: "untraceable"}})
	if err == nil {
		t.Fatal("AppendLedger() accepted an entry without run or source")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != before {
		t.Fatalf("AppendLedger() changed existing bytes: %q", after)
	}
}

func TestParseLedgerRequiresRecordsOutsideHeadingsAndComments(t *testing.T) {
	const record = `2026-10-11T09:30Z run=semantic_memory.extract src=t1 dropped "item" (transient noise)`
	data := []byte("# Conversion ledger\n\n<!-- One line per exit. -->\n" + record + "\n")
	entries, err := ParseLedger(data)
	if err != nil || len(entries) != 1 || entries[0].Description != "item" {
		t.Fatalf("ParseLedger() = %#v, %v; want one item record", entries, err)
	}
	if _, err := ParseLedger(append(data, []byte("unstructured exit evidence\n")...)); err == nil {
		t.Fatal("ParseLedger() accepted unstructured evidence")
	}
}
