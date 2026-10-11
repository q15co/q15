package bridge

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"time"

	"github.com/q15co/q15/libs/chat-contract/browser/protocol"
	"github.com/q15co/q15/libs/chat-contract/chatpb"
	"google.golang.org/grpc"
)

const (
	// serverShutdownTimeout leaves time for the runtime's other workers to
	// finish before Compose's stop deadline. Long-lived subscriptions cannot
	// drain by themselves, so graceful shutdown needs a hard stop fallback.
	serverShutdownTimeout = 2 * time.Second
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
	// serverMaxConcurrentStreams caps the grpc server's concurrent streams.
	// Each WatchEvents or Deliver stream is one goroutine plus buffered
	// channels plus, on subscribe, a replay slice of up to
	// sessionEventRetention retained events, and without this option all of
	// that scales with client behaviour instead of with a limit. 128 is
	// generous for one user's browser tabs — a tab holds one WatchEvents
	// stream per open session — and deliberately above anything a real ui
	// opens, so reaching it is an operational event, not everyday flow
	// control.
	serverMaxConcurrentStreams = 128
)

// Server owns the bridge's listener and its gRPC server. Binding happens in
// the constructor so a deployment that asks for the listener fails to start
// rather than coming up half-serviced; Serve blocks until shutdown.
type Server struct {
	listener net.Listener
	server   *grpc.Server
	service  *Service
}

// NewServer resolves the listen target — only a unix:// socket binds, and a
// non-unix target is refused rather than listened on — unlinks a stale unix
// socket so a crashed previous run does not block startup, binds, applies
// the socket's mode and group, and registers the service. It does not serve.
func NewServer(target string, service *Service) (*Server, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, errors.New("listen target is required")
	}
	if service == nil {
		return nil, errors.New("bridge service is required")
	}

	network, address, err := resolveListenTarget(target)
	if err != nil {
		return nil, err
	}
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

	// The grpc server keeps no credentials and no interceptor, so the
	// bound transport is the whole trust story; logging it lets a reader
	// see at startup which transport they got without a socket stat.
	log.Printf(
		"q15: runtime event=bridge_listening network=%s address=%q",
		network,
		address,
	)
	server := grpc.NewServer(
		grpc.MaxConcurrentStreams(serverMaxConcurrentStreams),
		grpc.MaxRecvMsgSize(protocol.MaxMediaWireBytes+1024),
	)
	chatpb.RegisterChatServiceServer(server, service)
	return &Server{listener: listener, server: server, service: service}, nil
}

// Serve blocks serving the chat contract until ctx is canceled, then stops the
// gRPC server, or until the listener fails. ctx is owned by the caller's
// runtime worker loop.
func (s *Server) Serve(ctx context.Context) error {
	sweepCtx, cancelSweep := context.WithCancel(ctx)
	defer cancelSweep()
	if s.service != nil {
		go s.sweepMedia(sweepCtx)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- s.server.Serve(s.listener)
	}()

	select {
	case <-ctx.Done():
		s.stopServing()
		return nil
	case err := <-errCh:
		return err
	}
}

func (s *Server) stopServing() {
	if s.service != nil {
		defer s.service.browser.Close()
	}
	done := make(chan struct{})
	go func() {
		s.server.GracefulStop()
		close(done)
	}()
	timer := time.NewTimer(serverShutdownTimeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		// Stop cancels the RPC contexts that idle streaming handlers wait on.
		// Join GracefulStop here rather than leaving a shutdown goroutine behind.
		s.server.Stop()
		<-done
	}
}

// Close stops the gRPC server and closes the listener without waiting. It is
// the hard shutdown path for callers whose Serve already ended or never ran,
// e.g. when a sibling startup step failed first.
func (s *Server) Close() {
	if s.service != nil {
		s.service.browser.Close()
	}
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

// resolveListenTarget refuses everything but a unix:// socket. It
// deliberately does not mirror systems/exec/internal/app/app.go, whose tcp
// fallthrough exec may keep: exec is port-isolated and holds no identity,
// while this bridge is the identity surface and its grpc server runs without
// credentials or an interceptor, so a tcp bind would publish every rpc in
// the clear on every interface the agent container can route to. The error
// names the target so a misconfigured deployment fails with the offending
// value in hand.
func resolveListenTarget(value string) (string, string, error) {
	if strings.HasPrefix(value, "unix://") {
		return "unix", strings.TrimPrefix(value, "unix://"), nil
	}
	return "", "", fmt.Errorf(
		"listen target %q must use the unix:// scheme; the chat bridge is the "+
			"identity surface, so a host:port tcp listener is refused because the "+
			"grpc server sets no credentials and no interceptor",
		value,
	)
}

func (s *Server) sweepMedia(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		if err := s.service.SweepMedia(ctx, time.Now()); err != nil {
			log.Printf("q15: media sweep failed: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func newRPCServer(service *Service) *grpc.Server {
	server := grpc.NewServer(
		grpc.MaxConcurrentStreams(serverMaxConcurrentStreams),
		grpc.MaxRecvMsgSize(protocol.MaxMediaWireBytes+1024),
	)
	chatpb.RegisterChatServiceServer(server, service)
	return server
}
