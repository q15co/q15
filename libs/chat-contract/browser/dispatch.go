package browser

import (
	"context"
	"strings"
	"time"

	"github.com/q15co/q15/libs/chat-contract/browser/protocol"
)

func (s *Endpoint) socketError(c *socketConn, code, ref string) {
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

func (s *Endpoint) dispatch(c *socketConn, principal, device string, frame protocol.Frame) {
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
			len(request.Text) > protocol.MaxMessageBytes {
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
