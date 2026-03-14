package app

import (
	"context"
	"net"
	"testing"

	"github.com/q15co/q15/libs/exec-contract/execpb"
	"github.com/q15co/q15/systems/agent/internal/config"
	"google.golang.org/grpc"
)

type testExecInfoServer struct {
	execpb.UnimplementedExecutionServiceServer
}

func (testExecInfoServer) GetRuntimeInfo(
	context.Context,
	*execpb.GetRuntimeInfoRequest,
) (*execpb.GetRuntimeInfoResponse, error) {
	return &execpb.GetRuntimeInfoResponse{
		ServiceVersion: "test",
		ExecutorType:   "local-shell",
	}, nil
}

func TestConnectExecutionServiceSucceeds(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	server := grpc.NewServer()
	execpb.RegisterExecutionServiceServer(server, testExecInfoServer{})
	go func() {
		_ = server.Serve(listener)
	}()
	defer server.Stop()
	defer listener.Close()

	client, info, err := connectExecutionService(context.Background(), &config.ExecutionRuntime{
		ServiceAddress: listener.Addr().String(),
	})
	if err != nil {
		t.Fatalf("connectExecutionService() error = %v", err)
	}
	defer client.Close()

	if info.GetExecutorType() != "local-shell" {
		t.Fatalf("executor type = %q, want local-shell", info.GetExecutorType())
	}
}

func TestConnectExecutionServiceFailsWhenUnavailable(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	addr := listener.Addr().String()
	listener.Close()

	client, info, err := connectExecutionService(context.Background(), &config.ExecutionRuntime{
		ServiceAddress: addr,
	})
	if err == nil {
		if client != nil {
			_ = client.Close()
		}
		t.Fatalf("expected unavailable execution service error, got info=%#v", info)
	}
}
