package app

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/q15co/q15/libs/exec-contract/execpb"
	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/execution"
	"github.com/q15co/q15/systems/agent/internal/fileops"
	q15media "github.com/q15co/q15/systems/agent/internal/media"
)

func TestComposeSystemPromptIncludesRuntimeAndExecGuidance(t *testing.T) {
	info := runtimeEnvironmentInfo{
		WorkspaceDir:        "/workspace",
		MemoryDir:           "/memory",
		MediaDir:            "/media",
		SkillsDir:           "/skills",
		ExecutorType:        "local-nix-shell",
		ProxyEnabled:        true,
		ProxyPolicyRevision: "rev-1",
	}

	prompt := composeSystemPrompt("Base prompt", "Jared", info, []agent.ToolDefinition{
		{
			Name:        "read_file",
			Description: "Read a file",
			PromptGuidance: []string{
				"Use for routine UTF-8 text reads instead of shelling out.",
			},
		},
		{
			Name: "exec",
			PromptGuidance: []string{
				"Use for commands, builds, tests, formatting, git, and other CLI workflows.",
			},
		},
	})

	for _, want := range []string{
		"<runtime_environment>",
		"- Workspace: /workspace",
		"- Persistent memory repo: /memory",
		"- Core self-model files (auto-injected into prompt each turn): /memory/core/*.md (seeded with AGENT.md, USER.md, SOUL.md)",
		"- Additional persistent memory layers (tool-fetched, not auto-injected): /memory/semantic, /memory/working, /memory/history, /memory/cognition",
		"- Transcript sequence bookkeeping lives under /memory/history/state/head.json.",
		"- Auxiliary notebook files live under /memory/notes/inbox, /memory/notes/zettel, and /memory/notes/maps using the built-in zettelkasten layout; they are not hidden system cognition state.",
		"- Runtime media root: /media",
		"- Shared skills root: /skills",
		"- Command runtime: q15-exec sessions via local-nix-shell",
		"- Proxy-mediated exec env injection is enabled (policy revision: rev-1).",
		"- Prefer exec for commands, builds, tests, formatting, git, and other CLI workflows, not for routine file reads or edits.",
		"- Browser-specific command presets are not built in; use exec directly with explicit browser packages when needed.",
		"- Use web_fetch for known web page URLs: it returns cleaned markdown plus slice metadata and is preferred over using exec with curl for ordinary webpage reads.",
		"<tool_advice>",
		`<tool name="exec">`,
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}

	if strings.Index(prompt, "<runtime_environment>") < strings.Index(prompt, "Base prompt") {
		t.Fatalf("runtime section should appear after base prompt:\n%s", prompt)
	}
}

func TestResolveRuntimeEnvironmentRequiresExecServiceFields(t *testing.T) {
	_, err := resolveRuntimeEnvironment(&execpb.GetRuntimeInfoResponse{})
	if err == nil || !strings.Contains(err.Error(), "workspace_dir") {
		t.Fatalf("resolveRuntimeEnvironment() error = %v", err)
	}
}

func TestBuildToolListIncludesOnlyCurrentRuntimeTools(t *testing.T) {
	t.Parallel()

	fileExec := &stubFileExecutor{}
	mediaStore, err := q15media.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}
	toolList, err := buildToolList(
		&stubExecutionService{},
		fileExec,
		nil,
		fileops.Settings{
			WorkspaceLocalDir:   t.TempDir(),
			WorkspaceRuntimeDir: "/workspace",
			MemoryLocalDir:      t.TempDir(),
			MemoryRuntimeDir:    "/memory",
			SkillsLocalDir:      t.TempDir(),
			SkillsRuntimeDir:    "/skills",
		},
		mediaStore,
		"",
	)
	if err != nil {
		t.Fatalf("buildToolList() error = %v", err)
	}

	if got, want := toolNames(toolList), []string{
		"read_file",
		"write_file",
		"edit_file",
		"apply_patch",
		"validate_skill",
		"exec",
		"load_image",
		"web_fetch",
	}; !equalStrings(got, want) {
		t.Fatalf("tool names = %v, want %v", got, want)
	}
}

func TestBuildToolListAppendsWebSearchWhenConfigured(t *testing.T) {
	t.Parallel()

	fileExec := &stubFileExecutor{}
	mediaStore, err := q15media.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}
	toolList, err := buildToolList(
		&stubExecutionService{},
		fileExec,
		nil,
		fileops.Settings{
			WorkspaceLocalDir:   t.TempDir(),
			WorkspaceRuntimeDir: "/workspace",
			MemoryLocalDir:      t.TempDir(),
			MemoryRuntimeDir:    "/memory",
			SkillsLocalDir:      t.TempDir(),
			SkillsRuntimeDir:    "/skills",
		},
		mediaStore,
		"brave-key",
	)
	if err != nil {
		t.Fatalf("buildToolList() error = %v", err)
	}

	if got, want := toolNames(toolList), []string{
		"read_file",
		"write_file",
		"edit_file",
		"apply_patch",
		"validate_skill",
		"exec",
		"load_image",
		"web_fetch",
		"web_search",
	}; !equalStrings(got, want) {
		t.Fatalf("tool names = %v, want %v", got, want)
	}
}

type stubExecutionService struct{}

func (stubExecutionService) GetRuntimeInfo(
	context.Context,
) (*execpb.GetRuntimeInfoResponse, error) {
	return &execpb.GetRuntimeInfoResponse{
		ExecutorType: "local-shell",
		WorkspaceDir: "/workspace",
		MemoryDir:    "/memory",
		MediaDir:     "/media",
		SkillsDir:    "/skills",
	}, nil
}

func (stubExecutionService) StartSession(
	context.Context,
	*execpb.StartSessionRequest,
) (*execpb.StartSessionResponse, error) {
	return &execpb.StartSessionResponse{}, nil
}

func (stubExecutionService) GetSession(
	context.Context,
	*execpb.GetSessionRequest,
) (*execpb.GetSessionResponse, error) {
	return &execpb.GetSessionResponse{}, nil
}

func (stubExecutionService) WatchSession(
	context.Context,
	*execpb.WatchSessionRequest,
) (execution.WatchStream, error) {
	return stubWatchStream{}, nil
}

func (stubExecutionService) WriteSessionStdin(
	context.Context,
	*execpb.WriteSessionStdinRequest,
) (*execpb.WriteSessionStdinResponse, error) {
	return &execpb.WriteSessionStdinResponse{}, nil
}

func (stubExecutionService) TerminateSession(
	context.Context,
	*execpb.TerminateSessionRequest,
) (*execpb.TerminateSessionResponse, error) {
	return &execpb.TerminateSessionResponse{}, nil
}

func (stubExecutionService) Close() error {
	return nil
}

type stubWatchStream struct{}

func (stubWatchStream) Recv() (*execpb.WatchSessionResponse, error) {
	return nil, io.EOF
}

type stubFileExecutor struct{}

func (stubFileExecutor) ReadFile(context.Context, string, int, int) (fileops.ReadResult, error) {
	return fileops.ReadResult{}, nil
}

func (stubFileExecutor) WriteFile(context.Context, string, string) (fileops.WriteResult, error) {
	return fileops.WriteResult{}, nil
}

func (stubFileExecutor) EditFile(
	context.Context,
	string,
	string,
	string,
) (fileops.EditResult, error) {
	return fileops.EditResult{}, nil
}

func (stubFileExecutor) ApplyPatch(context.Context, string) (fileops.ApplyPatchResult, error) {
	return fileops.ApplyPatchResult{}, nil
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
