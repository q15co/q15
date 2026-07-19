package bus

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPublishOutboundRetainsEnqueueOnlySemantics(t *testing.T) {
	t.Parallel()

	messageBus := New(1)
	want := OutboundMessage{
		Channel: ChannelTelegram,
		ChatID:  "chat-123",
		Text:    "queued",
	}
	if err := messageBus.PublishOutbound(context.Background(), want); err != nil {
		t.Fatalf("PublishOutbound() error = %v", err)
	}

	select {
	case got := <-messageBus.Outbound():
		if got != want {
			t.Fatalf("Outbound() = %#v, want %#v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for enqueue-only outbound message")
	}
}

func TestPublishOutboundAndWaitForDeliveryReturnsAcknowledgement(t *testing.T) {
	t.Parallel()

	messageBus := New(1)
	want := OutboundMessage{
		Channel: ChannelTelegram,
		ChatID:  "chat-123",
		Text:    "deliver",
	}
	deliveryErr := errors.New("telegram unavailable")
	done := make(chan error, 1)
	go func() {
		done <- messageBus.PublishOutboundAndWaitForDelivery(
			context.Background(),
			want,
		)
	}()

	var request OutboundDeliveryRequest
	select {
	case request = <-messageBus.OutboundDeliveryRequests():
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for acknowledged delivery request")
	}
	if got := request.Message(); got != want {
		t.Fatalf("Message() = %#v, want %#v", got, want)
	}
	request.Acknowledge(deliveryErr)

	select {
	case err := <-done:
		if !errors.Is(err, deliveryErr) {
			t.Fatalf("PublishOutboundAndWaitForDelivery() error = %v, want %v", err, deliveryErr)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for delivery result")
	}
}

func TestPublishOutboundAndWaitForDeliveryStopsWaitingOnContextCancellation(t *testing.T) {
	t.Parallel()

	messageBus := New(1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- messageBus.PublishOutboundAndWaitForDelivery(ctx, OutboundMessage{
			Channel: ChannelTelegram,
			ChatID:  "chat-123",
			Text:    "deliver",
		})
	}()

	var request OutboundDeliveryRequest
	select {
	case request = <-messageBus.OutboundDeliveryRequests():
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for acknowledged delivery request")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("PublishOutboundAndWaitForDelivery() error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for canceled publisher")
	}

	request.Acknowledge(nil)
}
