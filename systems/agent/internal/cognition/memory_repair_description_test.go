package cognition

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

func TestRepairDescriptionComparisonReportsNormalizedByteDifference(t *testing.T) {
	const head = "# Semantic Facts\n\n## Confirmed Facts\n\n"
	const richText = "> **Synthetic emphasis** — inspect `sample.go`"
	contextText := strings.Repeat("synthetic context ", 20)
	line := richText + "  " + contextText + "end."
	expected := richText + " " + contextText + "end."
	tail := strings.Repeat("Uncovered synthetic source. ", 900) + "\n"
	before := head + line + "\n" + tail
	for _, test := range []struct {
		name, description, ledgerByte, sourceByte string
		offset                                    int
	}{
		{name: "provided literal"},
		{
			name:        "invisible insertion after summary cutoff",
			description: strings.TrimSuffix(expected, "end.") + "\u200bend.",
			offset:      len(expected) - len("end."), ledgerByte: "0xe2", sourceByte: "0x65",
		},
		{
			name: "missing suffix", description: strings.TrimSuffix(expected, " end."),
			offset: len(expected) - len(" end."), ledgerByte: "end", sourceByte: "0x20",
		},
		{
			name: "extra suffix", description: expected + "x",
			offset: len(expected), ledgerByte: "0x78", sourceByte: "end",
		},
		{
			name: "different multibyte punctuation", description: strings.Replace(expected, "—", "–", 1),
			offset: strings.Index(expected, "—") + 2, ledgerByte: "0x93", sourceByte: "0x94",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			files := repairPreFiles()
			delete(files, repairNotesPath)
			files[semanticFactsRelativePath] = before
			loader := &repairProbeLoader{
				semanticMemoryJobLoader: &semanticMemoryJobLoader{files: repairRawFiles(files)},
			}
			for path, content := range files {
				loader.memoryFiles = append(
					loader.memoryFiles,
					MemoryFile{Path: path, Bytes: int64(len(content))},
				)
			}
			job := NewSemanticMemoryExtractionRegistration(0, 0).NewJob()
			spec, err := job.Build(context.Background(), loader)
			if err != nil {
				t.Fatal(err)
			}
			if !spec.repair || spec.repairTarget.Path != semanticFactsRelativePath {
				t.Fatalf(
					"repair=%v target=%s, want facts repair",
					spec.repair,
					spec.repairTarget.Path,
				)
			}
			section, ok := findPromptSection(spec.PromptSections, "repair_chunk")
			if !ok {
				t.Fatal("missing repair chunk")
			}
			_, literal, ok := strings.Cut(section.Body, " ledger_description=")
			if !ok {
				t.Fatal("missing ledger description")
			}
			quoted, err := strconv.QuotedPrefix(literal)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := strconv.Unquote(quoted)
			if err != nil || decoded != expected {
				t.Fatalf("description=%q error=%v, want %q", decoded, err, expected)
			}
			rawLiteral, ok := strings.CutPrefix(literal[len(quoted):], " raw_line=")
			if !ok {
				t.Fatal("missing raw source with doubled whitespace")
			}
			rawLine, err := strconv.Unquote(rawLiteral)
			if err != nil || rawLine != line {
				t.Fatalf("raw line=%q error=%v, want %q", rawLine, err, line)
			}
			if test.description != "" {
				quoted = strconv.Quote(test.description)
			}
			loader.files[semanticFactsRelativePath] = []byte(head + tail)
			loader.files["working/LEDGER.md"] = []byte(repairLedgerPre + fmt.Sprintf(
				"2026-10-17T09:00Z run=semantic_memory.extract src=mem:%s:%d dropped %s (synthetic example)\n",
				semanticFactsRelativePath,
				len(head),
				quoted,
			))
			result, err := applyWrittenRepairForTest(context.Background(), loader, JobOutput{
				Spec: spec, Messages: repairRunMessages(semanticFactsRuntimePath, workingLedgerRuntimePath),
			})
			if test.description == "" {
				if err != nil {
					t.Fatal(err)
				}
				if result.Metadata["chunk_lines_exited"] != "1" ||
					string(loader.files[semanticFactsRelativePath]) != head+tail {
					t.Fatalf("repair did not drain exactly the cited source: %#v", result)
				}
				return
			}
			want := fmt.Sprintf(
				"does not copy the drained chunk line at normalized byte offset %d (ledger=%s source=%s; ledger_bytes=%d source_bytes=%d)",
				test.offset,
				test.ledgerByte,
				test.sourceByte,
				len(test.description),
				len(expected),
			)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("ApplyResult error=%v, want %q", err, want)
			}
			if strings.Contains(err.Error(), contextText) {
				t.Fatal("diagnostic dumped the source instead of describing its byte difference")
			}
			if string(loader.files[semanticFactsRelativePath]) != before ||
				string(loader.files["working/LEDGER.md"]) != repairLedgerPre {
				t.Fatal("rejected repair did not restore source and ledger")
			}
		})
	}
}
