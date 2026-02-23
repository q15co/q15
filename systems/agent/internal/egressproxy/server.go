package egressproxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/elazarl/goproxy"
)

const defaultListenAddr = "0.0.0.0:0"

type Config struct {
	ListenAddr string
}

type Server struct {
	mu       sync.Mutex
	listener net.Listener
	server   *http.Server
	errCh    chan error
	ca       *generatedCA
	stopOnce sync.Once
	stopErr  error
}

func Start(ctx context.Context, cfg Config) (*Server, error) {
	listenAddr := strings.TrimSpace(cfg.ListenAddr)
	if listenAddr == "" {
		listenAddr = defaultListenAddr
	}

	ca, err := createExportedCA()
	if err != nil {
		return nil, fmt.Errorf("create exported CA: %w", err)
	}

	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		_ = ca.cleanup()
		return nil, fmt.Errorf("listen egress proxy on %q: %w", listenAddr, err)
	}

	proxy := goproxy.NewProxyHttpServer()
	proxy.Verbose = false

	httpServer := &http.Server{
		Handler:      proxy,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	s := &Server{
		listener: listener,
		server:   httpServer,
		errCh:    make(chan error, 1),
		ca:       ca,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.Stop(shutdownCtx)
	}()

	go func() {
		defer close(s.errCh)
		err := httpServer.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			select {
			case s.errCh <- err:
			default:
			}
		}
	}()

	return s, nil
}

func (s *Server) Stop(ctx context.Context) error {
	if s == nil {
		return nil
	}

	s.stopOnce.Do(func() {
		s.mu.Lock()
		server := s.server
		ca := s.ca
		s.mu.Unlock()

		if server != nil {
			if err := server.Shutdown(ctx); err != nil {
				s.stopErr = err
			}
		}
		if ca != nil {
			if err := ca.cleanup(); err != nil && s.stopErr == nil &&
				!errors.Is(err, os.ErrNotExist) {
				s.stopErr = err
			}
		}
	})
	return s.stopErr
}

func (s *Server) Addr() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

func (s *Server) Errors() <-chan error {
	if s == nil {
		return nil
	}
	return s.errCh
}

func (s *Server) CACertHostPath() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ca == nil {
		return ""
	}
	return s.ca.CertHostPath
}

func (s *Server) ProxyURLForContainerHost(containerHost string) (string, error) {
	if s == nil {
		return "", errors.New("egress proxy server is nil")
	}
	containerHost = strings.TrimSpace(containerHost)
	if containerHost == "" {
		return "", errors.New("container host is required")
	}

	addr := s.Addr()
	if addr == "" {
		return "", errors.New("egress proxy server is not listening")
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("parse listener address %q: %w", addr, err)
	}
	return "http://" + net.JoinHostPort(containerHost, port), nil
}
