package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/memoryrepo"
)

type recordingCommitter struct{ paths []string }

func (*recordingCommitter) EnsureRepo(context.Context, string) error { return nil }

func (c *recordingCommitter) CommitPaths(
	_ context.Context,
	_ string,
	_ string,
	paths []string,
) error {
	c.paths = append(c.paths, paths...)
	return nil
}
func (*recordingCommitter) ResetPaths(context.Context, string, []string) error { return nil }

func TestIdentityInitCreatesNoTranscriptOrJobState(t *testing.T) {
	root := t.TempDir()
	committer := &recordingCommitter{}
	store := NewStore(memoryrepo.New(root, committer), "Jared")
	for range 2 {
		if err := store.Init(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	files, err := store.LoadCoreMemory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(files.Files) != 3 {
		t.Fatalf("identity files=%d", len(files.Files))
	}
	for _, file := range files.Files {
		if strings.Contains(file.Content, "{{agent_name}}") || file.Description == "" {
			t.Fatalf("unrendered identity metadata: %s", file.RelativePath)
		}
	}
	for _, name := range []string{"working", "history", "cognition", "semantic"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("retired scaffold %s created", name)
		}
	}
	for _, path := range committer.paths {
		if path != "core" && path != "notes" {
			t.Fatalf("unexpected initialization commit path: %s", path)
		}
	}
}

func TestIdentityInitPreservesAuthoredContent(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "core"), 0755); err != nil {
		t.Fatal(err)
	}
	content := "---\ndescription: Custom identity\nlimit: 1234\n---\n\nMy name is {{agent_name}}.\n"
	if err := os.WriteFile(filepath.Join(root, "core/AGENT.md"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	store := NewStore(memoryrepo.New(root, &recordingCommitter{}), "Jared")
	if err := store.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	files, err := store.LoadCoreMemory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	first := files.Files[0]
	if first.Content != "My name is Jared." || first.Description != "Custom identity" ||
		first.Limit != 1234 {
		t.Fatalf("identity changed: %+v", first)
	}
	raw, err := os.ReadFile(filepath.Join(root, "core/AGENT.md"))
	if err != nil || string(raw) != content {
		t.Fatal("authored file rewritten", err)
	}
}
