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

func TestRegistryAddRejectsExactDuplicatePath(t *testing.T) {
	ctx := context.Background()
	settings := testSettings(t)
	mustMkdirRuntime(t, settings, "/workspace/library")

	registry := NewRegistry(settings)
	mustAddSource(ctx, t, registry, Source{
		ID:         "library-chunks",
		Collection: CollectionLibrary,
		SourceType: SourceTypeChunkedMarkdownTree,
		Path:       "/workspace/library",
		Enabled:    true,
	})

	// Mirrors the 2026-06-04 double-indexing incident: a second catch-all
	// source registered over the exact same path in the same collection.
	_, err := registry.Add(ctx, Source{
		ID:         "library-chunks-malema-batch",
		Collection: CollectionLibrary,
		SourceType: SourceTypeChunkedMarkdownTree,
		Path:       "/workspace/library",
		Enabled:    true,
	})
	if err == nil {
		t.Fatal("Add() duplicate path error = nil, want overlap rejection")
	}
}

func TestRegistryAddRejectsChildPathOfExisting(t *testing.T) {
	ctx := context.Background()
	settings := testSettings(t)
	mustMkdirRuntime(t, settings, "/workspace/library/fiction")

	registry := NewRegistry(settings)
	mustAddSource(ctx, t, registry, Source{
		ID:         "library-chunks",
		Collection: CollectionLibrary,
		SourceType: SourceTypeMarkdownTree,
		Path:       "/workspace/library",
		Enabled:    true,
	})

	_, err := registry.Add(ctx, Source{
		ID:         "library-fiction",
		Collection: CollectionLibrary,
		SourceType: SourceTypeMarkdownTree,
		Path:       "/workspace/library/fiction",
		Enabled:    true,
	})
	if err == nil {
		t.Fatal("Add() child-of-existing path error = nil, want overlap rejection")
	}
}

func TestRegistryAddRejectsAncestorOfExisting(t *testing.T) {
	ctx := context.Background()
	settings := testSettings(t)
	mustMkdirRuntime(t, settings, "/workspace/library/fiction")

	registry := NewRegistry(settings)
	mustAddSource(ctx, t, registry, Source{
		ID:         "library-fiction",
		Collection: CollectionLibrary,
		SourceType: SourceTypeMarkdownTree,
		Path:       "/workspace/library/fiction",
		Enabled:    true,
	})

	_, err := registry.Add(ctx, Source{
		ID:         "library-chunks",
		Collection: CollectionLibrary,
		SourceType: SourceTypeMarkdownTree,
		Path:       "/workspace/library",
		Enabled:    true,
	})
	if err == nil {
		t.Fatal("Add() ancestor-of-existing path error = nil, want overlap rejection")
	}
}

func TestRegistryAddOverlapErrorNamesExistingSource(t *testing.T) {
	ctx := context.Background()
	settings := testSettings(t)
	mustMkdirRuntime(t, settings, "/workspace/library")

	registry := NewRegistry(settings)
	mustAddSource(ctx, t, registry, Source{
		ID:         "library-chunks",
		Collection: CollectionLibrary,
		SourceType: SourceTypeMarkdownTree,
		Path:       "/workspace/library",
		Enabled:    true,
	})

	_, err := registry.Add(ctx, Source{
		ID:         "library-again",
		Collection: CollectionLibrary,
		SourceType: SourceTypeMarkdownTree,
		Path:       "/workspace/library/",
		Enabled:    true,
	})
	if err == nil {
		t.Fatal("Add() overlapping path error = nil, want overlap rejection")
	}
	want := `source path "/workspace/library" overlaps existing source ` +
		`"library-chunks" in collection "library"`
	if err.Error() != want {
		t.Fatalf("Add() overlap error = %q, want %q", err.Error(), want)
	}
}

func TestRegistryAddAllowsSiblingWithSharedPathPrefix(t *testing.T) {
	ctx := context.Background()
	settings := testSettings(t)
	mustMkdirRuntime(t, settings, "/workspace/library")
	mustMkdirRuntime(t, settings, "/workspace/library-friends")

	registry := NewRegistry(settings)
	mustAddSource(ctx, t, registry, Source{
		ID:         "library-chunks",
		Collection: CollectionLibrary,
		SourceType: SourceTypeMarkdownTree,
		Path:       "/workspace/library",
		Enabled:    true,
	})

	added, err := registry.Add(ctx, Source{
		ID:         "library-friends",
		Collection: CollectionLibrary,
		SourceType: SourceTypeMarkdownTree,
		Path:       "/workspace/library-friends",
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("Add() sibling path error = %v, want success", err)
	}
	if added.ID != "library-friends" {
		t.Fatalf("added ID = %q, want library-friends", added.ID)
	}
}

func TestRegistryAddAllowsSamePathInDifferentCollection(t *testing.T) {
	ctx := context.Background()
	settings := testSettings(t)
	mustMkdirRuntime(t, settings, "/workspace/shared")

	registry := NewRegistry(settings)
	mustAddSource(ctx, t, registry, Source{
		ID:         "shared-semantic",
		Collection: CollectionSemantic,
		SourceType: SourceTypeMarkdownTree,
		Path:       "/workspace/shared",
		Enabled:    true,
	})

	if _, err := registry.Add(ctx, Source{
		ID:         "shared-library",
		Collection: CollectionLibrary,
		SourceType: SourceTypeMarkdownTree,
		Path:       "/workspace/shared",
		Enabled:    true,
	}); err != nil {
		t.Fatalf("Add() same path in different collection error = %v, want success", err)
	}
}

func TestRegistryAddRejectsOverlapWithDisabledSource(t *testing.T) {
	ctx := context.Background()
	settings := testSettings(t)
	mustMkdirRuntime(t, settings, "/workspace/library")

	registry := NewRegistry(settings)
	mustAddSource(ctx, t, registry, Source{
		ID:         "library-chunks",
		Collection: CollectionLibrary,
		SourceType: SourceTypeMarkdownTree,
		Path:       "/workspace/library",
		Enabled:    true,
	})
	if _, err := registry.SetEnabled(ctx, "library-chunks", false); err != nil {
		t.Fatalf("SetEnabled(false) error = %v", err)
	}

	_, err := registry.Add(ctx, Source{
		ID:         "library-replacement",
		Collection: CollectionLibrary,
		SourceType: SourceTypeMarkdownTree,
		Path:       "/workspace/library",
		Enabled:    true,
	})
	if err == nil || !strings.Contains(err.Error(), `"library-chunks"`) {
		t.Fatalf(
			"Add() over disabled existing error = %v, want overlap naming library-chunks",
			err,
		)
	}
}

func TestRegistryAddUnrelatedPathSucceeds(t *testing.T) {
	ctx := context.Background()
	settings := testSettings(t)
	mustMkdirRuntime(t, settings, "/workspace/library")
	mustMkdirRuntime(t, settings, "/workspace/inbox")

	registry := NewRegistry(settings)
	mustAddSource(ctx, t, registry, Source{
		ID:         "library-chunks",
		Collection: CollectionLibrary,
		SourceType: SourceTypeMarkdownTree,
		Path:       "/workspace/library",
		Enabled:    true,
	})

	added, err := registry.Add(ctx, Source{
		ID:         "inbox",
		Collection: CollectionSemantic,
		SourceType: SourceTypeMarkdownTree,
		Path:       "/workspace/inbox",
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("Add() unrelated path error = %v, want success", err)
	}
	if added.ID != "inbox" {
		t.Fatalf("added ID = %q, want inbox", added.ID)
	}
}

func TestRegistryAddRejectsDuplicateID(t *testing.T) {
	ctx := context.Background()
	settings := testSettings(t)
	mustMkdirRuntime(t, settings, "/workspace/library")
	mustMkdirRuntime(t, settings, "/workspace/inbox")

	registry := NewRegistry(settings)
	mustAddSource(ctx, t, registry, Source{
		ID:         "docs",
		Collection: CollectionLibrary,
		SourceType: SourceTypeMarkdownTree,
		Path:       "/workspace/library",
		Enabled:    true,
	})

	_, err := registry.Add(ctx, Source{
		ID:         "docs",
		Collection: CollectionLibrary,
		SourceType: SourceTypeMarkdownTree,
		Path:       "/workspace/inbox",
		Enabled:    true,
	})
	if err == nil || err.Error() != `source id "docs" already exists` {
		t.Fatalf("Add() duplicate id error = %v, want duplicate id rejection", err)
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

func mustAddSource(
	ctx context.Context,
	t *testing.T,
	registry *Registry,
	source Source,
) Source {
	t.Helper()
	added, err := registry.Add(ctx, source)
	if err != nil {
		t.Fatalf("Add(%q) error = %v", source.ID, err)
	}
	return added
}

func mustMkdirRuntime(t *testing.T, settings Settings, runtimePath string) {
	t.Helper()
	rel, ok := strings.CutPrefix(runtimePath, "/workspace/")
	if !ok {
		t.Fatalf("mustMkdirRuntime only supports /workspace/ paths, got %q", runtimePath)
	}
	local := filepath.Join(settings.WorkspaceLocalDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatalf("create %s: %v", runtimePath, err)
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
