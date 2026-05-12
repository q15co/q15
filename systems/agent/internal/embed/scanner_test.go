package embed

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestScanMarkdownFileSplitsHeadingSections(t *testing.T) {
	settings := testSettings(t)
	if err := os.WriteFile(
		filepath.Join(settings.WorkspaceLocalDir, "note.md"),
		[]byte("---\ntitle: Test Note\n---\nIntro text.\n\n# First\nFirst body.\n\n# Second\nSecond body.\n"),
		0o644,
	); err != nil {
		t.Fatalf("write note: %v", err)
	}

	docs, err := ScanSource(context.Background(), settings, Source{
		ID:         "note",
		Collection: CollectionSemantic,
		SourceType: SourceTypeMarkdownFile,
		Path:       "/workspace/note.md",
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("ScanSource() error = %v", err)
	}
	if len(docs) != 3 {
		t.Fatalf("docs len = %d, want 3", len(docs))
	}
	if docs[0].Payload["section"] != "Preamble" {
		t.Fatalf("first section = %#v, want Preamble", docs[0].Payload["section"])
	}
	if docs[1].Payload["section"] != "First" {
		t.Fatalf("second section = %#v, want First", docs[1].Payload["section"])
	}
	if docs[1].Payload["title"] != "Test Note" {
		t.Fatalf("frontmatter title = %#v, want Test Note", docs[1].Payload["title"])
	}
}

func TestScanMarkdownTreeHonorsIncludeExcludeGlobs(t *testing.T) {
	settings := testSettings(t)
	for _, path := range []string{
		"notes/keep.md",
		"notes/nested/keep.md",
		"notes/private/drop.md",
	} {
		local := filepath.Join(settings.WorkspaceLocalDir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
			t.Fatalf("create parent: %v", err)
		}
		if err := os.WriteFile(local, []byte("# Note\nbody\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	docs, err := ScanSource(context.Background(), settings, Source{
		ID:           "notes",
		Collection:   CollectionZettelkasten,
		SourceType:   SourceTypeMarkdownTree,
		Path:         "/workspace/notes",
		IncludeGlobs: []string{"**/*.md"},
		ExcludeGlobs: []string{"private/**"},
		Enabled:      true,
	})
	if err != nil {
		t.Fatalf("ScanSource() error = %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("docs len = %d, want 2", len(docs))
	}
	for _, doc := range docs {
		if doc.Path == "/workspace/notes/private/drop.md" {
			t.Fatalf("excluded document was scanned: %#v", doc)
		}
	}
}

func TestScanChunkedMarkdownTreeLoadsFrontmatterAndSiblingMetadata(t *testing.T) {
	settings := testSettings(t)
	workDir := filepath.Join(settings.WorkspaceLocalDir, "library", "octavia-butler", "parable")
	chunksDir := filepath.Join(workDir, "chunks")
	if err := os.MkdirAll(chunksDir, 0o755); err != nil {
		t.Fatalf("create chunks: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(workDir, "meta.yml"),
		[]byte("title: Parable\nwork_type: book\nauthors:\n  - Octavia Butler\n"),
		0o644,
	); err != nil {
		t.Fatalf("write meta: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(chunksDir, "chunk-0001.md"),
		[]byte("---\nchunk_index: 1\ntotal_chunks: 2\nsection: Opening\n---\nChunk body.\n"),
		0o644,
	); err != nil {
		t.Fatalf("write chunk: %v", err)
	}

	docs, err := ScanSource(context.Background(), settings, Source{
		ID:         "parable-chunks",
		Collection: CollectionLibrary,
		SourceType: SourceTypeChunkedMarkdownTree,
		Path:       "/workspace/library/octavia-butler/parable/chunks",
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("ScanSource() error = %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("docs len = %d, want 1", len(docs))
	}
	doc := docs[0]
	if doc.Identity != "/workspace/library/octavia-butler/parable/chunks/chunk-0001.md" {
		t.Fatalf("identity = %q", doc.Identity)
	}
	for key, want := range map[string]any{
		"title":           "Parable",
		"work_type":       "book",
		"section":         "Opening",
		"parent_slug":     "chunks",
		"container_slug":  "parable",
		"metadata_source": "meta.yml",
	} {
		if got := doc.Payload[key]; got != want {
			t.Fatalf("payload[%s] = %#v, want %#v", key, got, want)
		}
	}
	if got, want := doc.Payload["chunk_index"], int64(1); got != want {
		t.Fatalf("chunk_index = %#v, want %#v", got, want)
	}
	if doc.Text != "Chunk body." {
		t.Fatalf("Text = %q, want Chunk body.", doc.Text)
	}
}
