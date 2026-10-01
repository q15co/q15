package bridge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/q15co/q15/libs/chat-contract/chatpb"
	"github.com/q15co/q15/systems/agent/internal/agent"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	// sessionEventRetention is how many events one session's log keeps. The
	// log is the client's resume source: WatchEvents reads a reconnected
	// stream's history out of this window, and a cursor that falls before it
	// is answered with a resync Notice and the window it can still reach
	// rather than a failure, so a run never stalls on a client that fell
	// behind.
	sessionEventRetention = 512
	// sessionEventRetentionBytes caps the serialized history retained for one
	// session. Snapshot keyframes carry the full answer draft, so a count-only
	// limit lets a bounded model response consume the sum of every historical
	// prefix. The newest event is always retained even when it alone exceeds
	// this budget, which keeps replay and resync semantics defined.
	sessionEventRetentionBytes = 4 << 20
	// sessionSubscriberBuffer is how many events one WatchEvents
	// subscriber's channel buffers before that subscriber is dropped. One
	// buffered channel per subscriber keeps fan-out a non-blocking send.
	sessionSubscriberBuffer = 64
	// resyncNoticeCode names the Notice a reconnecting client gets when its
	// acknowledged cursor no longer reaches the retained history.
	resyncNoticeCode = "resync"
)

// bridgeDeltaFlushInterval bounds how long the run's raw deltas sit
// unflushed. The first pending delta arms one flush this far out and later
// deltas leave it alone, so a flood becomes at most one coalesced frame per
// kind per interval while a stream that never pauses still emits keyframes on
// time. The telegram endpoint's progress edit debounce is the same shape, and
// the one part deliberately not copied is the re-aiming: a trailing debounce
// would emit nothing at all for a stream whose gaps are shorter than this.
const bridgeDeltaFlushInterval = 250 * time.Millisecond

// sessionEventLog is one logical session's run event stream. Every event
// takes a monotonic per-session event_index starting at 1; it is the counter
// the contract's SessionEvent.event_index comment owns and what a client
// acknowledges on reconnect, and it is deliberately not the transcript
// sequence, which turn_seq carries.
//
// Appends are a non-blocking fan-out, in step with the RunObserver contract:
// the engine hands events over synchronously and never spawns per-event work,
// so nothing here may queue events or block the run on a slow watcher.
type sessionEventLog struct {
	mu sync.Mutex
	// nextIndex is the index the next append takes. It starts at 1 so a
	// log's first entry has index 1 and zero stays free as the "no cursor"
	// value of after_event_index.
	nextIndex int64
	// events are the retained events, oldest first. The window is bounded by
	// both sessionEventRetention and sessionEventRetentionBytes; evicted ones
	// are gone for every reader.
	events []*chatpb.SessionEvent
	// retainedBytes is the sum of proto.Size for events. Events are immutable
	// after append, so the serialized-size accounting remains exact.
	retainedBytes int
	// subscribers are the live WatchEvents streams, keyed by their identity.
	subscribers map[*sessionSubscriber]struct{}
}

// sessionSubscriber is one live WatchEvents stream. Its channel is the only
// thing the append path touches.
type sessionSubscriber struct {
	events chan *chatpb.SessionEvent
}

// newSessionEventLog returns an empty log for one session.
func newSessionEventLog() *sessionEventLog {
	return &sessionEventLog{
		nextIndex:   1,
		subscribers: make(map[*sessionSubscriber]struct{}),
	}
}

// append assigns the event its event_index, retains it, and fans it out to
// every live subscriber.
func (l *sessionEventLog) append(event *chatpb.SessionEvent) {
	if event == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	event.EventIndex = l.nextIndex
	l.nextIndex++
	l.events = append(l.events, event)
	l.retainedBytes += proto.Size(event)

	drop := 0
	for len(l.events)-drop > 1 &&
		(len(l.events)-drop > sessionEventRetention ||
			l.retainedBytes > sessionEventRetentionBytes) {
		l.retainedBytes -= proto.Size(l.events[drop])
		drop++
	}
	if drop > 0 {
		// Copy the newest events forward into the same backing array so a
		// long-lived session's retention does not creep the slice's cap, then
		// clear the vacated pointers so the evicted messages can be collected.
		copy(l.events, l.events[drop:])
		clear(l.events[len(l.events)-drop:])
		l.events = l.events[:len(l.events)-drop]
	}
	for subscriber := range l.subscribers {
		select {
		case subscriber.events <- event:
		default:
			// A subscriber that cannot keep up is dropped, and deliberately
			// not stalled or starved. Blocking the run is what the
			// RunObserver contract forbids, and hiding the backlog would
			// leave the client with a stream that lies. Closing the buffered
			// channel instead ends its stream after everything it already
			// holds: the client sees the stream stop, reconnects with the
			// last event_index it received, and the replay below serves
			// exactly what it missed. Dropping is therefore a flow-control
			// signal, not data loss.
			delete(l.subscribers, subscriber)
			close(subscriber.events)
		}
	}
}

// subscribe admits one watcher at the client's acknowledged cursor. It
// registers the subscriber and takes the replay in one critical section, so
// an event is either retained-and-replayed here or fanned out live, never
// both and never neither. A cursor that can no longer reach the retained
// history opens the replay with the resync Notice instead of an error.
func (l *sessionEventLog) subscribe(afterIndex int64) (
	*sessionSubscriber,
	[]*chatpb.SessionEvent,
) {
	l.mu.Lock()
	defer l.mu.Unlock()

	replay := make([]*chatpb.SessionEvent, 0, len(l.events))
	if len(l.events) > 0 && afterIndex < l.events[0].EventIndex-1 {
		// The client acknowledges less than the window's oldest index less
		// one, so part of what it still needs is already evicted. A cursor
		// exactly one below the oldest index is not a gap: everything after
		// it is still retained. Explain the resync before the replayed
		// frames, since the client has to know where its history went.
		notice := &chatpb.SessionEvent{
			OccurredAt: timestamppb.Now(),
			Event: &chatpb.SessionEvent_Notice{Notice: &chatpb.Notice{
				Code: resyncNoticeCode,
				Text: fmt.Sprintf(
					"resync: event history before index %d is no longer retained, so the stream restarts at index %d; refetch the transcript with ListTurns to fill what the window dropped",
					l.events[0].EventIndex-1,
					l.events[0].EventIndex,
				),
			}},
		}
		// The notice sorts immediately before the window it introduces and
		// consumes no counter value. It has to carry an index below the
		// frames replayed next, because those follow it on this stream and a
		// client acknowledges what it received: a fresh index here would
		// break the stream's monotonicity and burn a value no event ever
		// fills. It is deliberately not retained either, since it is
		// addressed to this resyncing stream alone and retaining it would
		// replay one reader's resync at every future subscriber.
		notice.EventIndex = l.events[0].EventIndex - 1
		replay = append(replay, notice)
	}
	for _, event := range l.events {
		if event.EventIndex > afterIndex {
			replay = append(replay, event)
		}
	}
	subscriber := &sessionSubscriber{
		events: make(chan *chatpb.SessionEvent, sessionSubscriberBuffer),
	}
	l.subscribers[subscriber] = struct{}{}
	return subscriber, replay
}

// unsubscribe retires one live watcher. The close ends its delivery loop
// after whatever is already buffered, which is why the deferred call is
// harmless against a subscriber the drop path already closed: both paths go
// through the same map delete.
func (l *sessionEventLog) unsubscribe(subscriber *sessionSubscriber) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.subscribers[subscriber]; !ok {
		return
	}
	delete(l.subscribers, subscriber)
	close(subscriber.events)
}

// retained returns the log's retained events, oldest first. It is the tests'
// read on the stream, same rules as the replay serves.
func (l *sessionEventLog) retained() []*chatpb.SessionEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]*chatpb.SessionEvent{}, l.events...)
}

// appendRunEvent maps one agent run event into its contract form and appends
// it to the session's log. Unknown event types map to nil and are dropped:
// the contract is additive-only, so the bridge invents no stream members.
func (s *logicalSession) appendRunEvent(event agent.RunEvent) {
	s.events.append(runEventToSessionEvent(event))
}

// appendEvent appends one mapped contract event to the session's log. It is
// the run path's append for events the RunEvent mapping does not produce,
// such as the session opening and the bridge's coalesced keyframes.
func (s *logicalSession) appendEvent(event *chatpb.SessionEvent) {
	s.events.append(event)
}

// runEventToSessionEvent translates one agent run event into the oneof form
// the contract streams back. It is the single mapping for the layer:
// occurred_at comes from event.At, turn_seq from event.Seq, and the oneof
// member from a switch on event.Type. An event type the contract has no
// member for maps to nil, which append drops.
func runEventToSessionEvent(event agent.RunEvent) *chatpb.SessionEvent {
	event = protobufSafeRunEvent(event)
	var out *chatpb.SessionEvent
	switch event.Type {
	case agent.RunEventRunStarted: // run_events.go:15
		out = &chatpb.SessionEvent{Event: &chatpb.SessionEvent_RunStarted{
			RunStarted: &chatpb.RunStarted{},
		}}
	case agent.RunEventModelTurnStarted: // run_events.go:16
		out = &chatpb.SessionEvent{Event: &chatpb.SessionEvent_ModelTurnStarted{
			ModelTurnStarted: &chatpb.ModelTurnStarted{
				// loop_turn is the engine's per-run loop counter, not the
				// transcript sequence; event.Turn is deliberately not Seq
				// and the two must never swap. The narrowing to int32 is
				// safe because the engine bounds that counter by maxTurns.
				LoopTurn: int32(event.Turn),
				ModelRef: event.ModelRef,
			},
		}}
	case agent.RunEventModelTurnDelta: // run_events.go:17
		out = &chatpb.SessionEvent{Event: &chatpb.SessionEvent_ModelTurnDelta{
			ModelTurnDelta: &chatpb.ModelTurnDelta{Delta: event.Delta},
		}}
	case agent.RunEventModelReasoningDelta: // run_events.go:18
		out = &chatpb.SessionEvent{Event: &chatpb.SessionEvent_ModelReasoningDelta{
			ModelReasoningDelta: &chatpb.ModelReasoningDelta{Delta: event.Delta},
		}}
	case agent.RunEventToolStarted: // run_events.go:19
		out = &chatpb.SessionEvent{Event: &chatpb.SessionEvent_ToolStarted{
			ToolStarted: &chatpb.ToolStarted{Call: toolCallToProto(event.ToolCall)},
		}}
	case agent.RunEventToolFinished: // run_events.go:20
		// is_error comes from event.Err, the same signal the engine and the
		// telegram endpoint already agree on: the engine sets Err when a
		// tool invocation failed and folds the error into the output text,
		// and telegram renders a failed tool step from that field. There is
		// no separate tool error field to derive a second truth from.
		out = &chatpb.SessionEvent{Event: &chatpb.SessionEvent_ToolFinished{
			ToolFinished: &chatpb.ToolFinished{
				Call:    toolCallToProto(event.ToolCall),
				Output:  event.ToolOutput,
				IsError: event.Err != nil,
			},
		}}
	case agent.RunEventRunFinished: // run_events.go:21
		out = &chatpb.SessionEvent{Event: &chatpb.SessionEvent_RunFinished{
			RunFinished: &chatpb.RunFinished{
				FullText: event.FinalText,
				ModelRef: event.ModelRef,
				Status:   chatpb.RunStatus_RUN_STATUS_COMPLETED,
			},
		}}
	case agent.RunEventRunFailed: // run_events.go:22
		failed := &chatpb.RunFailed{
			FullText: event.FinalText,
			Status:   runFailedStatus(event.Err),
		}
		if event.Err != nil {
			failed.Error = protobufSafeString(event.Err.Error())
		}
		out = &chatpb.SessionEvent{Event: &chatpb.SessionEvent_RunFailed{
			RunFailed: failed,
		}}
	default:
		return nil
	}
	if !event.At.IsZero() {
		out.OccurredAt = timestamppb.New(event.At)
	}
	out.TurnSeq = event.Seq
	return out
}

// runFailedStatus classifies an engine failure event. A cancelled run is not
// a failure: the loop reports every engine error through run_failed,
// including the context cancellation a client's Abort produces, and the
// contract has a status for exactly that. Abort's own terminal frame stays as
// the fallback for a cancellation the engine never reported.
func runFailedStatus(err error) chatpb.RunStatus {
	if errors.Is(err, context.Canceled) {
		return chatpb.RunStatus_RUN_STATUS_ABORTED
	}
	return chatpb.RunStatus_RUN_STATUS_FAILED
}

// toolCallToProto translates one canonical tool call. The arguments stay the
// raw JSON object the model produced; the bridge parses nothing.
func toolCallToProto(call agent.ToolCall) *chatpb.ToolCall {
	return &chatpb.ToolCall{
		Id:        protobufSafeString(call.ID),
		Name:      protobufSafeString(call.Name),
		Arguments: protobufSafeString(call.Arguments),
	}
}

// protobufSafeRunEvent normalizes strings supplied by models and tools before
// they enter protobuf messages or the bridge's accumulated drafts. Go strings
// may contain arbitrary bytes, while protobuf string fields require UTF-8;
// replacing malformed runs at this boundary keeps one tool result from
// terminating both live delivery and every replay of the retained event.
func protobufSafeRunEvent(event agent.RunEvent) agent.RunEvent {
	event.ModelRef = protobufSafeString(event.ModelRef)
	event.Delta = protobufSafeString(event.Delta)
	event.ToolCall.ID = protobufSafeString(event.ToolCall.ID)
	event.ToolCall.Name = protobufSafeString(event.ToolCall.Name)
	event.ToolCall.Arguments = protobufSafeString(event.ToolCall.Arguments)
	event.ToolOutput = protobufSafeString(event.ToolOutput)
	event.FinalText = protobufSafeString(event.FinalText)
	return event
}

func protobufSafeString(value string) string {
	return strings.ToValidUTF8(value, "\uFFFD")
}

// OnRunEvent coalesces the run's raw, unthrottled deltas and appends the run
// to the session's event log in the contract's frames. Deltas accumulate as
// a pending draft that the flush turns into one frame per kind plus a
// Snapshot keyframe; every non-delta event flushes the draft first, so the
// stream's order keeps matching the run's. The engine hands events over
// synchronously and in order and spawns no per-event work, so this is one
// lock and a buffered fan-out, never a queue.
func (s *runSession) OnRunEvent(_ context.Context, event agent.RunEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished || s.terminalEmitted {
		// The run already ended: Finish or Abort closed it, or the engine
		// emitted its terminal event. Nothing may be appended past the
		// terminal frame, so a straggler is dropped rather than streamed.
		return
	}
	event = protobufSafeRunEvent(event)
	if event.ModelRef != "" {
		s.modelRef = event.ModelRef
	}
	if event.Seq > 0 {
		s.seq = event.Seq
		s.session.setTurnSeq(event.Seq)
	}

	switch event.Type {
	case agent.RunEventModelTurnDelta: // run_events.go:17
		s.answerPending += event.Delta
		s.answerSoFar += event.Delta
		s.scheduleDeltaFlushLocked()
	case agent.RunEventModelReasoningDelta: // run_events.go:18
		s.reasoningPending += event.Delta
		s.scheduleDeltaFlushLocked()
	case agent.RunEventRunFinished, agent.RunEventRunFailed: // run_events.go:21, :22
		// The engine owns its terminal event on the normal paths. Record
		// that here, so the worker's Finish or Abort cannot follow it with a
		// second terminal event.
		s.terminalEmitted = true
		s.flushDeltaBuffersLocked()
		if event.Type == agent.RunEventRunFailed && event.FinalText == "" {
			// A canceled or failed stream has no canonical response. Preserve
			// its current attempt's draft for UI recovery only; the engine's
			// transcript and any explicit final text remain authoritative.
			event.FinalText = s.answerSoFar
		}
		s.session.appendEvent(runEventToSessionEvent(event))
	default:
		s.flushDeltaBuffersLocked()
		if event.Type == agent.RunEventModelTurnStarted {
			// A new model attempt replaces the drafts: the RunEvent comment
			// says subscribers should replace previous answer and reasoning
			// drafts here, and the bridge is the first subscriber, so its
			// Snapshot keyframes restart from this point. Only the answer
			// draft is carried forward in a keyframe, because the contract's
			// Snapshot holds the answer text alone.
			s.answerSoFar = ""
		}
		s.session.appendEvent(runEventToSessionEvent(event))
	}
}

// scheduleDeltaFlushLocked arms one flush at most bridgeDeltaFlushInterval
// out. The first pending delta arms it and later deltas leave it alone, so the
// interval paces the stream instead of being pushed back by it, and the flush
// itself re-checks every state it touches, so a timer that lost its race to
// Finish or Abort does nothing.
func (s *runSession) scheduleDeltaFlushLocked() {
	if s.deltaTimer != nil {
		// A flush is already pending, so leave it armed. Re-aiming it on
		// every delta would make this a trailing debounce: a model stream
		// that never pauses for a whole interval would emit no keyframe at
		// all and the pending draft would grow without bound, which is the
		// opposite of the bounded storage the RunObserver contract asks for.
		return
	}
	s.deltaTimer = time.AfterFunc(bridgeDeltaFlushInterval, s.flushDeltaBuffers)
}

// flushDeltaBuffers is the timer's callback. A fire after the run ended is a
// no-op: by then Finish or Abort has set finished and stopped this timer,
// and the pending state is empty either way.
func (s *runSession) flushDeltaBuffers() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished {
		return
	}
	s.flushDeltaBuffersLocked()
}

// flushDeltaBuffersLocked appends the pending deltas, coalesced per kind,
// and then the Snapshot keyframe carrying the answer text accumulated so
// far, which is what a client that missed deltas recovers from. It stops the
// pending timer and does nothing with nothing pending. Locked: it shares the
// drafts with OnRunEvent and both run ends, and only ever takes the log's
// mutex after this one.
func (s *runSession) flushDeltaBuffersLocked() {
	if s.deltaTimer != nil {
		s.deltaTimer.Stop()
		s.deltaTimer = nil
	}
	answer := s.answerPending
	reasoning := s.reasoningPending
	s.answerPending = ""
	s.reasoningPending = ""
	if answer == "" && reasoning == "" {
		return
	}
	if answer != "" {
		s.session.appendRunEvent(agent.RunEvent{
			Type:  agent.RunEventModelTurnDelta,
			At:    time.Now(),
			Seq:   s.seq,
			Delta: answer,
		})
	}
	if reasoning != "" {
		s.session.appendRunEvent(agent.RunEvent{
			Type:  agent.RunEventModelReasoningDelta,
			At:    time.Now(),
			Seq:   s.seq,
			Delta: reasoning,
		})
	}
	// The keyframe is not a RunEvent: the contract describes Snapshot as the
	// bridge's own coalesced frame, and it carries the full draft the run's
	// model attempts have produced since the last one.
	s.session.appendEvent(&chatpb.SessionEvent{
		OccurredAt: timestamppb.Now(),
		TurnSeq:    s.seq,
		Event:      &chatpb.SessionEvent_Snapshot{Snapshot: &chatpb.Snapshot{Text: s.answerSoFar}},
	})
}

// appendTerminalRunFinishedLocked appends the terminal RunFinished that
// closes the gap the engine left on the abnormal paths: the contract wants
// one terminal event per run, carrying the full text the run produced, so
// one message is enough for a client to recover even at a cancelled run.
func (s *runSession) appendTerminalRunFinishedLocked(
	fullText string,
	terminalStatus chatpb.RunStatus,
) {
	s.terminalEmitted = true
	s.session.appendEvent(&chatpb.SessionEvent{
		OccurredAt: timestamppb.Now(),
		TurnSeq:    s.seq,
		Event: &chatpb.SessionEvent_RunFinished{RunFinished: &chatpb.RunFinished{
			FullText: protobufSafeString(fullText),
			ModelRef: protobufSafeString(s.modelRef),
			Status:   terminalStatus,
		}},
	})
}

// WatchEvents streams one session's run: the retained history after the
// client's acknowledged cursor first, then live events until the client's
// context is done. An unknown session is NotFound, matching SendMessage and
// Abort; a cursor older than the retained window is resynced with a Notice
// instead of failing, because the client can only ever ack what it received.
func (s *Service) WatchEvents(
	req *chatpb.WatchEventsRequest,
	stream grpc.ServerStreamingServer[chatpb.WatchEventsResponse],
) error {
	session := s.sessions.lookupSession(strings.TrimSpace(req.GetSessionId()))
	if session == nil {
		return status.Errorf(
			codes.NotFound,
			"bridge session %q not found",
			req.GetSessionId(),
		)
	}

	// The subscribe register happens under the same lock as the replay, so
	// events cannot fall between the two halves of the handoff.
	subscriber, replay := session.events.subscribe(req.GetAfterEventIndex())
	defer session.events.unsubscribe(subscriber)

	send := func(event *chatpb.SessionEvent) error {
		return stream.Send(&chatpb.WatchEventsResponse{Event: event})
	}
	for _, event := range replay {
		if err := send(event); err != nil {
			return err
		}
	}

	// The done case is what unblocks a watcher whose client walked away
	// while the session streams nothing; the unsubscribe stops the log from
	// keeping a watcher nobody reads.
	ctx := stream.Context()
	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-subscriber.events:
			if !ok {
				// The subscriber was dropped by the append path for falling
				// behind; end the stream without an error so the client's
				// reconnect is the recovery path, not a protocol failure.
				return nil
			}
			if err := send(event); err != nil {
				return err
			}
		}
	}
}
