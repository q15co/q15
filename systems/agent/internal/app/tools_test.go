package app

import (
	"context"
	"io"
	"testing"

	"github.com/q15co/q15/libs/exec-contract/execpb"
	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/embed"
	"github.com/q15co/q15/systems/agent/internal/execution"
	"github.com/q15co/q15/systems/agent/internal/fileops"
	q15media "github.com/q15co/q15/systems/agent/internal/media"
)

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
		nil,
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
		"exec_list",
		"exec_read",
		"exec_write",
		"exec_kill",
		"load_image",
		"attach_audio",
		"attach_image",
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
		nil,
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
		"exec_list",
		"exec_read",
		"exec_write",
		"exec_kill",
		"load_image",
		"attach_audio",
		"attach_image",
		"web_fetch",
		"web_search",
	}; !equalStrings(got, want) {
		t.Fatalf("tool names = %v, want %v", got, want)
	}
}

func TestBuildToolListAppendsEmbeddingToolsWhenConfigured(t *testing.T) {
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
		embed.NewService(embed.Settings{}, nil, nil, nil),
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
		"exec_list",
		"exec_read",
		"exec_write",
		"exec_kill",
		"load_image",
		"attach_audio",
		"attach_image",
		"web_fetch",
		"embed_sources",
		"embed_sync",
		"embed_search",
		"embed_status",
	}; !equalStrings(got, want) {
		t.Fatalf("tool names = %v, want %v", got, want)
	}
}

// --- tool test doubles ---

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

func (stubExecutionService) ListSessions(
	context.Context,
	*execpb.ListSessionsRequest,
) (*execpb.ListSessionsResponse, error) {
	return &execpb.ListSessionsResponse{}, nil
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
