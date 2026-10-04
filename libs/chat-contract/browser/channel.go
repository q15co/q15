package browser

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/q15co/q15/libs/chat-contract/browser/protocol"
	"github.com/q15co/q15/libs/chat-contract/browser/seal"
	"github.com/q15co/q15/libs/chat-contract/chatpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const maxQueueBytes = protocol.MaxServerFrameBytes
const maxQueueFrames = 2048

var errContentTooLarge = errors.New("content exceeds envelope budget")

type socketConn struct {
	ctx         context.Context
	cancel      context.CancelFunc
	queue       chan []byte
	queuedBytes atomic.Int64
	keys        *seal.Keys
	mu          sync.Mutex
	seen        map[string]struct{}
	binding     string
}

func (c *socketConn) enqueue(frame protocol.Frame) bool {
	data, err := c.encode(frame)
	if errors.Is(err, errContentTooLarge) {
		data, err = c.encode(protocol.New(protocol.Error, frame.ID, 0, time.Now(),
			protocol.ErrorPayload{Code: "content_too_large", Ref: frame.ID}))
	}
	return err == nil && c.enqueueBytes(data)
}

func (c *socketConn) encode(frame protocol.Frame) ([]byte, error) {
	if seal.Content(frame.Type) {
		if len(frame.Payload) > protocol.MaxEnvelopeBytes {
			return nil, errContentTooLarge
		}
		value, err := c.keys.Wrap(seal.JSONType, seal.Context(frame), frame.Payload)
		if err != nil {
			return nil, err
		}
		frame.Payload, err = json.Marshal(value)
		if err != nil {
			return nil, err
		}
	}
	return json.Marshal(frame)
}

func (c *socketConn) enqueueBytes(data []byte) bool {
	if c.ctx.Err() != nil {
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

func (c *socketConn) unseal(frame *protocol.Frame) bool {
	var value protocol.Sealed
	if !decode(frame.Payload, &value) {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, replay := c.seen[value.Stream]; replay || len(c.seen) >= protocol.MaxContentStreams {
		return false
	}
	var data bytes.Buffer
	kind, err := c.keys.Open(value, seal.Context(*frame), &data)
	if err != nil || kind != seal.JSONType {
		return false
	}
	c.seen[value.Stream] = struct{}{}
	frame.Payload = data.Bytes()
	return true
}

// Channel accepts an identity packet from the web authorization gate, never from
// browser JSON. Keys exist only here, and die when the authorized stream ends.
func (s *Endpoint) Channel(
	stream grpc.BidiStreamingServer[chatpb.BrowserPacket, chatpb.BrowserPacket],
) error {
	s.once.Do(func() { s.start(s.deliver) })
	identity, err := stream.Recv()
	if err != nil {
		return err
	}
	if identity.GetPrincipal() != "owner" || len(identity.GetBinding()) != 43 ||
		len(identity.GetFrame()) != 0 {
		return status.Error(codes.Unauthenticated, "authorized session required")
	}
	ctx, cancel := context.WithCancel(stream.Context())
	defer cancel()
	c := &socketConn{
		ctx:     ctx,
		cancel:  cancel,
		queue:   make(chan []byte, maxQueueFrames),
		binding: identity.GetBinding(),
		seen:    make(map[string]struct{}),
	}
	go c.writePackets(stream)
	packets := c.readPackets(stream)
	registered := false
	channelID := ""
	defer func() {
		if registered {
			s.registry.remove(identity.GetPrincipal(), c)
		}
		s.channelsMu.Lock()
		delete(s.channels, channelID)
		s.channelsMu.Unlock()
	}()
	for {
		var packet *chatpb.BrowserPacket
		select {
		case <-ctx.Done():
			return ctx.Err()
		case next := <-packets:
			if next.err != nil {
				return next.err
			}
			packet = next.packet
		}
		var frame protocol.Frame
		if len(packet.GetFrame()) > protocol.MaxClientFrameBytes ||
			json.Unmarshal(packet.GetFrame(), &frame) != nil {
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
			channelID, err = s.establish(c, identity.GetPrincipal(), &frame)
			registered = channelID != ""
			if err != nil {
				return err
			}
			if !registered {
				continue
			}
		}
		if frame.Type == protocol.Send && !c.unseal(&frame) {
			s.socketError(c, "unseal_failed", frame.ID)
			continue
		}
		s.dispatch(c, identity.GetPrincipal(), channelID, frame)
	}
}

type browserStream = grpc.BidiStreamingServer[chatpb.BrowserPacket, chatpb.BrowserPacket]
type receivedPacket struct {
	packet *chatpb.BrowserPacket
	err    error
}

func (c *socketConn) readPackets(stream browserStream) <-chan receivedPacket {
	packets := make(chan receivedPacket, 1)
	go func() {
		for {
			packet, err := stream.Recv()
			select {
			case packets <- receivedPacket{packet, err}:
			case <-c.ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	return packets
}

func (c *socketConn) writePackets(stream browserStream) {
	// Returning from the handler cancels a blocked transport Send or Recv.
	for {
		select {
		case <-c.ctx.Done():
			return
		case data := <-c.queue:
			c.queuedBytes.Add(-int64(len(data)))
			if err := stream.Send(&chatpb.BrowserPacket{Frame: data}); err != nil {
				c.stop()
				return
			}
		}
	}
}

func (s *Endpoint) establish(
	c *socketConn,
	principal string,
	frame *protocol.Frame,
) (string, error) {
	var hello protocol.HelloPayload
	if !decode(frame.Payload, &hello) || hello.Cursor < 0 {
		s.socketError(c, "invalid_cursor", frame.ID)
		return "", nil
	}
	if hello.Binding != c.binding {
		s.socketError(c, "invalid_key", frame.ID)
		return "", nil
	}
	private, public, err := seal.Offer()
	if err != nil {
		return "", status.Error(codes.Internal, "key establishment failed")
	}
	c.keys, err = seal.Agree(private, hello.PublicKey, c.binding, hello.PublicKey, public, true)
	if err != nil {
		s.socketError(c, "invalid_key", frame.ID)
		return "", nil
	}
	id := rand.Text()
	s.channelsMu.Lock()
	s.channels[id] = c
	s.channelsMu.Unlock()
	if !c.enqueue(
		protocol.New(
			"key",
			frame.ID,
			0,
			time.Now(),
			protocol.KeyPayload{ChannelID: id, PublicKey: public, Binding: c.binding},
		),
	) {
		return id, status.Error(codes.ResourceExhausted, "slow browser")
	}
	if !s.registry.add(principal, c) {
		return id, status.Error(codes.ResourceExhausted, "too many devices")
	}
	frame.Payload, _ = json.Marshal(protocol.Cursor{Cursor: hello.Cursor})
	return id, nil
}

// History wraps canonical records for an active channel from the same auth session.
func (s *Endpoint) History(
	ctx context.Context,
	req *chatpb.BrowserHistoryRequest,
) (*chatpb.BrowserPacket, error) {
	s.channelsMu.Lock()
	c := s.channels[req.GetChannelId()]
	s.channelsMu.Unlock()
	if c == nil || c.ctx.Err() != nil || c.binding != req.GetBinding() {
		return nil, status.Error(codes.Unauthenticated, "active content session required")
	}
	page, err := s.service.ListTurns(
		ctx,
		&chatpb.ListTurnsRequest{AfterSeq: req.GetAfterSeq(), Limit: req.GetLimit()},
	)
	if err != nil {
		return nil, status.Error(codes.Unavailable, "history unavailable")
	}
	payload, err := historyPayload(page, protocol.MaxEnvelopeBytes)
	if err != nil {
		return nil, err
	}
	frame := protocol.New(protocol.History, req.GetChannelId(), 0, time.Now(), nil)
	frame.Payload = payload
	data, err := c.encode(frame)
	if err != nil {
		return nil, status.Error(codes.Internal, "content sealing failed")
	}
	return &chatpb.BrowserPacket{Frame: data}, nil
}
