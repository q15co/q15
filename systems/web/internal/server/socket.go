package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/q15co/q15/systems/web/internal/gate"
	"github.com/q15co/q15/systems/web/internal/protocol"
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

func (c *socketConn) enqueue(frame protocol.Frame) bool {
	data, err := json.Marshal(frame)
	if err != nil || c.ctx.Err() != nil || (c.valid != nil && !c.valid()) {
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
	principal := gate.PrincipalFrom(r.Context()).ID
	if principal == "" {
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
	device := make([]byte, 16)
	if _, err := rand.Read(device); err != nil {
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
	helloCtx, helloCancel := context.WithTimeout(ctx, helloTimeout)
	defer helloCancel()
	registered := false
	defer func() {
		if registered {
			s.registry.remove(principal, c)
		}
	}()
	for {
		readCtx := ctx
		if !registered {
			readCtx = helloCtx
		}
		kind, data, err := conn.Read(readCtx)
		if err != nil {
			return
		}
		if c.valid != nil && !c.valid() {
			_ = conn.Close(websocket.StatusCode(4401), "session expired or revoked")
			return
		}
		var frame protocol.Frame
		if kind != websocket.MessageText || json.Unmarshal(data, &frame) != nil {
			s.socketError(c, "invalid_frame", "")
			continue
		}
		if frame.V != protocol.Version {
			s.socketError(c, "protocol_mismatch", frame.ID)
			continue
		}
		if frame.ID == "" || len(frame.ID) > 128 || frame.Seq < 0 {
			s.socketError(c, "invalid_frame", frame.ID)
			continue
		}
		if !registered && frame.Type != protocol.Hello {
			s.socketError(c, "hello_required", frame.ID)
			continue
		}
		if registered && frame.Type == protocol.Hello {
			s.socketError(c, "already_ready", frame.ID)
			continue
		}
		if !registered {
			var cursor protocol.Cursor
			if !decode(frame.Payload, &cursor) || cursor.Cursor < 0 {
				s.socketError(c, "invalid_cursor", frame.ID)
				continue
			}
			if !s.registry.add(principal, c) {
				s.socketError(c, "too_many_devices", frame.ID)
				return
			}
			registered = true
			helloCancel()
		}
		s.dispatch(c, principal, hex.EncodeToString(device), frame)
	}
}

func decode(data json.RawMessage, value any) bool {
	return len(data) > 0 && string(data) != "null" && json.Unmarshal(data, value) == nil
}

func (s *Server) socketError(c *socketConn, code, ref string) {
	if !c.enqueue(
		protocol.New(
			protocol.Error,
			ref,
			0,
			time.Now(),
			protocol.ErrorPayload{Code: code, Ref: ref},
		),
	) {
		c.stop()
	}
}

func (s *Server) dispatch(c *socketConn, principal, device string, frame protocol.Frame) {
	ctx, cancel := context.WithTimeout(c.ctx, rpcTimeout)
	defer cancel()
	session := s.principalSession(principal)
	switch frame.Type {
	case protocol.Hello, protocol.Sync:
		var cursor protocol.Cursor
		if !decode(frame.Payload, &cursor) || cursor.Cursor < 0 {
			s.socketError(c, "invalid_cursor", frame.ID)
			return
		}
		if !s.replay(ctx, c, device, frame.ID, cursor.Cursor) {
			return
		}
		if snapshot, ok := session.snapshot(); ok && !c.enqueue(snapshot) {
			c.stop()
		}
	case protocol.Send:
		var request protocol.SendRequest
		if !decode(frame.Payload, &request) || request.ClientMsgID == "" ||
			len(request.ClientMsgID) > 128 ||
			strings.TrimSpace(request.Text) == "" ||
			len(request.Text) > 64<<10 {
			s.socketError(c, "invalid_message", frame.ID)
			return
		}
		if err := session.send(ctx, frame.ID, request); err != nil {
			s.socketError(c, "bridge_unavailable", frame.ID)
		}
	case protocol.Abort:
		var request protocol.AbortRequest
		if !decode(frame.Payload, &request) || request.Turn < 0 {
			s.socketError(c, "invalid_turn", frame.ID)
			return
		}
		if err := session.abort(ctx, request.Turn); err != nil {
			s.socketError(c, "bridge_unavailable", frame.ID)
		}
	case protocol.Status:
		if !c.enqueue(session.statusFrame(frame.ID)) {
			c.stop()
		}
	case protocol.Ack:
		var request protocol.AckRequest
		if !decode(frame.Payload, &request) || !session.validAck(request.Seq) {
			s.socketError(c, "invalid_ack", frame.ID)
		}
	case protocol.Presence:
		var request protocol.PresenceRequest
		if !decode(frame.Payload, &request) {
			s.socketError(c, "invalid_presence", frame.ID)
		}
	case protocol.Ping:
		if !c.enqueue(protocol.New(protocol.Pong, frame.ID, 0, time.Now(), struct{}{})) {
			c.stop()
		}
	default:
		s.socketError(c, "unknown_type", frame.ID)
	}
}
