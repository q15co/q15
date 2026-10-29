package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/q15co/q15/libs/chat-contract/chatpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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

func TestStartupHandshakeWaitsForBridgeSocket(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "bridge.sock")
	rpc := grpc.NewServer()
	chatpb.RegisterChatServiceServer(rpc, &runtimeServer{version: chatpb.ProtocolVersion})
	defer rpc.Stop()
	started := make(chan error, 1)
	go func() {
		time.Sleep(50 * time.Millisecond)
		listener, err := net.Listen("unix", socket)
		started <- err
		if err == nil {
			_ = rpc.Serve(listener)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := connectBridge(ctx, "unix://"+socket)
	if listenErr := <-started; listenErr != nil {
		t.Fatal(listenErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
}

func TestWaitForBridgeRetriesTransientFailures(t *testing.T) {
	for _, code := range []codes.Code{codes.Unavailable, codes.DeadlineExceeded} {
		t.Run(code.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				calls := 0
				err := waitForBridge(
					context.Background(),
					func(ctx context.Context) (*chatpb.GetRuntimeInfoResponse, error) {
						calls++
						if calls == 1 {
							if code == codes.DeadlineExceeded {
								<-ctx.Done()
							}
							return nil, status.Error(code, "starting")
						}
						return &chatpb.GetRuntimeInfoResponse{
							ProtocolVersion: chatpb.ProtocolVersion,
						}, nil
					},
				)
				if err != nil || calls != 2 {
					t.Fatalf("error=%v, calls=%d", err, calls)
				}
			})
		})
	}
}

func TestWaitForBridgeStopsOnPermanentFailure(t *testing.T) {
	for _, test := range []struct {
		name string
		info *chatpb.GetRuntimeInfoResponse
		err  error
	}{
		{name: "denied", err: status.Error(codes.PermissionDenied, "denied")},
		{name: "protocol mismatch", info: &chatpb.GetRuntimeInfoResponse{ProtocolVersion: -1}},
		{name: "missing metadata"},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			err := waitForBridge(
				context.Background(),
				func(context.Context) (*chatpb.GetRuntimeInfoResponse, error) {
					calls++
					return test.info, test.err
				},
			)
			if err == nil || calls != 1 {
				t.Fatalf("error=%v, calls=%d", err, calls)
			}
		})
	}
}

func TestWaitForBridgeCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		calls := 0
		go func() {
			time.Sleep(100 * time.Millisecond)
			cancel()
		}()
		err := waitForBridge(ctx, func(context.Context) (*chatpb.GetRuntimeInfoResponse, error) {
			calls++
			return nil, status.Error(codes.Unavailable, "starting")
		})
		if !errors.Is(err, context.Canceled) || calls != 1 {
			t.Fatalf("error=%v, calls=%d", err, calls)
		}
	})
}

func TestStartupHandshakeDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := connectBridge(ctx, "unix://"+filepath.Join(t.TempDir(), "missing.sock"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
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
