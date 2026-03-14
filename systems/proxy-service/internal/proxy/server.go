package proxy

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/elazarl/goproxy"
)

const defaultListenAddr = "0.0.0.0:0"

// Config configures the proxy listener, CA state, and request rules.
type Config struct {
	ListenAddr   string
	StateDir     string
	SecretValues map[string]string
	Rules        []Rule
}

// Server owns the running MITM proxy and its persisted CA bundle.
type Server struct {
	mu       sync.Mutex
	listener net.Listener
	server   *http.Server
	errCh    chan error
	ca       *generatedCA
	stopOnce sync.Once
	stopErr  error

	compiledRules []compiledRule
	secretValues  map[string]string
	mitmAction    *goproxy.ConnectAction
}

// Start launches the proxy and shuts it down when ctx is canceled.
func Start(ctx context.Context, cfg Config) (*Server, error) {
	listenAddr := strings.TrimSpace(cfg.ListenAddr)
	if listenAddr == "" {
		listenAddr = defaultListenAddr
	}

	ca, err := loadOrCreateCA(cfg.StateDir)
	if err != nil {
		return nil, fmt.Errorf("load proxy CA: %w", err)
	}
	compiledRules, err := compileRules(cfg.Rules, cfg.SecretValues)
	if err != nil {
		return nil, fmt.Errorf("compile proxy rules: %w", err)
	}

	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
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
		listener:      listener,
		server:        httpServer,
		errCh:         make(chan error, 1),
		ca:            ca,
		compiledRules: compiledRules,
		secretValues:  maps.Clone(cfg.SecretValues),
		mitmAction: &goproxy.ConnectAction{
			Action:    goproxy.ConnectMitm,
			TLSConfig: goproxy.TLSConfigFromCA(&ca.TLSCert),
		},
	}
	proxy.OnRequest().HandleConnectFunc(s.handleConnect)
	proxy.OnRequest().DoFunc(s.handleRequest)
	proxy.OnResponse().DoFunc(s.handleResponse)

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

// Stop gracefully shuts down the proxy server.
func (s *Server) Stop(ctx context.Context) error {
	if s == nil {
		return nil
	}

	s.stopOnce.Do(func() {
		s.mu.Lock()
		server := s.server
		s.mu.Unlock()

		if server != nil {
			if err := server.Shutdown(ctx); err != nil {
				s.stopErr = err
			}
		}
	})
	return s.stopErr
}

// Addr returns the listener address for the running proxy.
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

// Errors returns asynchronous serve errors from the proxy.
func (s *Server) Errors() <-chan error {
	if s == nil {
		return nil
	}
	return s.errCh
}

// CACertHostPath returns the host path of the exported proxy CA certificate.
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

// CACertPEM returns the proxy CA certificate PEM bytes.
func (s *Server) CACertPEM() []byte {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ca == nil || len(s.ca.CertPEM) == 0 {
		return nil
	}
	return append([]byte(nil), s.ca.CertPEM...)
}

// ProxyURLForContainerHost builds the proxy URL reachable from the sandbox host alias.
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

func (s *Server) handleConnect(host string, _ *goproxy.ProxyCtx) (*goproxy.ConnectAction, string) {
	if s.shouldMITMHost(host) {
		return s.mitmAction, host
	}
	return goproxy.OkConnect, host
}

func (s *Server) shouldMITMHost(host string) bool {
	for _, rule := range s.compiledRules {
		if rule.matchesConnectHost(host) {
			return true
		}
	}
	return false
}

func (s *Server) handleRequest(
	req *http.Request,
	_ *goproxy.ProxyCtx,
) (*http.Request, *http.Response) {
	if req == nil {
		return nil, nil
	}
	if req.Method == http.MethodConnect {
		return req, nil
	}

	for _, rule := range s.compiledRules {
		if !rule.matchesRequest(req) {
			continue
		}
		if err := rule.apply(req, s.secretValues); err != nil {
			return req, goproxy.NewResponse(
				req,
				goproxy.ContentTypeText,
				http.StatusBadGateway,
				fmt.Sprintf("proxy rule %q header render failed", displayRuleName(rule)),
			)
		}
	}
	return req, nil
}

func (s *Server) handleResponse(resp *http.Response, _ *goproxy.ProxyCtx) *http.Response {
	if resp == nil {
		return nil
	}
	for _, key := range []string{"Authorization", "Proxy-Authorization", "X-Api-Key"} {
		resp.Header.Del(key)
	}
	return resp
}

func displayRuleName(r compiledRule) string {
	if strings.TrimSpace(r.name) != "" {
		return r.name
	}
	return "<unnamed>"
}
