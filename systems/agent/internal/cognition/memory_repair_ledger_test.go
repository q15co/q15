package cognition

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/memory/claims"
)

func TestRepairChunkLedgerDescriptionsRoundTrip(t *testing.T) {
	const first = "Check \"sample\" (see `guide.md`) under C:\\notes."
	const second = "Retain  the\tcomplete source."
	head := []byte("# Notes\n\n" + first + "\n" + second + "\n")
	chunk, err := cutRepairChunk(head)
	if err != nil {
		t.Fatal(err)
	}
	section := repairChunkSection(
		MemorySurfaceTarget{Path: semanticFactsRelativePath},
		chunk,
		repairChunkWindowBytes(head),
	)
	rows := strings.Split(section.Body, "\n")
	if len(rows) != 2 {
		t.Fatalf("repair chunk has %d rows, want two content lines", len(rows))
	}
	var appended strings.Builder
	for i, row := range rows {
		frame, descriptionAndText, ok := strings.Cut(row, " ledger_description=")
		if !ok {
			t.Fatalf("repair row %d provides no safely quoted ledger description: %s", i, row)
		}
		wantFrame := fmt.Sprintf("offset=%d sha256=%x", chunk[i].offset, chunk[i].sum)
		if frame != wantFrame {
			t.Fatalf("repair row frame = %q, want %q", frame, wantFrame)
		}
		quoted, err := strconv.QuotedPrefix(descriptionAndText)
		if err != nil {
			t.Fatalf("repair row %d has an invalid description literal: %v", i, err)
		}
		source, err := strconv.Unquote(quoted)
		if err != nil {
			t.Fatal(err)
		}
		if remainder := descriptionAndText[len(quoted):]; remainder != "" {
			raw, ok := strings.CutPrefix(remainder, " raw_line=")
			if !ok {
				t.Fatalf("repair row has unexpected source text: %q", remainder)
			}
			source, err = strconv.Unquote(raw)
			if err != nil {
				t.Fatalf("repair row has invalid raw source literal: %v", err)
			}
		}
		if source != chunk[i].text {
			t.Fatalf("repair row source text = %q, want %q", source, chunk[i].text)
		}
		fmt.Fprintf(
			&appended,
			"2026-10-17T09:00Z run=semantic_memory.extract src=mem:%s:%d dropped %s (synthetic example)\n",
			semanticFactsRelativePath,
			chunk[i].offset,
			quoted,
		)
	}
	ledgerPath := filepath.Join(t.TempDir(), "LEDGER.md")
	if err := os.WriteFile(ledgerPath, []byte("# Conversion Ledger\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(ledgerPath, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(appended.String()); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := claims.ParseLedger(raw)
	if err != nil {
		t.Fatalf("parse appended repair ledger: %v", err)
	}
	wantDescriptions := []string{first, "Retain the complete source."}
	if len(entries) != len(wantDescriptions) {
		t.Fatalf("parsed %d ledger records, want %d", len(entries), len(wantDescriptions))
	}
	for i, entry := range entries {
		if entry.Description != wantDescriptions[i] {
			t.Fatalf("description %d = %q, want %q", i, entry.Description, wantDescriptions[i])
		}
		wantSource := fmt.Sprintf("mem:%s:%d", semanticFactsRelativePath, chunk[i].offset)
		if entry.SourceSeq != wantSource {
			t.Fatalf("source %d = %q, want %q", i, entry.SourceSeq, wantSource)
		}
	}
}
