package cognition

import (
	"context"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/memory/claims"
)

func TestRepairAdoptionMatchesOneCompleteRecord(t *testing.T) {
	t.Parallel()

	const journal = "adoption-one"
	const reason = "runtime adopted the file"
	wanted := claims.LedgerEntry{
		Run: semanticMemoryExtractionJobType, SourceSeq: "mem:" + repairNotesPath + ":0",
		Outcome: claims.OutcomeDropped, Description: repairNotesPath,
		Note: reason + "; repair_id=" + journal,
	}
	for _, location := range []string{"split records", "active", "archive", "prior adoption"} {
		t.Run(location, func(t *testing.T) {
			loader := &semanticMemoryJobLoader{files: repairRawFiles(repairPreFiles())}
			base := string(loader.files[workingLedgerRelativePath])
			expected := 1
			switch location {
			case "split records":
				first, second := wanted, wanted
				first.Note = "unrelated reason"
				second.SourceSeq = "mem:semantic/another.md:0"
				loader.files[workingLedgerRelativePath] = []byte(
					base + claims.FormatLedgerEntry(first) + claims.FormatLedgerEntry(second),
				)
			case "active":
				loader.files[workingLedgerRelativePath] = []byte(
					base + claims.FormatLedgerEntry(wanted),
				)
			case "archive":
				loader.ledgerHistory = claims.FormatLedgerEntry(wanted)
				expected = 0
			case "prior adoption":
				prior := wanted
				prior.Note = reason + "; repair_id=older-adoption"
				loader.files[workingLedgerRelativePath] = []byte(
					base + claims.FormatLedgerEntry(prior),
				)
			}
			if err := appendRepairAdoptionRecord(context.Background(), loader, repairNotesPath, reason, journal); err != nil {
				t.Fatal(err)
			}
			entries, err := claims.ParseLedger(loader.files[workingLedgerRelativePath])
			if err != nil {
				t.Fatal(err)
			}
			actual := 0
			for _, entry := range entries {
				if entry.SourceSeq == wanted.SourceSeq && entry.Note == wanted.Note {
					actual++
				}
			}
			if actual != expected {
				t.Fatalf(
					"matching active records = %d, want %d; ledger=%s",
					actual,
					expected,
					loader.files[workingLedgerRelativePath],
				)
			}
		})
	}
}

func TestRepairAdoptionRejectsMalformedLedger(t *testing.T) {
	t.Parallel()

	loader := &semanticMemoryJobLoader{files: map[string][]byte{
		workingLedgerRelativePath: []byte("this is not an exit record\n"),
	}}
	err := appendRepairAdoptionRecord(
		context.Background(),
		loader,
		repairNotesPath,
		repairAdoptionNote,
		"repair-one",
	)
	if err == nil || !strings.Contains(err.Error(), "parse adoption ledger") {
		t.Fatalf("error=%v, want malformed history to fail before deletion", err)
	}
}
