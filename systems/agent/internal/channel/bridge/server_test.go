package bridge

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func TestResolveListenTargetKeepsExecConvention(t *testing.T) {
	for _, tc := range []struct {
		target  string
		network string
		address string
	}{
		{"unix:///run/q15/bridge.sock", "unix", "/run/q15/bridge.sock"},
		{"127.0.0.1:50051", "tcp", "127.0.0.1:50051"},
		{":50051", "tcp", ":50051"},
	} {
		network, address := resolveListenTarget(tc.target)
		if network != tc.network || address != tc.address {
			t.Fatalf(
				"resolveListenTarget(%q) = (%q %q), want (%q %q)",
				tc.target,
				network,
				address,
				tc.network,
				tc.address,
			)
		}
	}
}

func TestServerServeStopsWithIdleStreamingRPC(t *testing.T) {
	listener := bufconn.Listen(1024 * 1024)
	started := make(chan struct{})
	stopped := make(chan struct{})
	grpcServer := grpc.NewServer(
		grpc.UnknownServiceHandler(func(_ any, stream grpc.ServerStream) error {
			close(started)
			<-stream.Context().Done()
			close(stopped)
			return nil
		}),
	)
	server := &Server{listener: listener, server: grpcServer}
	t.Cleanup(server.Close)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	conn, err := grpc.NewClient(
		"passthrough:///bridge-test",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return listener.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	rpcCtx, rpcCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer rpcCancel()
	if _, err := conn.NewStream(rpcCtx, &grpc.StreamDesc{ServerStreams: true}, "/test.Stream/Watch"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-rpcCtx.Done():
		t.Fatal("streaming handler did not start")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not stop while an idle stream was connected")
	}
	select {
	case <-stopped:
	case <-rpcCtx.Done():
		t.Fatal("shutdown did not cancel the streaming handler")
	}
}

// canChgrpSocket reports whether this process may chgrp to socketGroupID:
// only root or a process already in that group can. Callers skip rather than
// fail where the platform will not permit the chgrp.
func canChgrpSocket() bool {
	if os.Geteuid() == 0 {
		return true
	}
	groups, err := os.Getgroups()
	if err != nil {
		return false
	}
	for _, gid := range groups {
		if gid == socketGroupID {
			return true
		}
	}
	return false
}

func TestNewServerUnlinksStaleSocketAndPreparesModeGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the bridge socket is a unix-domain path")
	}
	if !canChgrpSocket() {
		t.Skipf("chgrp to group %d requires root or group membership", socketGroupID)
	}

	dir := t.TempDir()
	address := filepath.Join(dir, "bridge.sock")
	// A crashed previous run leaves its socket file behind; the bind must
	// replace it rather than fail.
	if err := os.WriteFile(address, []byte("stale"), 0o600); err != nil {
		t.Fatalf("write stale socket file: %v", err)
	}

	server, err := NewServer("unix://"+address, NewService(&fakeTurnLister{}, &AgentEndpoint{}))
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	info, err := os.Stat(address)
	if err != nil {
		t.Fatalf("stat socket %q: %v", address, err)
	}
	if got := info.Mode().Perm(); got != socketFileMode {
		t.Fatalf("socket mode = %v, want %v", got, socketFileMode)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		server.Close()
		t.Skipf("os.Stat().Sys() is not a syscall.Stat_t on %s", runtime.GOOS)
	}
	if stat.Gid != socketGroupID {
		t.Fatalf("socket gid = %d, want %d", stat.Gid, socketGroupID)
	}
	server.Close()

	// Shutdown may leave the socket file behind; the next bind must unlink it
	// rather than fail on startup.
	restarted, err := NewServer("unix://"+address, NewService(&fakeTurnLister{}, &AgentEndpoint{}))
	if err != nil {
		t.Fatalf("NewServer() after Close() error = %v", err)
	}
	restarted.Close()
}

func TestNewServerSurfacesBindFailure(t *testing.T) {
	// A configured target whose path cannot exist must fail loudly with the
	// path named, never silently.
	target := "unix://" + filepath.Join(t.TempDir(), "missing-dir", "bridge.sock")

	_, err := NewServer(target, NewService(&fakeTurnLister{}, &AgentEndpoint{}))
	if err == nil {
		t.Fatalf("NewServer(%q) error = nil, want bind failure", target)
	}
	if !strings.Contains(err.Error(), filepath.Join("missing-dir", "bridge.sock")) {
		t.Fatalf("NewServer() error = %v, want the socket path named", err)
	}
}

func TestServerServeStopsOnContextCancellation(t *testing.T) {
	// TCP keeps this test away from socket permissions entirely.
	server, err := NewServer("127.0.0.1:0", NewService(&fakeTurnLister{}, &AgentEndpoint{}))
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	t.Cleanup(server.Close)

	ctx, cancel := context.WithCancel(context.Background())
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.Serve(ctx)
	}()
	cancel()
	if err := <-serveErr; err != nil {
		t.Fatalf("Serve() after cancellation error = %v, want nil", err)
	}
}
