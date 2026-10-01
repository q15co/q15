package bridge

import (
	"context"
	"fmt"
	"sync"

	"github.com/q15co/q15/libs/chat-contract/chatpb"
	"github.com/q15co/q15/systems/agent/internal/bus"
	channelport "github.com/q15co/q15/systems/agent/internal/channel"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// outboundSubscriberBuffer is how many messages one Deliver subscriber's
// channel buffers before the fan-out drops it. Deliver is called by the
// outbound worker the Telegram endpoint shares, so the fan-out must never
// wait on a stream whose client stopped reading, and one buffered channel per
// subscriber keeps that send non-blocking.
const outboundSubscriberBuffer = 64

// outboundSubscriber is one live Deliver stream. Its channel is the only
// thing the fan-out path touches.
type outboundSubscriber struct {
	messages chan *chatpb.DeliverResponse
}

// outboundFanout is the registry of open Deliver streams. It lives on the
// agent endpoint rather than on a second object because the Service and the
// endpoint are the same object held once: the rpc half streams from it and
// the worker half fans into it, and two registries would split the
// subscribers from the messages.
type outboundFanout struct {
	mu          sync.Mutex
	subscribers map[*outboundSubscriber]struct{}
}

// newOutboundFanout returns an empty Deliver registry.
func newOutboundFanout() *outboundFanout {
	return &outboundFanout{
		subscribers: make(map[*outboundSubscriber]struct{}),
	}
}

// subscribe admits one Deliver stream. DeliverRequest is empty, so there is
// nothing to negotiate on the way in: every open stream receives every
// message bound for this channel, and whatever chat filtering a client needs
// is its own job above the contract.
func (f *outboundFanout) subscribe() *outboundSubscriber {
	f.mu.Lock()
	defer f.mu.Unlock()
	subscriber := &outboundSubscriber{
		messages: make(chan *chatpb.DeliverResponse, outboundSubscriberBuffer),
	}
	f.subscribers[subscriber] = struct{}{}
	return subscriber
}

// unsubscribe retires one Deliver stream. The close ends its delivery loop
// after whatever is already buffered, and the map delete it shares with the
// drop path makes the call a no-op against a subscriber the fan-out already
// removed.
func (f *outboundFanout) unsubscribe(subscriber *outboundSubscriber) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.subscribers[subscriber]; !ok {
		return
	}
	delete(f.subscribers, subscriber)
	close(subscriber.messages)
}

// fanOut hands one contract message to every open subscriber and returns how
// many dropped and how many were live. The send never blocks and never
// starves: a subscriber whose buffer is full is dropped — removed and closed —
// so its stream ends after everything it already holds. Its read side, not the
// fan-out, is where the client learns about it.
func (f *outboundFanout) fanOut(msg *chatpb.DeliverResponse) (dropped, live int) {
	f.mu.Lock()
	defer f.mu.Unlock()

	// live is how many streams this message was handed to, read under the
	// lock so the count describes the loop below rather than a later state.
	live = len(f.subscribers)
	for subscriber := range f.subscribers {
		select {
		case subscriber.messages <- msg:
		default:
			delete(f.subscribers, subscriber)
			close(subscriber.messages)
			dropped++
		}
	}
	return dropped, live
}

// count returns how many subscribers the fan-out holds. It is the tests' read
// on the registry, the same role retained() plays for the event log.
func (f *outboundFanout) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.subscribers)
}

// Deliver fans one outbound message out to every open bridge stream and
// implements channelport.OutboundEndpoint for the outbound worker. It
// accepts the port's ctx but cannot use it: fan-out is fire-and-forget by
// design, and awaiting delivery here would hang the shared worker on a slow
// or absent UI, which the endpoint contract forbids.
//
// The error means the message reached nobody, and nothing else. That is a
// narrower claim than "some delivery went wrong", and deliberately so, because
// of what the publisher does with it: the scheduler's wait-for-delivery path
// does not retry, it records the failure against the run
// (systems/agent/internal/schedule/manager.go:975). Reporting a partial drop
// as a failure would therefore mark a notification the UI did receive as
// failed, which is worse than the silence it replaces.
//
//   - No subscriber connected: nil. Nothing was attempted, so nothing failed.
//     The message is dropped rather than held, because this slice has no
//     outbound persistence, and the scheduler's own path keeps it readable:
//     it records the notification as a durable transcript event, so ListTurns
//     still has the text even though no stream saw it.
//   - Some subscribers dropped for a full buffer: nil. The message reached
//     every other stream, so the delivery happened. The dropped stream ends,
//     and because the frozen DeliverRequest carries no cursor there is no
//     replay for it: those messages are lost for that client and the closed
//     stream is the only signal it gets. Unlike the session event log, whose
//     replay makes a drop "a flow-control signal, not data loss", a drop here
//     really is data loss for that subscriber. That is the price of a
//     cursor-less contract, stated rather than hidden.
//   - Every subscriber dropped: an error. The message reached no one even
//     though the bridge was connected to, which is the one case where
//     delivery genuinely failed.
func (e *AgentEndpoint) Deliver(_ context.Context, msg bus.OutboundMessage) error {
	out := &chatpb.DeliverResponse{
		ChatId: msg.ChatID,
		Text:   msg.Text,
		// The contract defines queued_at as when the agent produced the
		// message, not when the bridge forwarded it, and bus.OutboundMessage
		// is {Channel, ChatID, Text} with no timestamp, so the producer's
		// time is unreachable here. This is the bridge's receive time: the
		// earliest instant the bridge can attest to, bounded from the
		// producer's by the bus queue and the worker loop's drain. It is a
		// deliberate deviation from the field's wording rather than a silent
		// one, and closing it needs a timestamp on the bus message.
		QueuedAt: timestamppb.Now(),
	}
	dropped, live := e.outbound.fanOut(out)
	if live > 0 && dropped == live {
		return fmt.Errorf(
			"bridge deliver: all %d subscriber(s) fell behind and were dropped",
			live,
		)
	}
	return nil
}

var _ channelport.OutboundEndpoint = (*AgentEndpoint)(nil)

// Deliver opens one outbound subscription and streams every message the bus
// publishes for this channel until the client's context is done.
// DeliverRequest is empty and the contract freezes it that way, so there is
// no chat filtering here: every open stream receives every message, and
// chat_id travels on the message itself for the client to route.
func (s *Service) Deliver(
	_ *chatpb.DeliverRequest,
	stream grpc.ServerStreamingServer[chatpb.DeliverResponse],
) error {
	subscriber := s.sessions.outbound.subscribe()
	defer s.sessions.outbound.unsubscribe(subscriber)

	// The done case is what unblocks a stream whose client walked away while
	// the bridge publishes nothing; the unsubscribe stops the fan-out from
	// keeping a stream nobody reads.
	ctx := stream.Context()
	for {
		select {
		case <-ctx.Done():
			return nil
		case msg, ok := <-subscriber.messages:
			if !ok {
				// The subscriber was dropped by the fan-out for falling
				// behind, and the frozen DeliverRequest carries no cursor, so
				// there is no replay to resume from: ending the stream is the
				// whole signal, and whatever was published while this client
				// was away is lost to it.
				return nil
			}
			if err := stream.Send(msg); err != nil {
				return err
			}
		}
	}
}
