package bus

import (
	"context"
	"fmt"
)

const (
	DefaultBufferSize = 128
	ChannelTelegram   = "telegram"
)

type InboundMessage struct {
	Channel string
	ChatID  string
	UserID  string
	Text    string
}

type OutboundMessage struct {
	Channel string
	ChatID  string
	Text    string
}

type Broker struct {
	inbound  chan InboundMessage
	outbound chan OutboundMessage
}

func New(bufferSize int) *Broker {
	if bufferSize <= 0 {
		bufferSize = DefaultBufferSize
	}

	return &Broker{
		inbound:  make(chan InboundMessage, bufferSize),
		outbound: make(chan OutboundMessage, bufferSize),
	}
}

func (b *Broker) Inbound() <-chan InboundMessage {
	return b.inbound
}

func (b *Broker) Outbound() <-chan OutboundMessage {
	return b.outbound
}

func (b *Broker) PublishInbound(ctx context.Context, msg InboundMessage) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case b.inbound <- msg:
		return nil
	}
}

func (b *Broker) PublishOutbound(ctx context.Context, msg OutboundMessage) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case b.outbound <- msg:
		return nil
	}
}

func SessionKey(channel, chatID string) (string, error) {
	if channel == "" {
		return "", fmt.Errorf("channel is required")
	}
	if chatID == "" {
		return "", fmt.Errorf("chat id is required")
	}
	return channel + ":" + chatID, nil
}
