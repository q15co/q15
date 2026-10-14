package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildArchiveResidue(t *testing.T) {
	t.Parallel()
	working := "# Working Memory\n\n- keep me\n- also live\n\n## Current Priorities\n\n- None\n"

	files := []ArchiveFile{
		{
			RelativePath: "working/archive/a.md",
			Content:      "- keep me\n- already gone\n\n- only in a\n",
		},
		{
			RelativePath: "working/archive/b.md",
			Content:      "- only in a\n- second copy\n",
		},
	}

	got := BuildArchiveResidue(files, working)
	want := "- already gone\n- only in a\n- second copy\n"
	if got != want {
		t.Fatalf("BuildArchiveResidue() =\n%q\nwant\n%q", got, want)
	}
}

func TestBuildArchiveResidueDropsBlanksAndCollapsesDuplicates(t *testing.T) {
	t.Parallel()
	files := []ArchiveFile{
		{
			RelativePath: "working/archive/a.md",
			Content:      "\n\n- shared\n\n  \n- a-only\n",
		},
		{
			RelativePath: "working/archive/b.md",
			Content:      "- shared\n- b-only\n",
		},
	}
	// "shared" appears in both files and collapses to one residue line; blank
	// lines are dropped; leading indentation is trimmed.
	got := BuildArchiveResidue(files, "- present in working memory\n")
	want := "- shared\n- a-only\n- b-only\n"
	if got != want {
		t.Fatalf("BuildArchiveResidue() =\n%q\nwant\n%q", got, want)
	}
}

func TestBuildArchiveResidueMatchesWorkingMemoryTrimmed(t *testing.T) {
	t.Parallel()
	// The working-memory membership test compares trimmed text, so a line that
	// differs only in surrounding whitespace is already present and excluded.
	got := BuildArchiveResidue(
		[]ArchiveFile{{RelativePath: "working/archive/a.md", Content: "  - padded line  \n"}},
		"- padded line\n",
	)
	if got != "" {
		t.Fatalf(
			"BuildArchiveResidue() = %q, want empty: the line is already in working memory",
			got,
		)
	}
}

func TestStageArchiveResidue(t *testing.T) {
	t.Parallel()
	store, root := newSemanticChunkStore(t)
	ctx := context.Background()

	archiveDir := filepath.Join(root, "working", "archive")
	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(archive) error = %v", err)
	}
	writeArchiveCopy := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(archiveDir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", name, err)
		}
	}
	writeArchiveCopy("a.md", "- keep me\n- folded away\n")
	writeArchiveCopy("b.md", "- folded away\n- folded too\n")

	summary, folded, err := store.StageArchiveResidue(ctx, "- keep me\n")
	if err != nil {
		t.Fatalf("StageArchiveResidue() error = %v", err)
	}
	if !folded {
		t.Fatalf("StageArchiveResidue() folded = false, want true")
	}
	if summary.FilesFolded != 2 || summary.ResidueLines != 2 {
		t.Fatalf("summary = %+v, want 2 files and 2 residue lines", summary)
	}
	residue, err := os.ReadFile(filepath.Join(root, "working", "archive-residue.md"))
	if err != nil {
		t.Fatalf("ReadFile(residue) error = %v", err)
	}
	if got, want := string(residue), "- folded away\n- folded too\n"; got != want {
		t.Fatalf("residue =\n%q\nwant\n%q", got, want)
	}
}

func TestStageArchiveResidueWithoutArchiveIsANoOp(t *testing.T) {
	t.Parallel()
	store, root := newSemanticChunkStore(t)

	summary, folded, err := store.StageArchiveResidue(context.Background(), "- keep me\n")
	if err != nil {
		t.Fatalf("StageArchiveResidue() error = %v", err)
	}
	if folded {
		t.Fatalf("StageArchiveResidue() folded = true, want false with no archive")
	}
	if summary.FilesFolded != 0 || summary.ResidueLines != 0 {
		t.Fatalf("summary = %+v, want zero with no archive", summary)
	}
	if _, err := os.Stat(filepath.Join(root, "working", "archive-residue.md")); !os.IsNotExist(
		err,
	) {
		t.Fatalf("residue file exists with no archive, want absent")
	}
}

func TestDeleteWorkingArchive(t *testing.T) {
	t.Parallel()
	store, root := newSemanticChunkStore(t)
	ctx := context.Background()

	archiveDir := filepath.Join(root, "working", "archive")
	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(archive) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(archiveDir, "a.md"), []byte("- x\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(a.md) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(archiveDir, "b.md"), []byte("- y\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(b.md) error = %v", err)
	}
	residuePath := filepath.Join(root, "working", "archive-residue.md")
	if err := os.WriteFile(residuePath, []byte("- z\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(residue) error = %v", err)
	}

	filesFolded, err := store.DeleteWorkingArchive(ctx)
	if err != nil {
		t.Fatalf("DeleteWorkingArchive() error = %v", err)
	}
	if filesFolded != 2 {
		t.Fatalf("files folded = %d, want 2", filesFolded)
	}
	if _, err := os.Stat(archiveDir); !os.IsNotExist(err) {
		t.Fatalf("archive dir still exists after deletion")
	}
	if _, err := os.Stat(residuePath); !os.IsNotExist(err) {
		t.Fatalf("residue file still exists after deletion")
	}

	// Deleting again is a no-op with zero files folded.
	filesFolded, err = store.DeleteWorkingArchive(ctx)
	if err != nil {
		t.Fatalf("DeleteWorkingArchive(twice) error = %v", err)
	}
	if filesFolded != 0 {
		t.Fatalf("files folded on second delete = %d, want 0", filesFolded)
	}
}

func TestBuildArchiveResiduePreservesOrderAndBlankLinesDropped(t *testing.T) {
	t.Parallel()
	// Order is the caller's responsibility, but the builder must not reorder
	// lines within a file or across files, and must not emit blank lines.
	files := []ArchiveFile{
		{RelativePath: "working/archive/a.md", Content: "- first\n\n- second\n"},
	}
	got := BuildArchiveResidue(files, "")
	if strings.Contains(got, "\n\n") {
		t.Fatalf("residue contains a blank line: %q", got)
	}
	if got != "- first\n- second\n" {
		t.Fatalf("BuildArchiveResidue() = %q, want ordered non-blank lines", got)
	}
}
