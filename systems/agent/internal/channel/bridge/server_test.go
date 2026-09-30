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

// TestResolveListenTargetOnlyAcceptsUnix pins the bridge's own rule: a
// unix:// target resolves to its socket path and nothing else is accepted,
// because the bridge is the identity surface and grpc.NewServer sets no
// credentials and no interceptor.
func TestResolveListenTargetOnlyAcceptsUnix(t *testing.T) {
	for _, tc := range []struct {
		target  string
		address string
	}{
		{"unix:///run/q15/bridge.sock", "/run/q15/bridge.sock"},
		{"unix://bridge.sock", "bridge.sock"},
	} {
		network, address, err := resolveListenTarget(tc.target)
		if err != nil {
			t.Fatalf("resolveListenTarget(%q) error = %v, want a unix socket", tc.target, err)
		}
		if network != "unix" || address != tc.address {
			t.Fatalf(
				"resolveListenTarget(%q) = (%q %q), want (unix %q)",
				tc.target,
				network,
				address,
				tc.address,
			)
		}
	}

	// The exec resolver's tcp fallthrough is refused here: every rejected
	// form returns an error carrying the offending value, so a bad config
	// names its own blame.
	for _, target := range []string{"127.0.0.1:50051", ":50053", "/run/q15/bridge.sock"} {
		network, address, err := resolveListenTarget(target)
		if err == nil {
			t.Fatalf(
				"resolveListenTarget(%q) = (%q %q), want the target refused",
				target,
				network,
				address,
			)
		}
		if !strings.Contains(err.Error(), target) {
			t.Fatalf("resolveListenTarget(%q) error = %v, want the target named", target, err)
		}
	}
}

// TestNewServerRejectsNonUnixTargets pins the refusal at the bind: a
// host:port target fails startup with the value named, rather than serving
// seven rpcs in the clear with no credentials and no interceptor.
func TestNewServerRejectsNonUnixTargets(t *testing.T) {
	for _, target := range []string{"127.0.0.1:50051", ":50053"} {
		server, err := NewServer(target, NewService(&fakeTurnLister{}, &AgentEndpoint{}))
		if err == nil {
			server.Close()
			t.Fatalf("NewServer(%q) error = nil, want the tcp target refused", target)
		}
		if !strings.Contains(err.Error(), target) {
			t.Fatalf("NewServer(%q) error = %v, want the offending target named", target, err)
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
	// The socket lives only in this test's temp dir, so the test needs the
	// same chgrp permission the mode/group test does; unix-only is the
	// server's rule, so this test no longer leans on a tcp listener to keep
	// away from socket permissions.
	if runtime.GOOS == "windows" {
		t.Skip("the bridge socket is a unix-domain path")
	}
	if !canChgrpSocket() {
		t.Skipf("chgrp to group %d requires root or group membership", socketGroupID)
	}
	address := filepath.Join(t.TempDir(), "bridge.sock")
	server, err := NewServer("unix://"+address, NewService(&fakeTurnLister{}, &AgentEndpoint{}))
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
