package app

import (
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/sandbox"
)

func TestComposeSystemPromptIncludesOSRuntimeAndNixBashDetails(t *testing.T) {
	info := sandbox.Info{
		ContainerName: "q15-jared",
		WorkspaceDir:  "/workspace",
		SkillsDir:     "/skills",
		Runtime:       "nix-only",
		BaseImage:     "registry.example/sandbox:test",
		OSPrettyName:  "Debian GNU/Linux 12 (bookworm)",
		NixPath:       "/root/.nix-profile/bin/nix",
		NixVersion:    "nix (Nix) 2.33.3",
		BashPath:      "/bin/bash",
		BashVersion:   "GNU bash, version 5.2.15(1)-release (x86_64-pc-linux-gnu)",
	}

	prompt := composeSystemPrompt("Base prompt", "Jared", info, "/memory", []agent.ToolDefinition{
		{
			Name:        "read_file",
			Description: "Read a file",
			PromptGuidance: []string{
				"Use for routine UTF-8 text reads instead of shelling out.",
				"Use for routine UTF-8 text reads instead of shelling out.",
			},
		},
		{
			Name: "exec_nix_shell_bash",
			PromptGuidance: []string{
				"Use for commands, builds, tests, formatting, git, and other CLI workflows.",
			},
		},
	})

	for _, want := range []string{
		"<sandbox_environment>",
		"- OS: Debian GNU/Linux 12 (bookworm)",
		"- Sandbox runtime: nix-only",
		"- Base image: registry.example/sandbox:test",
		"- Shared skills root: /skills (bind-mounted when skills.host_dir is configured)",
		"- Package management model: nix-only via exec_nix_shell_bash.",
		"- Built-in skills are available read-only under `/skills/@builtin/...` via read_file even when no shared skills mount is configured.",
		"- Shared skills, when configured, are available under `/skills/<name>/...` and may be edited with the normal file tools.",
		"- Use read_file for routine UTF-8 text reads from the workspace, memory, or skills roots; paths may be relative to the workspace or absolute under `/workspace/...`, `/memory/...`, `/skills/...`, or `/skills/@builtin/...`.",
		"- Use write_file to create or fully replace UTF-8 text files in the workspace, memory, or shared skills roots.",
		"- Use edit_file for a single exact text replacement in an existing UTF-8 text file in the workspace, memory, or shared skills roots when you know the current text.",
		"- Use apply_patch for multi-file or diff-style edits in the workspace, memory, or shared skills roots using the high-level patch envelope.",
		"- Use validate_skill after creating or updating a skill directory.",
		"- apply_patch does not accept unified diff, git diff, or context diff syntax. Never send `diff --git`, `--- a/...`, `+++ b/...`, `*** a/...`, `*** b/...`, or bare path lines.",
		"- apply_patch patches must start with `*** Begin Patch` and end with `*** End Patch`.",
		"- Inside apply_patch, use exactly one of `*** Add File: PATH`, `*** Delete File: PATH`, or `*** Update File: PATH`. For renames, put `*** Move to: NEW_PATH` immediately after `*** Update File: PATH`.",
		"- In `*** Add File`, every file-content line must start with `+`.",
		"- In `*** Update File`, each hunk must start with `@@`, then use a leading space for context lines, `-` for removed lines, and `+` for added lines.",
		"- Minimal apply_patch example:",
		"*** Begin Patch",
		"*** Update File: /memory/notes/todo.md",
		"@@",
		"-old value",
		"+new value",
		"*** End Patch",
		"- Use exec_nix_shell_bash for commands, builds, tests, formatting, git, and other CLI workflows, not for routine file reads or edits.",
		"- Every exec_nix_shell_bash call must include a non-empty `packages` array of nix installables (for example `nixpkgs#git`).",
		"- Use exec_nix_shell_bash by providing the user command in `command` and the needed nix installables in `packages`; the sandbox runtime provisions those packages and executes the command via nix shell and bash.",
		"- Use web_fetch for known web page URLs: it returns cleaned markdown plus slice metadata and is preferred over using exec_nix_shell_bash with curl for ordinary webpage reads.",
		"- Use web_search for discovering current sources, then use web_fetch on a chosen result URL when you need page contents.",
		"- Nix: /root/.nix-profile/bin/nix (nix (Nix) 2.33.3)",
		"- Bash: /bin/bash (GNU bash, version 5.2.15(1)-release (x86_64-pc-linux-gnu))",
		"<tool_advice>",
		`<tool name="read_file" summary="Read a file">`,
		`<tool name="exec_nix_shell_bash">`,
		"- Use for routine UTF-8 text reads instead of shelling out.",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}

	for _, notWanted := range []string{
		"- Detected package manager binary:",
		"- Available shells:",
		"- Preinstalled tools detected:",
		"exec_shell",
	} {
		if strings.Contains(prompt, notWanted) {
			t.Fatalf("prompt should not contain %q:\n%s", notWanted, prompt)
		}
	}

	if strings.Count(prompt, "Use for routine UTF-8 text reads instead of shelling out.") != 1 {
		t.Fatalf("duplicate tool guidance should be deduplicated:\n%s", prompt)
	}
	if strings.Index(prompt, "<sandbox_environment>") < strings.Index(prompt, "Base prompt") {
		t.Fatalf("sandbox section should be appended after base prompt:\n%s", prompt)
	}
	if strings.Index(prompt, "<tool_advice>") < strings.Index(prompt, "<sandbox_environment>") {
		t.Fatalf("tool advice should appear after sandbox environment:\n%s", prompt)
	}
}

func TestFormatBinarySummarySupportsPartialValues(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		version string
		want    string
	}{
		{
			name:    "path_and_version",
			path:    "/bin/bash",
			version: "GNU bash, version 5.2",
			want:    "/bin/bash (GNU bash, version 5.2)",
		},
		{
			name:    "path_only",
			path:    "/bin/bash",
			version: "",
			want:    "/bin/bash",
		},
		{
			name:    "version_only",
			path:    "",
			version: "nix (Nix) 2.33.3",
			want:    "nix (Nix) 2.33.3",
		},
		{
			name:    "empty",
			path:    "",
			version: "",
			want:    "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatBinarySummary(tc.path, tc.version); got != tc.want {
				t.Fatalf(
					"formatBinarySummary(%q, %q) = %q, want %q",
					tc.path,
					tc.version,
					got,
					tc.want,
				)
			}
		})
	}
}

func TestBuildToolListIncludesFileToolsInStableOrder(t *testing.T) {
	t.Parallel()

	toolList, err := buildToolList(nil, nil, "")
	if err != nil {
		t.Fatalf("buildToolList() error = %v", err)
	}

	if got, want := toolNames(toolList), []string{
		"read_file",
		"write_file",
		"edit_file",
		"apply_patch",
		"validate_skill",
		"exec_nix_shell_bash",
		"web_fetch",
	}; !equalStrings(got, want) {
		t.Fatalf("tool names = %v, want %v", got, want)
	}
}

func TestBuildToolListAppendsWebSearchWhenConfigured(t *testing.T) {
	t.Parallel()

	toolList, err := buildToolList(nil, nil, "brave-key")
	if err != nil {
		t.Fatalf("buildToolList() error = %v", err)
	}

	if got, want := toolNames(toolList), []string{
		"read_file",
		"write_file",
		"edit_file",
		"apply_patch",
		"validate_skill",
		"exec_nix_shell_bash",
		"web_fetch",
		"web_search",
	}; !equalStrings(got, want) {
		t.Fatalf("tool names = %v, want %v", got, want)
	}
}

func toolNames(toolList []agent.Tool) []string {
	names := make([]string, 0, len(toolList))
	for _, tool := range toolList {
		names = append(names, tool.Definition().Name)
	}
	return names
}

func equalStrings(got []string, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
