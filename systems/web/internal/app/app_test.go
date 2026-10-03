package app

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/q15co/q15/libs/chat-contract/chatpb"
	"google.golang.org/grpc"
)

type runtimeServer struct {
	chatpb.UnimplementedChatServiceServer
	version int32
}

func (s *runtimeServer) GetRuntimeInfo(
	context.Context,
	*chatpb.GetRuntimeInfoRequest,
) (*chatpb.GetRuntimeInfoResponse, error) {
	return &chatpb.GetRuntimeInfoResponse{ProtocolVersion: s.version}, nil
}

func TestStartupHandshake(t *testing.T) {
	for _, version := range []int32{chatpb.ProtocolVersion, 0, chatpb.ProtocolVersion + 1, -42} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			socket := filepath.Join(t.TempDir(), "bridge.sock")
			listener, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			rpc := grpc.NewServer()
			chatpb.RegisterChatServiceServer(rpc, &runtimeServer{version: version})
			go func() { _ = rpc.Serve(listener) }()
			defer rpc.Stop()
			client, err := connectBridge(context.Background(), "unix://"+socket)
			if version == chatpb.ProtocolVersion {
				if err != nil {
					t.Fatal(err)
				}
				_ = client.Close()
			} else if err == nil || !strings.Contains(err.Error(), "protocol mismatch") {
				t.Fatalf("version %d: %v", version, err)
			}
		})
	}
}

func TestRunRejectsArguments(t *testing.T) {
	if err := Run([]string{"--anything"}); err == nil {
		t.Fatal("accepted args")
	}
}

func TestHealthcheck(t *testing.T) {
	for _, code := range []int{http.StatusOK, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/healthz" {
					t.Errorf("health path %q", r.URL.Path)
				}
				w.WriteHeader(code)
			}))
			defer s.Close()
			if err := healthcheck(strings.TrimPrefix(s.URL, "http://")); (err == nil) != (code == http.StatusOK) {
				t.Fatalf("healthcheck = %v", err)
			}
		})
	}
}
