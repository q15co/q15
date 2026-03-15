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
)

func TestComposeSystemPromptIncludesRuntimeAndExecGuidance(t *testing.T) {
	info := runtimeEnvironmentInfo{
		WorkspaceDir:        "/workspace",
		MemoryDir:           "/memory",
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
		"- Shared skills root: /skills",
		"- Command runtime: exec-service sessions via local-nix-shell",
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
	toolList, err := buildToolList(&stubExecutionService{}, fileExec, nil, "")
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
		"web_fetch",
	}; !equalStrings(got, want) {
		t.Fatalf("tool names = %v, want %v", got, want)
	}
}

func TestBuildToolListAppendsWebSearchWhenConfigured(t *testing.T) {
	t.Parallel()

	fileExec := &stubFileExecutor{}
	toolList, err := buildToolList(&stubExecutionService{}, fileExec, nil, "brave-key")
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
	return &execpb.GetRuntimeInfoResponse{ExecutorType: "local-shell"}, nil
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
