package skills

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/fileops"
)

func TestLoadCatalogIncludesBuiltinAndValidSharedSkills(t *testing.T) {
	t.Parallel()

	cfg := newTestSettings(t)
	writeSkillFile(t, filepath.Join(cfg.SkillsLocalDir, "my-skill", skillFileName), `---
name: my-skill
description: Shared skill description.
---

# My Skill
`)
	if err := os.MkdirAll(filepath.Join(cfg.SkillsLocalDir, "broken"), 0o755); err != nil {
		t.Fatalf("MkdirAll(broken) error = %v", err)
	}

	manager := NewManager(cfg)
	catalog := manager.LoadCatalog()

	if len(catalog.Entries) != 3 {
		t.Fatalf("catalog entries = %#v, want 2 builtins + shared", catalog.Entries)
	}
	names := entryNames(catalog.Entries)
	for _, want := range []string{"skill-creator", "skill-discovery", "my-skill"} {
		if !contains(names, want) {
			t.Fatalf("catalog names = %v, want %q present", names, want)
		}
	}
	if len(catalog.Warnings) != 1 ||
		!strings.Contains(catalog.Warnings[0], `skipping invalid shared skill "broken"`) {
		t.Fatalf("catalog warnings = %#v", catalog.Warnings)
	}
}

func TestBuiltinSkillCreatorExplicitlyRequiresColocatedResources(t *testing.T) {
	t.Parallel()

	raw, err := builtinSkillFS.ReadFile("builtins/skill-creator/SKILL.md")
	if err != nil {
		t.Fatalf("ReadFile(skill-creator) error = %v", err)
	}
	text := string(raw)
	for _, want := range []string{
		"Keep every final skill-owned file inside that skill directory.",
		"Do not leave final skill resources in `/workspace`.",
		"use absolute paths under `/skills/<name>/...`",
		"every final skill resource is stored under `/skills/<name>/...`",
		"/skills/@builtin/skill-discovery/SKILL.md",
		"do not blindly copy another skill",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("builtin skill-creator missing %q:\n%s", want, text)
		}
	}
}

func TestBuiltinSkillDiscoveryGuidesExternalDiscoveryAndAdaptation(t *testing.T) {
	t.Parallel()

	raw, err := builtinSkillFS.ReadFile("builtins/skill-discovery/SKILL.md")
	if err != nil {
		t.Fatalf("ReadFile(skill-discovery) error = %v", err)
	}
	text := string(raw)
	for _, want := range []string{
		"skills.sh",
		"public agent-skill repositories",
		"Use `web_search` when available",
		"rewrite it as a q15-native local skill",
		"Do not clone another",
		"/skills/@builtin/skill-creator/SKILL.md",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("builtin skill-discovery missing %q:\n%s", want, text)
		}
	}
}

func TestValidateSkillSupportsBuiltinSharedAndWorkspaceDrafts(t *testing.T) {
	t.Parallel()

	cfg := newTestSettings(t)
	writeSkillFile(t, filepath.Join(cfg.SkillsLocalDir, "my-skill", skillFileName), `---
name: my-skill
description: Shared skill description.
---
`)
	writeSkillFile(
		t,
		filepath.Join(cfg.WorkspaceLocalDir, "drafts", "draft-skill", skillFileName),
		`---
name: draft-skill
description: Draft description.
---
`,
	)

	manager := NewManager(cfg)

	builtin, err := manager.ValidateSkill("/skills/@builtin/skill-creator")
	if err != nil {
		t.Fatalf("ValidateSkill(builtin) error = %v", err)
	}
	if !builtin.Valid || builtin.Source != SourceBuiltin {
		t.Fatalf("builtin result = %#v", builtin)
	}

	discovery, err := manager.ValidateSkill("/skills/@builtin/skill-discovery")
	if err != nil {
		t.Fatalf("ValidateSkill(skill-discovery) error = %v", err)
	}
	if !discovery.Valid || discovery.Source != SourceBuiltin ||
		discovery.Name != "skill-discovery" {
		t.Fatalf("skill-discovery result = %#v", discovery)
	}

	shared, err := manager.ValidateSkill("/skills/my-skill")
	if err != nil {
		t.Fatalf("ValidateSkill(shared) error = %v", err)
	}
	if !shared.Valid || shared.Source != SourceShared || shared.Name != "my-skill" {
		t.Fatalf("shared result = %#v", shared)
	}

	draft, err := manager.ValidateSkill("drafts/draft-skill")
	if err != nil {
		t.Fatalf("ValidateSkill(draft) error = %v", err)
	}
	if !draft.Valid || draft.Source != SourceDraft || draft.Name != "draft-skill" {
		t.Fatalf("draft result = %#v", draft)
	}
}

func TestReadBuiltinFileSupportsPagination(t *testing.T) {
	t.Parallel()

	manager := NewManager(Settings{})
	got, handled, err := manager.ReadBuiltinFile("/skills/@builtin/skill-creator/SKILL.md", 1, 4)
	if err != nil {
		t.Fatalf("ReadBuiltinFile() error = %v", err)
	}
	if !handled {
		t.Fatal("ReadBuiltinFile() handled = false, want true")
	}
	if got.TotalLines == 0 || !strings.Contains(got.Content, "name: skill-creator") {
		t.Fatalf("ReadBuiltinFile() = %#v", got)
	}
}

func TestFileExecutorRejectsBuiltinWritesAndDelegatesSharedPaths(t *testing.T) {
	t.Parallel()

	cfg := newTestSettings(t)
	manager := NewManager(cfg)
	delegate := &stubFileDelegate{}
	exec := NewFileExecutor(delegate, manager)

	if _, err := exec.WriteFile(context.Background(), "/skills/@builtin/skill-creator/SKILL.md", "x"); err == nil ||
		!strings.Contains(err.Error(), "read-only") {
		t.Fatalf("WriteFile() error = %v, want builtin read-only error", err)
	}

	disabledManager := NewManager(Settings{
		WorkspaceLocalDir:   cfg.WorkspaceLocalDir,
		WorkspaceRuntimeDir: cfg.WorkspaceRuntimeDir,
		SkillsRuntimeDir:    cfg.SkillsRuntimeDir,
	})
	exec = NewFileExecutor(delegate, disabledManager)
	if _, err := exec.ReadFile(context.Background(), "/skills/shared/SKILL.md", 0, 0); err == nil ||
		!strings.Contains(err.Error(), "shared skills root is not configured") {
		t.Fatalf("ReadFile() error = %v, want missing shared root error", err)
	}

	manager = NewManager(cfg)
	exec = NewFileExecutor(delegate, manager)
	if _, err := exec.ReadFile(context.Background(), "/skills/shared/SKILL.md", 0, 0); err != nil {
		t.Fatalf("ReadFile(shared) error = %v", err)
	}
	if delegate.readPath != "/skills/shared/SKILL.md" {
		t.Fatalf("delegate.readPath = %q", delegate.readPath)
	}
}

type stubFileDelegate struct {
	readPath string
}

func (s *stubFileDelegate) ReadFile(
	_ context.Context,
	path string,
	_ int,
	_ int,
) (fileops.ReadResult, error) {
	s.readPath = path
	return fileops.ReadResult{Content: "ok", TotalLines: 1}, nil
}

func (s *stubFileDelegate) WriteFile(
	_ context.Context,
	path string,
	content string,
) (fileops.WriteResult, error) {
	return fileops.WriteResult{Path: path, BytesWritten: len(content)}, nil
}

func (s *stubFileDelegate) EditFile(
	_ context.Context,
	path string,
	_ string,
	_ string,
) (fileops.EditResult, error) {
	return fileops.EditResult{Path: path}, nil
}

func (s *stubFileDelegate) ApplyPatch(
	_ context.Context,
	_ string,
) (fileops.ApplyPatchResult, error) {
	return fileops.ApplyPatchResult{Summary: "ok"}, nil
}

func newTestSettings(t *testing.T) Settings {
	t.Helper()

	root := t.TempDir()
	workspaceLocalDir := filepath.Join(root, "workspace")
	skillsLocalDir := filepath.Join(root, "skills")
	if err := os.MkdirAll(workspaceLocalDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(workspace) error = %v", err)
	}
	return Settings{
		WorkspaceLocalDir:   workspaceLocalDir,
		WorkspaceRuntimeDir: "/workspace",
		SkillsLocalDir:      skillsLocalDir,
		SkillsRuntimeDir:    "/skills",
	}
}

func writeSkillFile(t *testing.T, fullPath string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(fullPath), err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", fullPath, err)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func entryNames(entries []Entry) []string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name)
	}
	return names
}
