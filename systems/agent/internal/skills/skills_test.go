package skills

import (
	"context"
	"fmt"
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
	writeSkillFile(t, filepath.Join(cfg.SkillsLocalDir, "tooled-skill", skillFileName), `---
name: tooled-skill
description: A skill with declared tools.
tools:
  - read_file
  - exec
---

# Tooled Skill
`)
	if err := os.MkdirAll(filepath.Join(cfg.SkillsLocalDir, "broken"), 0o755); err != nil {
		t.Fatalf("MkdirAll(broken) error = %v", err)
	}

	manager := NewManager(cfg)
	catalog := manager.LoadCatalog()

	if len(catalog.Entries) != 4 {
		t.Fatalf("catalog entries = %#v, want 2 builtins + shared", catalog.Entries)
	}
	names := entryNames(catalog.Entries)
	for _, want := range []string{"skill-creator", "skill-discovery", "my-skill", "tooled-skill"} {
		if !contains(names, want) {
			t.Fatalf("catalog names = %v, want %q present", names, want)
		}
	}
	// Verify the tooled skill exposes its declared tools.
	for _, entry := range catalog.Entries {
		if entry.Name != "tooled-skill" {
			continue
		}
		if len(entry.Tools) != 2 || entry.Tools[0] != "read_file" || entry.Tools[1] != "exec" {
			t.Fatalf("tooled-skill Tools = %#v, want [read_file exec]", entry.Tools)
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

func TestValidateSkillParsesToolsFrontmatter(t *testing.T) {
	t.Parallel()

	cfg := newTestSettings(t)
	writeSkillFile(t, filepath.Join(cfg.SkillsLocalDir, "tooled", skillFileName), `---
name: tooled
description: A skill with declared tools.
tools:
  - read_file
  - exec
  - read_file
---

# Tooled
`)

	result, err := NewManager(cfg).ValidateSkill("/skills/tooled")
	if err != nil {
		t.Fatalf("ValidateSkill() error = %v", err)
	}
	if !result.Valid {
		t.Fatalf("result = %#v, want valid", result)
	}
	if len(result.Tools) != 2 || result.Tools[0] != "read_file" || result.Tools[1] != "exec" {
		t.Fatalf("Tools = %#v, want [read_file exec] (deduped)", result.Tools)
	}
	// Duplicate declaration is a warning, not an error.
	foundDupWarning := false
	for _, w := range result.Warnings {
		if strings.Contains(w, `duplicate declared tool "read_file"`) {
			foundDupWarning = true
			break
		}
	}
	if !foundDupWarning {
		t.Fatalf("warnings = %#v, want duplicate tool warning", result.Warnings)
	}
}

func TestValidateSkillRejectsInvalidToolsFrontmatter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		wantErr string
	}{
		{
			name: "scalar instead of list",
			content: `---
name: bad
description: x
tools: read_file
---
`,
			wantErr: `"tools" must be a list of strings`,
		},
		{
			name: "non-string item",
			content: `---
name: bad
description: x
tools:
  - 42
---
`,
			wantErr: "must be a string",
		},
		{
			name: "empty string",
			content: `---
name: bad
description: x
tools:
  - ""
---
`,
			wantErr: "is empty",
		},
		{
			name: "invalid characters",
			content: `---
name: bad
description: x
tools:
  - "Bad Name!"
---
`,
			wantErr: "lowercase letters",
		},
		{
			name: "overly long name",
			content: `---
name: bad
description: x
tools:
  - "` + strings.Repeat("a", maxDeclaredToolNameLen+1) + `"
---
`,
			wantErr: "<= " + fmt.Sprintf("%d", maxDeclaredToolNameLen) + " characters",
		},
		{
			name: "too many tools",
			content: "---\nname: bad\ndescription: x\ntools:\n" +
				strings.Repeat("  - a\n", maxDeclaredTools+1) + "---\n",
			wantErr: "<= " + fmt.Sprintf("%d", maxDeclaredTools) + " entries",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := newTestSettings(t)
			writeSkillFile(t, filepath.Join(cfg.SkillsLocalDir, "bad", skillFileName), tt.content)
			result, err := NewManager(cfg).ValidateSkill("/skills/bad")
			if err != nil {
				t.Fatalf("ValidateSkill() error = %v", err)
			}
			if result.Valid {
				t.Fatalf("result = %#v, want invalid", result)
			}
			found := false
			for _, e := range result.Errors {
				if strings.Contains(e, tt.wantErr) {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("errors = %#v, want %q", result.Errors, tt.wantErr)
			}
		})
	}
}

func TestResolveSkillByNameAndPathIncludesBodyAndTools(t *testing.T) {
	t.Parallel()

	cfg := newTestSettings(t)
	writeSkillFile(t, filepath.Join(cfg.SkillsLocalDir, "shared-skill", skillFileName), `---
name: shared-skill
description: A shared skill.
tools:
  - read_file
---

# Shared Skill

Do things.
`)
	writeSkillFile(
		t,
		filepath.Join(cfg.WorkspaceLocalDir, "drafts", "draft-skill", skillFileName),
		`---
name: draft-skill
description: A draft skill.
tools:
  - exec
---

# Draft Skill

Draft body.
`,
	)

	manager := NewManager(cfg)

	// Resolve by bare name (shared skill).
	resolved, err := manager.ResolveSkill("shared-skill")
	if err != nil {
		t.Fatalf("ResolveSkill(shared-skill) error = %v", err)
	}
	if resolved.Name != "shared-skill" {
		t.Fatalf("name = %q", resolved.Name)
	}
	if len(resolved.Tools) != 1 || resolved.Tools[0] != "read_file" {
		t.Fatalf("tools = %#v", resolved.Tools)
	}
	if !strings.Contains(resolved.Body, "# Shared Skill") {
		t.Fatalf("body = %q, want stripped body", resolved.Body)
	}

	// Resolve by workspace draft path.
	resolvedDraft, err := manager.ResolveSkill("drafts/draft-skill")
	if err != nil {
		t.Fatalf("ResolveSkill(drafts/draft-skill) error = %v", err)
	}
	if resolvedDraft.Name != "draft-skill" {
		t.Fatalf("name = %q", resolvedDraft.Name)
	}
	if len(resolvedDraft.Tools) != 1 || resolvedDraft.Tools[0] != "exec" {
		t.Fatalf("tools = %#v", resolvedDraft.Tools)
	}
	if !strings.Contains(resolvedDraft.Body, "# Draft Skill") {
		t.Fatalf("body = %q, want stripped body", resolvedDraft.Body)
	}

	// Resolve a builtin by path.
	resolvedBuiltin, err := manager.ResolveSkill("/skills/@builtin/skill-creator")
	if err != nil {
		t.Fatalf("ResolveSkill(builtin) error = %v", err)
	}
	if resolvedBuiltin.Name != "skill-creator" {
		t.Fatalf("builtin name = %q", resolvedBuiltin.Name)
	}
	if strings.TrimSpace(resolvedBuiltin.Body) == "" {
		t.Fatal("builtin body is empty")
	}

	// Unknown name is a hard error.
	if _, err := manager.ResolveSkill("does-not-exist"); err == nil ||
		!strings.Contains(err.Error(), `unknown skill "does-not-exist"`) {
		t.Fatalf("ResolveSkill(unknown) error = %v, want unknown skill error", err)
	}
}
