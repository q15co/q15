package bridge

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/q15co/q15/libs/chat-contract/chatpb"
	"google.golang.org/grpc"
)

const (
	// socketFileMode is the mode the bridge socket is chmod'ed to after
	// binding: owner and group may connect, others may not.
	//
	// The mode is intent rather than a security boundary: this repo's images
	// run as root with no USER directive and no group setup, so the chmod and
	// the chgrp below carry no real protection. The boundary that actually
	// holds is which containers mount the socket's volume.
	socketFileMode = 0o660
	// socketGroupID is the numeric group the socket is chgrp'ed to, the group
	// a future web tier container joins to reach the socket without owning
	// it. It is numeric because the images run as root and set up no named
	// groups.
	socketGroupID = 1000
)

// Server owns the bridge's listener and its gRPC server. Binding happens in
// the constructor so a deployment that asks for the listener fails to start
// rather than coming up half-serviced; Serve blocks until shutdown.
type Server struct {
	listener net.Listener
	server   *grpc.Server
}

// NewServer resolves the listen target, unlinks a stale unix socket so a
// crashed previous run does not block startup, binds, applies the socket's
// mode and group, and registers the service. It does not serve.
func NewServer(target string, service *Service) (*Server, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, errors.New("listen target is required")
	}
	if service == nil {
		return nil, errors.New("bridge service is required")
	}

	network, address := resolveListenTarget(target)
	if network == "unix" {
		_ = os.Remove(address)
	}
	listener, err := net.Listen(network, address)
	if err != nil {
		return nil, fmt.Errorf("listen %s %s: %w", network, address, err)
	}
	if network == "unix" {
		if err := prepareSocket(listener); err != nil {
			_ = listener.Close()
			return nil, err
		}
	}

	server := grpc.NewServer()
	chatpb.RegisterChatServiceServer(server, service)
	return &Server{listener: listener, server: server}, nil
}

// Serve blocks serving the chat contract until ctx is canceled, then stops the
// gRPC server, or until the listener fails. ctx is owned by the caller's
// runtime worker loop.
func (s *Server) Serve(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		errCh <- s.server.Serve(s.listener)
	}()

	select {
	case <-ctx.Done():
		s.server.GracefulStop()
		return nil
	case err := <-errCh:
		return err
	}
}

// Close stops the gRPC server and closes the listener without waiting. It is
// the hard shutdown path for callers whose Serve already ended or never ran,
// e.g. when a sibling startup step failed first.
func (s *Server) Close() {
	s.server.Stop()
	_ = s.listener.Close()
}

// prepareSocket applies the freshly bound unix socket's mode and group so the
// web tier can connect once the shared socket volume exists.
func prepareSocket(listener net.Listener) error {
	address := listener.Addr().String()
	if err := os.Chmod(address, socketFileMode); err != nil {
		return fmt.Errorf("chmod bridge socket %q: %w", address, err)
	}
	if err := os.Chown(address, -1, socketGroupID); err != nil {
		return fmt.Errorf("chgrp bridge socket %q: %w", address, err)
	}
	return nil
}

// resolveListenTarget mirrors systems/exec/internal/app/app.go so both
// runtimes keep one listen-target convention: a unix:// prefix selects a unix
// socket and anything else is a TCP address.
func resolveListenTarget(value string) (string, string) {
	if strings.HasPrefix(value, "unix://") {
		return "unix", strings.TrimPrefix(value, "unix://")
	}
	return "tcp", value
}
