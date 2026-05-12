package embed

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegistryAddRemoveEnableDisable(t *testing.T) {
	ctx := context.Background()
	settings := testSettings(t)
	sourceDir := filepath.Join(settings.WorkspaceLocalDir, "docs")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatalf("create source dir: %v", err)
	}

	registry := NewRegistry(settings)
	source, err := registry.Add(ctx, Source{
		ID:         "docs",
		Collection: CollectionSemantic,
		SourceType: SourceTypeMarkdownTree,
		Path:       "/workspace/docs",
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if source.ID != "docs" {
		t.Fatalf("ID = %q, want docs", source.ID)
	}

	disabled, err := registry.SetEnabled(ctx, "docs", false)
	if err != nil {
		t.Fatalf("SetEnabled(false) error = %v", err)
	}
	if disabled.Enabled {
		t.Fatal("disabled source Enabled = true")
	}
	enabled, err := registry.SetEnabled(ctx, "docs", true)
	if err != nil {
		t.Fatalf("SetEnabled(true) error = %v", err)
	}
	if !enabled.Enabled {
		t.Fatal("enabled source Enabled = false")
	}

	removed, err := registry.Remove(ctx, "docs")
	if err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if removed.ID != "docs" {
		t.Fatalf("removed ID = %q, want docs", removed.ID)
	}
}

func TestRegistryValidatesPathRootsAndSourceTypeCompatibility(t *testing.T) {
	ctx := context.Background()
	settings := testSettings(t)
	if err := os.MkdirAll(filepath.Join(settings.WorkspaceLocalDir, "docs"), 0o755); err != nil {
		t.Fatalf("create docs: %v", err)
	}
	mdPath := filepath.Join(settings.WorkspaceLocalDir, "note.md")
	if err := os.WriteFile(mdPath, []byte("# Note\n"), 0o644); err != nil {
		t.Fatalf("write note: %v", err)
	}

	registry := NewRegistry(settings)
	_, err := registry.Add(ctx, Source{
		ID:         "outside",
		Collection: CollectionSemantic,
		SourceType: SourceTypeMarkdownTree,
		Path:       "/tmp/outside",
		Enabled:    true,
	})
	if err == nil ||
		!strings.Contains(err.Error(), "must be under /workspace, /memory, or /skills") {
		t.Fatalf("Add outside path error = %v, want root validation", err)
	}

	_, err = registry.Add(ctx, Source{
		ID:         "dir-as-file",
		Collection: CollectionSemantic,
		SourceType: SourceTypeMarkdownFile,
		Path:       "/workspace/docs",
		Enabled:    true,
	})
	if err == nil || !strings.Contains(err.Error(), "must be a markdown file") {
		t.Fatalf("Add markdown_file directory error = %v", err)
	}

	_, err = registry.Add(ctx, Source{
		ID:         "file-as-tree",
		Collection: CollectionSemantic,
		SourceType: SourceTypeMarkdownTree,
		Path:       "/workspace/note.md",
		Enabled:    true,
	})
	if err == nil || !strings.Contains(err.Error(), "must be a directory") {
		t.Fatalf("Add markdown_tree file error = %v", err)
	}

	_, err = registry.Add(ctx, Source{
		ID:           "metadata-on-tree",
		Collection:   CollectionSemantic,
		SourceType:   SourceTypeMarkdownTree,
		Path:         "/workspace/docs",
		MetadataPath: "/workspace/note.md",
		Enabled:      true,
	})
	if err == nil || !strings.Contains(err.Error(), "metadata_path is only supported") {
		t.Fatalf("Add metadata_path on markdown_tree error = %v", err)
	}
}

func TestRegistryDefaultsMissingEnabledToTrue(t *testing.T) {
	settings := testSettings(t)
	if err := os.MkdirAll(filepath.Dir(settings.RegistryPath), 0o755); err != nil {
		t.Fatalf("create registry dir: %v", err)
	}
	if err := os.WriteFile(settings.RegistryPath, []byte(`{
  "version": 1,
  "sources": [
    {
      "id": "manual",
      "collection": "semantic",
      "source_type": "markdown_tree",
      "path": "/workspace/manual"
    }
  ]
}
`), 0o644); err != nil {
		t.Fatalf("write registry: %v", err)
	}

	sources, err := NewRegistry(settings).List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("sources len = %d, want 1", len(sources))
	}
	if !sources[0].Enabled {
		t.Fatal("missing enabled field loaded as false, want true")
	}
}

func testSettings(t *testing.T) Settings {
	t.Helper()
	root := t.TempDir()
	settings := Settings{
		WorkspaceLocalDir: filepath.Join(root, "workspace"),
		MemoryLocalDir:    filepath.Join(root, "memory"),
		SkillsLocalDir:    filepath.Join(root, "skills"),
		RegistryPath:      filepath.Join(root, "workspace", ".q15", "embed", "sources.json"),
		StatePath:         filepath.Join(root, "workspace", ".q15", "embed", "state.jsonl"),
	}
	for _, dir := range []string{
		settings.WorkspaceLocalDir,
		settings.MemoryLocalDir,
		settings.SkillsLocalDir,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create %s: %v", dir, err)
		}
	}
	return settings
}
