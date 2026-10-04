// Package server serves the browser socket, history API and app on one origin.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/q15co/q15/libs/chat-contract/chatpb"
	"github.com/q15co/q15/systems/web/internal/bridge"
	"github.com/q15co/q15/systems/web/internal/gate"
)

const rpcTimeout = 5 * time.Second

// Config provides the HTTP policy and the replaceable authorization seam.
type Config struct {
	Origin     string
	Authorizer gate.Authorizer
	Assets     http.Handler
}

// Server owns only authorization, bounded relay connections and HTTP policy.
// Content keys, replay and drafts remain in the agent.
type Server struct {
	ctx      context.Context
	cancel   context.CancelFunc
	service  bridge.Service
	config   Config
	handler  http.Handler
	registry *registry
	wg       sync.WaitGroup
	workMu   sync.Mutex
	closing  bool
}

// New wires every route through the gate except the exact health endpoint.
func New(ctx context.Context, service bridge.Service, config Config) (*Server, error) {
	if err := gate.ValidateOrigin(config.Origin); err != nil {
		return nil, err
	}
	if service == nil || config.Authorizer == nil || config.Assets == nil {
		return nil, fmt.Errorf("bridge, authorizer and assets are required")
	}
	ctx, cancel := context.WithCancel(ctx)
	s := &Server{ctx: ctx, cancel: cancel, service: service, config: config,
		registry: newRegistry()}
	protected := http.NewServeMux()
	protected.HandleFunc("GET /ws", s.socket)
	protected.HandleFunc("GET /api/turns", s.history)
	protected.Handle("/", config.Assets)
	secured := config.Authorizer.RequireScope("chat", protected)
	s.handler = gate.Headers(
		config.Origin,
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/healthz" {
				w.Header().Set("Cache-Control", "no-store")
				if r.Method != http.MethodGet && r.Method != http.MethodHead {
					w.Header().Set("Allow", "GET, HEAD")
					http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
					return
				}
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				if r.Method != http.MethodHead {
					_, _ = w.Write([]byte("ok\n"))
				}
				return
			}
			secured.ServeHTTP(w, r)
		}),
	)
	return s, nil
}

// ServeHTTP applies the shared policy to every response.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

// Close stops all socket and bridge work, including hijacked HTTP connections.
func (s *Server) Close() {
	s.workMu.Lock()
	s.closing = true
	s.cancel()
	s.workMu.Unlock()
	s.registry.close()
	s.wg.Wait()
}

func (s *Server) start(work func()) bool {
	s.workMu.Lock()
	defer s.workMu.Unlock()
	if s.closing || s.ctx.Err() != nil {
		return false
	}
	s.wg.Add(1)
	go func() { defer s.wg.Done(); work() }()
	return true
}

func (s *Server) history(w http.ResponseWriter, r *http.Request) {
	after, err := queryInt(r, "after_seq", 0)
	if err != nil || after < 0 {
		writeError(w, http.StatusBadRequest, "invalid_after_seq")
		return
	}
	limit, err := queryInt(r, "limit", 50)
	if err != nil || limit < 1 || limit > 500 {
		writeError(w, http.StatusBadRequest, "invalid_limit")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), rpcTimeout)
	defer cancel()
	page, err := s.service.BrowserHistory(
		ctx,
		&chatpb.BrowserHistoryRequest{
			AfterSeq:  after,
			Limit:     int32(limit),
			Binding:   gate.PrincipalFrom(r.Context()).Binding,
			ChannelId: r.Header.Get("Q15-Channel"),
		},
	)
	if err != nil {
		writeError(w, http.StatusBadGateway, "bridge_unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(page.GetFrame())
}

func queryInt(r *http.Request, name string, fallback int64) (int64, error) {
	values, ok := r.URL.Query()[name]
	if !ok {
		return fallback, nil
	}
	if len(values) != 1 {
		return 0, fmt.Errorf("duplicate %s", name)
	}
	value, err := strconv.ParseInt(values[0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid %s", name)
	}
	return value, nil
}

func writeError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}
