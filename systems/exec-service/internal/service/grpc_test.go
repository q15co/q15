package service

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"

	"github.com/q15co/q15/libs/exec-contract/execpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func TestGRPCServerRunsSessionsEndToEnd(t *testing.T) {
	t.Parallel()

	manager, err := NewManager(ManagerConfig{
		DefaultWorkingDir: t.TempDir(),
		Executor:          ShellExecutor{},
	})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	api, err := NewGRPCServer(manager, RuntimeInfo{
		ServiceVersion:      "test",
		ExecutorType:        "local-shell",
		WorkspaceDir:        "/workspace",
		MemoryDir:           "/memory",
		SkillsDir:           "/skills",
		ProxyEnabled:        true,
		ProxyPolicyRevision: "rev-123",
	})
	if err != nil {
		t.Fatalf("NewGRPCServer() error = %v", err)
	}

	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	api.Register(server)
	go func() {
		_ = server.Serve(listener)
	}()
	defer server.Stop()

	ctx := context.Background()
	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return listener.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient() error = %v", err)
	}
	defer conn.Close()

	client := execpb.NewExecutionServiceClient(conn)
	info, err := client.GetRuntimeInfo(ctx, &execpb.GetRuntimeInfoRequest{})
	if err != nil {
		t.Fatalf("GetRuntimeInfo() error = %v", err)
	}
	if got := info.GetExecutorType(); got != "local-shell" {
		t.Fatalf("executor type = %q, want local-shell", got)
	}
	if !info.GetProxyEnabled() {
		t.Fatalf("expected proxy_enabled=true")
	}
	if got := info.GetProxyPolicyRevision(); got != "rev-123" {
		t.Fatalf("proxy policy revision = %q, want rev-123", got)
	}

	started, err := client.StartSession(ctx, &execpb.StartSessionRequest{
		Command:  "printf alpha; printf beta >&2",
		Packages: []string{"ignored"},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	watch, err := client.WatchSession(ctx, &execpb.WatchSessionRequest{
		SessionId: started.GetSession().GetSessionId(),
	})
	if err != nil {
		t.Fatalf("WatchSession() error = %v", err)
	}

	var sawStdout bool
	var sawStderr bool
	var sawExit bool
	var seen []string
	for {
		resp, err := watch.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("watch.Recv() error = %v", err)
		}
		if resp.GetEvent().GetStdout() != nil {
			sawStdout = true
			seen = append(seen, "stdout")
		}
		if resp.GetEvent().GetStderr() != nil {
			sawStderr = true
			seen = append(seen, "stderr")
		}
		if resp.GetEvent().GetExited() != nil {
			sawExit = true
			seen = append(seen, "exited")
		}
		if resp.GetEvent().GetStarted() != nil {
			seen = append(seen, "started")
		}
		if resp.GetEvent().GetStdinClosed() != nil {
			seen = append(seen, "stdin_closed")
		}
	}

	if !sawStdout || !sawStderr || !sawExit {
		t.Fatalf(
			"watch stream missing expected events: stdout=%t stderr=%t exit=%t seen=%v",
			sawStdout,
			sawStderr,
			sawExit,
			seen,
		)
	}
}

func TestGRPCServerSupportsInteractiveStdin(t *testing.T) {
	t.Parallel()

	manager, err := NewManager(ManagerConfig{
		DefaultWorkingDir: t.TempDir(),
		Executor:          ShellExecutor{},
	})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	api, err := NewGRPCServer(manager, RuntimeInfo{
		ServiceVersion: "test",
		ExecutorType:   "local-shell",
		WorkspaceDir:   "/workspace",
		MemoryDir:      "/memory",
		SkillsDir:      "/skills",
	})
	if err != nil {
		t.Fatalf("NewGRPCServer() error = %v", err)
	}

	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	api.Register(server)
	go func() {
		_ = server.Serve(listener)
	}()
	defer server.Stop()

	ctx := context.Background()
	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return listener.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient() error = %v", err)
	}
	defer conn.Close()

	client := execpb.NewExecutionServiceClient(conn)
	started, err := client.StartSession(ctx, &execpb.StartSessionRequest{
		Command:       "cat",
		Packages:      []string{"ignored"},
		KeepStdinOpen: true,
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	if _, err := client.WriteSessionStdin(ctx, &execpb.WriteSessionStdinRequest{
		SessionId:  started.GetSession().GetSessionId(),
		Data:       []byte("hello\n"),
		CloseStdin: true,
	}); err != nil {
		t.Fatalf("WriteSessionStdin() error = %v", err)
	}

	watch, err := client.WatchSession(ctx, &execpb.WatchSessionRequest{
		SessionId: started.GetSession().GetSessionId(),
	})
	if err != nil {
		t.Fatalf("WatchSession() error = %v", err)
	}

	var stdout string
	for {
		resp, err := watch.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("watch.Recv() error = %v", err)
		}
		if chunk := resp.GetEvent().GetStdout(); chunk != nil {
			stdout += string(chunk.GetData())
		}
	}

	if stdout != "hello\n" {
		t.Fatalf("stdout = %q, want %q", stdout, "hello\n")
	}
}
