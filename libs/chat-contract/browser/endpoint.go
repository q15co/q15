// Package browser adapts the agent chat service to sealed browser transport.
package browser

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/q15co/q15/libs/chat-contract/chatpb"
)

const rpcTimeout = 5 * time.Second

// Backend stays on the agent side of the content boundary.
type backend interface {
	OpenSession(context.Context, *chatpb.OpenSessionRequest) (*chatpb.OpenSessionResponse, error)
	SendMessage(context.Context, *chatpb.SendMessageRequest) (*chatpb.SendMessageResponse, error)
	Abort(context.Context, *chatpb.AbortRequest) (*chatpb.AbortResponse, error)
	WatchEvents(context.Context, *chatpb.WatchEventsRequest) (watchStream, error)
	ListTurns(context.Context, *chatpb.ListTurnsRequest) (*chatpb.ListTurnsResponse, error)
	Deliver(context.Context) (deliverStream, error)
}

// Endpoint keeps content keys, projections and drafts on the agent side of the relay.
type Endpoint struct {
	ctx        context.Context
	cancel     context.CancelFunc
	service    backend
	registry   *registry
	sessionsMu sync.Mutex
	sessions   map[string]*session
	channelsMu sync.Mutex
	channels   map[string]*socketConn
	wg         sync.WaitGroup
	workMu     sync.Mutex
	closing    bool
	once       sync.Once
}

func newEndpoint(service backend) *Endpoint {
	ctx, cancel := context.WithCancel(context.Background())
	return &Endpoint{
		ctx:      ctx,
		cancel:   cancel,
		service:  service,
		registry: newRegistry(),
		sessions: make(map[string]*session),
		channels: make(map[string]*socketConn),
	}
}

// Close cancels fan-out and watchers that can outlive individual browser sockets.
func (s *Endpoint) Close() {
	s.workMu.Lock()
	s.closing = true
	s.cancel()
	s.workMu.Unlock()
	s.registry.close()
	s.wg.Wait()
}

func (s *Endpoint) start(work func()) bool {
	s.workMu.Lock()
	defer s.workMu.Unlock()
	if s.closing || s.ctx.Err() != nil {
		return false
	}
	s.wg.Add(1)
	go func() { defer s.wg.Done(); work() }()
	return true
}

func decode(data json.RawMessage, value any) bool {
	return len(data) > 0 && string(data) != "null" && json.Unmarshal(data, value) == nil
}

func (s *Endpoint) principalSession(principal string) *session {
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	value := s.sessions[principal]
	if value == nil {
		value = &session{owner: principal, server: s}
		s.sessions[principal] = value
	}
	return value
}
