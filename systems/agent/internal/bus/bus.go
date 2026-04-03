// Package bus provides in-memory message passing between runtime components.
package bus

import (
	"context"
	"fmt"
)

const (
	// DefaultBufferSize is the default channel buffer size for new buses.
	DefaultBufferSize = 128
	// ChannelTelegram identifies Telegram transport messages.
	ChannelTelegram = "telegram"
)

// InboundMessage is a user-originated message entering the runtime.
type InboundMessage struct {
	Channel   string
	ChatID    string
	UserID    string
	MessageID string
	Text      string
	Media     []string
}

// OutboundMessage is a transport-bound message leaving the runtime.
type OutboundMessage struct {
	Channel string
	ChatID  string
	Text    string
}

// Bus carries inbound and outbound runtime messages.
type Bus struct {
	inbound  chan InboundMessage
	outbound chan OutboundMessage
}

// New constructs a bus with the requested buffer size.
func New(bufferSize int) *Bus {
	if bufferSize <= 0 {
		bufferSize = DefaultBufferSize
	}

	return &Bus{
		inbound:  make(chan InboundMessage, bufferSize),
		outbound: make(chan OutboundMessage, bufferSize),
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
