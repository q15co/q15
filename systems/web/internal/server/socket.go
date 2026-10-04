package server

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/q15co/q15/libs/chat-contract/chatpb"
	"github.com/q15co/q15/systems/web/internal/gate"
)

// HeartbeatInterval stays well below the ingress proxy's socket idle cutoff.
const HeartbeatInterval = 25 * time.Second

const (
	helloTimeout   = 10 * time.Second
	writeTimeout   = 10 * time.Second
	maxInputBytes  = 128 << 10
	maxQueueBytes  = 8 << 20
	maxQueueFrames = 2048 // replay can carry 500 complete turns with several messages
)

type socketConn struct {
	conn        *websocket.Conn
	ctx         context.Context
	cancel      context.CancelFunc
	queue       chan []byte
	queuedBytes atomic.Int64
	valid       func() bool
}

func (c *socketConn) enqueue(data []byte) bool {
	if c.ctx.Err() != nil || (c.valid != nil && !c.valid()) {
		return false
	}
	if c.queuedBytes.Add(int64(len(data))) > maxQueueBytes {
		c.queuedBytes.Add(-int64(len(data)))
		return false
	}
	select {
	case c.queue <- data:
		return true
	default:
		c.queuedBytes.Add(-int64(len(data)))
		return false
	}
}

func (c *socketConn) stop() { c.cancel() }

func (c *socketConn) write() {
	defer c.cancel()
	defer func() { _ = c.conn.CloseNow() }()
	ticker := time.NewTicker(HeartbeatInterval)
	defer ticker.Stop()
	authTicker := time.NewTicker(time.Second)
	defer authTicker.Stop()
	for {
		if c.valid != nil && !c.valid() {
			_ = c.conn.Close(websocket.StatusCode(4401), "session expired or revoked")
			return
		}
		select {
		case <-c.ctx.Done():
			return
		case <-authTicker.C:
			continue
		case data := <-c.queue:
			if c.valid != nil && !c.valid() {
				_ = c.conn.Close(websocket.StatusCode(4401), "session expired or revoked")
				return
			}
			c.queuedBytes.Add(-int64(len(data)))
			ctx, cancel := context.WithTimeout(c.ctx, writeTimeout)
			err := c.conn.Write(ctx, websocket.MessageText, data)
			cancel()
			if err != nil {
				return
			}
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(c.ctx, writeTimeout)
			err := c.conn.Ping(ctx)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

func (s *Server) socket(w http.ResponseWriter, r *http.Request) {
	if !gate.CheckOrigin(r, s.config.Origin) {
		writeError(w, http.StatusForbidden, "invalid_origin")
		return
	}
	principal := gate.PrincipalFrom(r.Context())
	if principal.ID == "" || len(principal.Binding) != 43 {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	// Exact origin + fetch metadata was checked above; Accept's additional check
	// is host-pattern based and would be wrong behind a Host-rewriting ingress.
	conn, err := websocket.Accept(
		w,
		r,
		&websocket.AcceptOptions{InsecureSkipVerify: true, Subprotocols: []string{"q15-auth"}},
	)
	if err != nil {
		return
	}
	conn.SetReadLimit(maxInputBytes)
	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	stream, err := s.service.BrowserChannel(ctx)
	if err != nil {
		_ = conn.CloseNow()
		return
	}
	if err := stream.Send(&chatpb.BrowserPacket{Principal: principal.ID, Binding: principal.Binding}); err != nil {
		_ = conn.CloseNow()
		return
	}
	c := &socketConn{conn: conn, ctx: ctx, cancel: cancel, queue: make(chan []byte, maxQueueFrames)}
	if checker, ok := s.config.Authorizer.(gate.SessionChecker); ok {
		c.valid = func() bool { return checker.SessionValid(r.Context()) }
	}
	done := make(chan struct{})
	go func() { defer close(done); c.write() }()
	defer func() { cancel(); <-done }()
	if !s.registry.add(principal.ID, c) {
		return
	}
	defer s.registry.remove(principal.ID, c)
	if !s.start(func() {
		for {
			packet, err := stream.Recv()
			if err != nil || !c.enqueue(packet.GetFrame()) {
				c.stop()
				return
			}
		}
	}) {
		return
	}
	first := true
	for {
		readCtx := ctx
		readCancel := func() {}
		if first {
			readCtx, readCancel = context.WithTimeout(ctx, helloTimeout)
		}
		kind, data, err := conn.Read(readCtx)
		readCancel()
		if err != nil {
			return
		}
		if c.valid != nil && !c.valid() {
			_ = conn.Close(websocket.StatusCode(4401), "session expired or revoked")
			return
		}
		if kind != websocket.MessageText || !json.Valid(data) {
			_ = conn.Close(websocket.StatusUnsupportedData, "invalid frame")
			return
		}
		first = false
		if err := stream.Send(&chatpb.BrowserPacket{Frame: data}); err != nil {
			return
		}
	}
}
