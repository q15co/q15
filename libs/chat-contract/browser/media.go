package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/q15co/q15/libs/chat-contract/browser/protocol"
	"github.com/q15co/q15/libs/chat-contract/browser/seal"
	"github.com/q15co/q15/libs/chat-contract/chatpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// MediaType authenticates the binary bundle's descriptor and bytes together.
const MediaType = "application/vnd.q15.media"

func (s *Endpoint) mediaChannel(binding, channel string) (*socketConn, error) {
	s.channelsMu.Lock()
	c := s.channels[channel]
	s.channelsMu.Unlock()
	if c == nil || c.ctx.Err() != nil || c.binding != binding {
		return nil, status.Error(codes.Unauthenticated, "active content session required")
	}
	return c, nil
}

// OpenMedia authenticates uploads before the agent interprets any descriptors.
func (s *Endpoint) OpenMedia(req *chatpb.PutMediaRequest) ([]byte, string, error) {
	c, err := s.mediaChannel(req.GetBinding(), req.GetChannelId())
	if err != nil {
		return nil, "", err
	}
	if len(req.GetFrame()) > protocol.MaxMediaWireBytes {
		return nil, "", status.Error(codes.ResourceExhausted, "content_too_large")
	}
	var frame protocol.Frame
	if json.Unmarshal(req.GetFrame(), &frame) != nil || frame.V != protocol.Version ||
		frame.Type != protocol.MediaPut || frame.ID == "" || len(frame.ID) > 128 || frame.Seq != 0 {
		return nil, "", status.Error(codes.InvalidArgument, "invalid media frame")
	}
	var value protocol.Sealed
	if !decode(frame.Payload, &value) {
		return nil, "", status.Error(codes.InvalidArgument, "invalid envelope")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, replay := c.seen[value.Stream]; replay || len(c.seen) >= protocol.MaxContentStreams {
		return nil, "", status.Error(codes.InvalidArgument, "replayed envelope")
	}
	out := &mediaCollector{}
	kind, err := c.keys.Open(value, seal.Context(frame), out)
	if errors.Is(err, errContentTooLarge) {
		return nil, "", status.Error(codes.ResourceExhausted, "content_too_large")
	}
	if err != nil || kind != MediaType {
		return nil, "", status.Error(codes.InvalidArgument, "unseal_failed")
	}
	c.seen[value.Stream] = struct{}{}
	return out.Bytes(), frame.ID, nil
}

type mediaCollector struct{ bytes.Buffer }

func (w *mediaCollector) Write(p []byte) (int, error) {
	if w.Len()+len(p) > protocol.MaxMediaPlainBytes {
		return 0, errContentTooLarge
	}
	return w.Buffer.Write(p)
}

// MediaResult wraps upload refs and names for the same live content session.
func (s *Endpoint) MediaResult(
	req *chatpb.PutMediaRequest,
	id string,
	result protocol.MediaResult,
) (*chatpb.BrowserPacket, error) {
	c, err := s.mediaChannel(req.GetBinding(), req.GetChannelId())
	if err != nil {
		return nil, err
	}
	data, err := c.encode(protocol.New(protocol.MediaDone, id, 0, time.Now(), result))
	if err != nil {
		return nil, status.Error(codes.Internal, "content sealing failed")
	}
	return &chatpb.BrowserPacket{Frame: data}, nil
}

// StreamMedia emits a single JSON envelope using bounded encrypted chunks.
func (s *Endpoint) StreamMedia(
	ctx context.Context,
	req *chatpb.GetMediaRequest,
	source io.Reader,
	emit func(*chatpb.BrowserPacket) error,
) error {
	c, err := s.mediaChannel(req.GetBinding(), req.GetChannelId())
	if err != nil {
		return err
	}
	frame := protocol.New(protocol.MediaGet, req.GetMediaRef(), 0, time.Now(), nil)
	header, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	header = bytes.TrimSuffix(header, []byte("null}"))
	first := true
	_, err = c.keys.Seal(
		MediaType,
		seal.Context(frame),
		source,
		func(stream string, chunk protocol.Chunk) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if c.ctx.Err() != nil {
				return status.Error(codes.Unauthenticated, "content session closed")
			}
			data, err := json.Marshal(chunk)
			if err != nil {
				return err
			}
			if first {
				prefix, _ := json.Marshal(struct {
					Version int    `json:"version"`
					Stream  string `json:"stream"`
				}{1, stream})
				data = append(
					append(append(header, prefix[:len(prefix)-1]...), []byte(",\"chunks\":[")...),
					data...)
				first = false
			} else {
				data = append([]byte(","), data...)
			}
			return emit(&chatpb.BrowserPacket{Frame: data})
		},
	)
	if err != nil {
		return err
	}
	return emit(&chatpb.BrowserPacket{Frame: []byte("]}}")})
}
