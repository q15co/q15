package bridge

import (
	"context"
	"testing"
	"time"

	"github.com/q15co/q15/libs/chat-contract/chatpb"
	"github.com/q15co/q15/systems/agent/internal/bus"
	channelport "github.com/q15co/q15/systems/agent/internal/channel"
)

// The worker's registries require both ports of the endpoint, so both hold
// as compile-time assertions beside the tests that exercise them.
var (
	_ channelport.AgentEndpoint    = (*AgentEndpoint)(nil)
	_ channelport.OutboundEndpoint = (*AgentEndpoint)(nil)
)

// waitDeliverSubscribers waits until the fan-out holds at least want
// subscribers. The server-side Deliver registers its stream once the rpc
// reaches the handler, which races the client's call, so tests synchronize on
// the registry instead of sleeping past it.
func waitDeliverSubscribers(
	t *testing.T,
	endpoint *AgentEndpoint,
	want int,
) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if endpoint.outbound.count() >= want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf(
		"Deliver registry holds %d subscriber(s) after 2s, want at least %d",
		endpoint.outbound.count(),
		want,
	)
}

// recvDeliverFrame reads one Deliver stream frame on a deadline so a missing
// frame fails the test instead of hanging it.
func recvDeliverFrame(
	t *testing.T,
	stream chatpb.ChatService_DeliverClient,
) (*chatpb.DeliverResponse, error) {
	t.Helper()
	type frame struct {
		response *chatpb.DeliverResponse
		err      error
	}
	done := make(chan frame, 1)
	go func() {
		response, err := stream.Recv()
		if err != nil {
			done <- frame{err: err}
			return
		}
		done <- frame{response: response}
	}()
	select {
	case got := <-done:
		return got.response, got.err
	case <-time.After(2 * time.Second):
		t.Fatal("Deliver Recv() timed out waiting for a frame")
		return nil, nil
	}
}

// assertDeliverFrame pins the contract fields a fan-out frame must carry: the
// chat id and text the bus published and a queued_at stamp, which is the
// bridge's receive time because the bus message carries no producer
// timestamp.
func assertDeliverFrame(
	t *testing.T,
	name string,
	got *chatpb.DeliverResponse,
	msg bus.OutboundMessage,
) {
	t.Helper()
	if got.GetChatId() != msg.ChatID || got.GetText() != msg.Text {
		t.Fatalf(
			"%s frame = (%q %q), want (%q %q)",
			name, got.GetChatId(), got.GetText(), msg.ChatID, msg.Text,
		)
	}
	if got.GetQueuedAt() == nil {
		t.Fatalf("%s queued_at = unset, want the bridge's receive stamp", name)
	}
}

// TestAgentEndpointDeliverFansOutToEveryOpenSubscriber is the fake-message
// acceptance: one bus message, every subscribed stream holds it.
func TestAgentEndpointDeliverFansOutToEveryOpenSubscriber(t *testing.T) {
	_, endpoint := newTestEndpoint(&fakePublisher{})
	subscribers := []*outboundSubscriber{
		endpoint.outbound.subscribe(),
		endpoint.outbound.subscribe(),
		endpoint.outbound.subscribe(),
	}

	published := bus.OutboundMessage{
		Channel: bus.ChannelBridge,
		ChatID:  "conv-9",
		Text:    "unsolicited hello",
	}
	if err := endpoint.Deliver(context.Background(), published); err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}
	for i, subscriber := range subscribers {
		select {
		case got := <-subscriber.messages:
			assertDeliverFrame(t, "fan-out", got, published)
		case <-time.After(2 * time.Second):
			t.Fatalf("subscriber %d never received the message", i)
		}
	}
}

// TestAgentEndpointDeliverDropsFullSubscriberWithoutBlocking pins the drop
// semantics for a partial drop: Deliver returns without waiting on the
// subscriber that stopped reading — its stream ends instead — the healthy
// subscriber still receives the message, and the partial drop is NOT an error,
// because the message did reach a stream. The publisher records an error
// against the run rather than retrying, so reporting this as a failure would
// mark a notification the UI received as failed.
func TestAgentEndpointDeliverDropsFullSubscriberWithoutBlocking(t *testing.T) {
	_, endpoint := newTestEndpoint(&fakePublisher{})
	slow := endpoint.outbound.subscribe()
	healthy := endpoint.outbound.subscribe()
	// Fill the slow subscriber's buffer by hand, so the next fan-out finds
	// its non-blocking send unable to land.
	for i := 0; i < outboundSubscriberBuffer; i++ {
		slow.messages <- &chatpb.DeliverResponse{}
	}

	published := bus.OutboundMessage{
		Channel: bus.ChannelBridge,
		ChatID:  "conv-4",
		Text:    "for those still reading",
	}
	done := make(chan error, 1)
	go func() {
		done <- endpoint.Deliver(context.Background(), published)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Deliver() error = %v, want nil: the healthy stream received it", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Deliver() blocked on the full subscriber instead of dropping it")
	}

	assertDeliverFrame(t, "healthy", <-healthy.messages, published)

	// The dropped stream ends after what it already buffered: ranging the
	// closed channel drains its frames and then stops, which is what the
	// client's stream loop sees.
	drained := 0
	for range slow.messages {
		drained++
	}
	if drained != outboundSubscriberBuffer {
		t.Fatalf(
			"dropped subscriber delivered %d buffered frames, want %d",
			drained, outboundSubscriberBuffer,
		)
	}
	if endpoint.outbound.count() != 1 {
		t.Fatalf("Deliver registry holds %d subscriber(s) after the drop, want 1",
			endpoint.outbound.count())
	}
}

// TestAgentEndpointDeliverReportsFailureWhenNobodyReceivedIt pins the one case
// that is an error: the bridge was connected to and every stream fell behind,
// so the message reached nobody at all. That is a real delivery failure, and
// it is the only one worth telling the publisher about.
func TestAgentEndpointDeliverReportsFailureWhenNobodyReceivedIt(t *testing.T) {
	_, endpoint := newTestEndpoint(&fakePublisher{})
	slow := endpoint.outbound.subscribe()
	for i := 0; i < outboundSubscriberBuffer; i++ {
		slow.messages <- &chatpb.DeliverResponse{}
	}

	err := endpoint.Deliver(context.Background(), bus.OutboundMessage{
		Channel: bus.ChannelBridge,
		ChatID:  "conv-5",
		Text:    "nobody is reading",
	})
	if err == nil {
		t.Fatal("Deliver() error = nil, want a failure when no stream received the message")
	}
	if endpoint.outbound.count() != 0 {
		t.Fatalf("registry holds %d subscriber(s) after the last one was dropped, want 0",
			endpoint.outbound.count())
	}
}

// TestAgentEndpointDeliverWithoutSubscribersDropsQuietly pins the registry
// key and the no-audience decision: Channel() stays bus.ChannelBridge, and
// Deliver with nothing connected returns nil. Nothing was attempted, so
// nothing failed, and the message is dropped rather than held — an error
// would leave a publisher retrying a notification against a UI that may
// never connect, with no persistence on this slice to replay it from.
func TestAgentEndpointDeliverWithoutSubscribersDropsQuietly(t *testing.T) {
	_, endpoint := newTestEndpoint(&fakePublisher{})
	if got := endpoint.Channel(); got != bus.ChannelBridge {
		t.Fatalf("Channel() = %q, want %q", got, bus.ChannelBridge)
	}

	err := endpoint.Deliver(context.Background(), bus.OutboundMessage{
		Channel: bus.ChannelBridge,
		ChatID:  "conv-2",
		Text:    "nobody listening",
	})
	if err != nil {
		t.Fatalf("Deliver() with no subscriber error = %v, want nil", err)
	}
}

// TestServiceDeliverStreamsPublishedMessages drives the rpc over a real
// transport: the client's stream receives the message the worker fanned out,
// carrying the chat id and text it was published with.
func TestServiceDeliverStreamsPublishedMessages(t *testing.T) {
	service, endpoint := newTestEndpoint(&fakePublisher{})
	client := startEventBridgeClient(t, service)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := client.Deliver(ctx, &chatpb.DeliverRequest{})
	if err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}
	waitDeliverSubscribers(t, endpoint, 1)

	published := bus.OutboundMessage{
		Channel: bus.ChannelBridge,
		ChatID:  "conv-11",
		Text:    "hello ui",
	}
	if err := endpoint.Deliver(context.Background(), published); err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}
	frame, err := recvDeliverFrame(t, stream)
	if err != nil {
		t.Fatalf("Deliver Recv() error = %v, want a frame", err)
	}
	// Client-side the frame is what the bus carried, so the fan-out's
	// mapping is checked at the transport boundary, not only in memory.
	assertDeliverFrame(t, "stream", frame, published)
}
