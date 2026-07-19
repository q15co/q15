// Package bus provides in-memory message passing between runtime components.
package bus

import (
	"context"
	"fmt"
	"time"

	"github.com/q15co/q15/systems/agent/internal/conversation"
)

const (
	// DefaultBufferSize is the default channel buffer size for new buses.
	DefaultBufferSize = 128
	// ChannelTelegram identifies Telegram transport messages.
	ChannelTelegram = "telegram"
)

// InboundMessage is a user-originated message entering the runtime.
type InboundMessage struct {
	Channel     string
	ChatID      string
	UserID      string
	MessageID   string
	SentAt      time.Time
	Text        string
	Attachments []conversation.Part
}

// OutboundMessage is a transport-bound message leaving the runtime.
type OutboundMessage struct {
	Channel string
	ChatID  string
	Text    string
}

// OutboundDeliveryRequest carries one message whose publisher is waiting for
// the transport delivery result.
type OutboundDeliveryRequest struct {
	ctx    context.Context
	msg    OutboundMessage
	result chan error
}

// Bus carries inbound and outbound runtime messages.
type Bus struct {
	inbound                  chan InboundMessage
	outbound                 chan OutboundMessage
	outboundDeliveryRequests chan OutboundDeliveryRequest
}

// New constructs a bus with the requested buffer size.
func New(bufferSize int) *Bus {
	if bufferSize <= 0 {
		bufferSize = DefaultBufferSize
	}

	return &Bus{
		inbound:                  make(chan InboundMessage, bufferSize),
		outbound:                 make(chan OutboundMessage, bufferSize),
		outboundDeliveryRequests: make(chan OutboundDeliveryRequest, bufferSize),
	}
}

// Inbound returns the inbound message stream.
func (b *Bus) Inbound() <-chan InboundMessage {
	return b.inbound
}

// Outbound returns the outbound message stream.
func (b *Bus) Outbound() <-chan OutboundMessage {
	return b.outbound
}

// OutboundDeliveryRequests returns messages whose publishers require the
// endpoint's actual delivery result.
func (b *Bus) OutboundDeliveryRequests() <-chan OutboundDeliveryRequest {
	return b.outboundDeliveryRequests
}

// PublishInbound enqueues an inbound message or returns the context error.
func (b *Bus) PublishInbound(ctx context.Context, msg InboundMessage) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case b.inbound <- msg:
		return nil
	}
}

// PublishOutbound enqueues an outbound message or returns the context error.
func (b *Bus) PublishOutbound(ctx context.Context, msg OutboundMessage) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case b.outbound <- msg:
		return nil
	}
}

// PublishOutboundAndWaitForDelivery enqueues an outbound message and waits
// until the transport endpoint acknowledges success or returns its delivery
// error. The context bounds both queueing and delivery.
func (b *Bus) PublishOutboundAndWaitForDelivery(
	ctx context.Context,
	msg OutboundMessage,
) error {
	result := make(chan error, 1)
	request := OutboundDeliveryRequest{
		ctx:    ctx,
		msg:    msg,
		result: result,
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case b.outboundDeliveryRequests <- request:
	}

	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		select {
		case err := <-result:
			return err
		default:
			return ctx.Err()
		}
	}
}

// Message returns the transport-bound message for this delivery request.
func (r OutboundDeliveryRequest) Message() OutboundMessage {
	return r.msg
}

// Context returns the publisher context that bounds this delivery request.
func (r OutboundDeliveryRequest) Context() context.Context {
	if r.ctx == nil {
		return context.Background()
	}
	return r.ctx
}

// Acknowledge reports the endpoint's delivery result to the waiting publisher.
// It never blocks when the publisher has already stopped waiting.
func (r OutboundDeliveryRequest) Acknowledge(err error) {
	if r.result == nil {
		return
	}
	select {
	case r.result <- err:
	default:
	}
}

// SessionKey builds a stable per-channel session identifier.
func SessionKey(channel, chatID string) (string, error) {
	if channel == "" {
		return "", fmt.Errorf("channel is required")
	}
	if chatID == "" {
		return "", fmt.Errorf("chat id is required")
	}
	return channel + ":" + chatID, nil
}
