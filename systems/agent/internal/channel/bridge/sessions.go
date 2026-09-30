package bridge

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/q15co/q15/libs/chat-contract/chatpb"
	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/bus"
	channelport "github.com/q15co/q15/systems/agent/internal/channel"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// defaultBridgeChatID is the conversation the OpenSession rpc binds when the
// request leaves chat_id empty. This agent owns exactly one durable transcript,
// so the bridge has exactly one default conversation.
const defaultBridgeChatID = "default"

// bridgeOwnerID names this single-operator transport's stable owner. Socket
// access supplies authority; this identifier keeps job ownership stable across
// reconnects and process restarts without impersonating a Telegram user.
const bridgeOwnerID = "owner"

// InboundPublisher is the send side of the runtime message bus. *bus.Bus
// implements it; the interface keeps the bridge to that one method instead of
// the whole bus.
type InboundPublisher interface {
	PublishInbound(ctx context.Context, msg bus.InboundMessage) error
}

var _ InboundPublisher = (*bus.Bus)(nil)

// AgentEndpoint adapts the chat-contract bridge to the generic app worker. It
// owns the logical sessions that the Service's rpcs and the worker's runs share.
//
// It keeps no queue of its own: the worker is a single sequential loop and the
// bus is buffered, so queueing is the bus's job. A send published while a run
// is in flight waits there and its run starts only after the session is idle
// again. The running flag exists so the Service can tell the client that a
// send queued, not to enforce ordering.
type AgentEndpoint struct {
	publisher InboundPublisher

	sessionsMu sync.Mutex
	sessions   map[string]*logicalSession
	nextID     atomic.Uint64
}

// NewAgentEndpoint constructs the bridge agent endpoint over one inbound
// publisher.
func NewAgentEndpoint(publisher InboundPublisher) *AgentEndpoint {
	return &AgentEndpoint{
		publisher: publisher,
		sessions:  make(map[string]*logicalSession),
	}
}

// Channel returns the transport name handled by this endpoint.
func (e *AgentEndpoint) Channel() string {
	return bus.ChannelBridge
}

// OpenSession resolves the logical session for one inbound bus message and
// marks it running for the run the worker starts next. SendMessage publishes
// with a transient session id separate from the conversation, so msg.SessionID
// is the join key. A message
// for an unknown session resolves to a nil session, which the worker skips.
func (e *AgentEndpoint) OpenSession(
	_ context.Context,
	msg bus.InboundMessage,
) (channelport.AgentSession, error) {
	session := e.lookupSession(strings.TrimSpace(msg.SessionID))
	if session == nil {
		return nil, nil
	}
	session.startRun()
	return &runSession{session: session}, nil
}

// NewSession allocates and registers a logical session for the OpenSession
// rpc. Empty chat ids select defaultBridgeChatID. The id scheme mirrors
// systems/exec/internal/service/manager.go: a sess-<n> id drawn from one
// atomic counter. The two registries live in different processes, so their
// namespaces never meet on the wire.
func (e *AgentEndpoint) NewSession(chatID string) *chatpb.Session {
	return e.newSession(chatID).snapshot()
}

// newSession allocates and registers one logical session. NewSession wraps it
// for the rpc and returns the contract view; in-package tests use this when
// they need the session itself rather than its view.
func (e *AgentEndpoint) newSession(chatID string) *logicalSession {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		chatID = defaultBridgeChatID
	}
	session := &logicalSession{
		id:       fmt.Sprintf("sess-%d", e.nextID.Add(1)),
		chatID:   chatID,
		openedAt: time.Now().UTC(),
		events:   newSessionEventLog(),
	}
	// The log opens on the session opening, so the first entry a WatchEvents
	// client replays is the session it subscribed to and the event indexes
	// start at 1 there.
	session.appendEvent(&chatpb.SessionEvent{
		OccurredAt: timestamppb.New(session.openedAt),
		Event: &chatpb.SessionEvent_SessionOpened{SessionOpened: &chatpb.SessionOpened{
			SessionId: session.id,
		}},
	})
	e.sessionsMu.Lock()
	defer e.sessionsMu.Unlock()
	e.sessions[session.id] = session
	return session
}

// lookupSession returns the logical session registered under the id, or nil
// when it was never opened. Sessions are never removed because the contract
// has no close rpc, so every entry lives as long as the process does.
func (e *AgentEndpoint) lookupSession(sessionID string) *logicalSession {
	e.sessionsMu.Lock()
	defer e.sessionsMu.Unlock()
	return e.sessions[sessionID]
}

// SessionSnapshot returns the session's contract view, or nil when the session
// id is unknown. Reading it exists for assertions while driving the endpoint
// through the real worker; the rpcs return snapshots of their own.
func (e *AgentEndpoint) SessionSnapshot(sessionID string) *chatpb.Session {
	session := e.lookupSession(sessionID)
	if session == nil {
		return nil
	}
	return session.snapshot()
}

// publishSend keeps the stable owner and conversation separate from the
// transient session used to route the worker's reply stream.
func (e *AgentEndpoint) publishSend(
	ctx context.Context,
	session *logicalSession,
	clientMsgID string,
	text string,
) error {
	return e.publisher.PublishInbound(ctx, bus.InboundMessage{
		Channel:   bus.ChannelBridge,
		ChatID:    session.chatID,
		SessionID: session.id,
		UserID:    bridgeOwnerID,
		MessageID: clientMsgID,
		SentAt:    time.Now(),
		Text:      text,
	})
}

var _ channelport.AgentEndpoint = (*AgentEndpoint)(nil)

// logicalSession is one open bridge conversation. At most one run is in
// flight: the worker is sequential, so extra sends queue on the bus until the
// session is idle again.
type logicalSession struct {
	id       string
	chatID   string
	openedAt time.Time
	// events is the session's run event stream, the log WatchEvents serves
	// and every run session appends to. It is allocated with the session and
	// its first entry is the session opening below.
	events *sessionEventLog

	mu sync.Mutex
	// running is true between the worker's OpenSession call and the run
	// session's Finish or Abort.
	running bool
	// cancel is the in-flight run's cancel func, recorded by the run session's
	// SetCancel. The Service's Abort rpc invokes it. Finished runs clear it,
	// so a Stop during the next run's startup is remembered for SetCancel.
	cancel context.CancelFunc
	// abortRequested records an Abort that arrived between the worker marking
	// the session running and handing over the run's cancel func. setCancel
	// honours it, so a client's stop is not dropped in that window.
	abortRequested bool
	// reply records the finished run's reply. Nothing publishes it beyond
	// the terminal event the stream carries; Deliver is a later layer, and
	// recording is this layer's test seam.
	reply agent.ReplyResult
}

// snapshot returns the session's contract view.
func (s *logicalSession) snapshot() *chatpb.Session {
	s.mu.Lock()
	defer s.mu.Unlock()

	state := chatpb.SessionState_SESSION_STATE_IDLE
	if s.running {
		state = chatpb.SessionState_SESSION_STATE_RUNNING
	}
	session := &chatpb.Session{
		SessionId: s.id,
		ChatId:    s.chatID,
		State:     state,
	}
	if !s.openedAt.IsZero() {
		session.OpenedAt = timestamppb.New(s.openedAt)
	}
	return session
}

// startRun marks the session running for the run the worker is about to start.
func (s *logicalSession) startRun() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running = true
	s.cancel = nil
	s.abortRequested = false
}

// isRunning reports whether a run is in flight. The rpcs read it to report
// queueing, not to enforce it: the bus and the sequential worker do that.
func (s *logicalSession) isRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// setCancel records the in-flight run's cancel func.
func (s *logicalSession) setCancel(cancel context.CancelFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancel = cancel
	if s.abortRequested {
		// An Abort landed before this call, while the session was already
		// running but had no cancel func to invoke. Honour it now.
		s.abortRequested = false
		cancel()
	}
}

// cancelRun cancels the in-flight run, if any. With nothing in flight it is a
// no-op.
func (s *logicalSession) cancelRun() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return
	}
	if s.cancel == nil {
		// The worker marked the session running but has not handed over the
		// run's cancel func yet; remember the request so setCancel can honour
		// it rather than dropping the client's stop.
		s.abortRequested = true
		return
	}
	s.cancel()
}

// finish returns the session to idle and records the run's reply for this
// layer's tests.
func (s *logicalSession) finish(result agent.ReplyResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running = false
	s.cancel = nil
	s.abortRequested = false
	s.reply = result
}

// abortRun returns the session to idle after its run was canceled.
func (s *logicalSession) abortRun() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running = false
	s.cancel = nil
	s.abortRequested = false
}

// runSession is the channel-port session for one run of a logical bridge
// session. The worker owns its lifetime: the endpoint's OpenSession returns
// it, and the worker finishes or aborts it when the run ends. The run
// session is also where the stream's pacing lives: it coalesces the engine's
// raw deltas into the event log the web tier watches, and it owns the run's
// one terminal event.
type runSession struct {
	session *logicalSession

	// mu is the only lock the run's stream state needs; the event log keeps
	// its own.
	mu sync.Mutex
	// finished is set once the run has ended, by Finish or Abort. A timer
	// that fires after it, or an event the engine sends in behind them, is
	// harmless by construction: the coalescer and the appends check it.
	finished bool
	// terminalEmitted records whether this run already produced its terminal
	// event. The engine emits run_finished or run_failed on the normal
	// paths, and the worker calls Finish, or Abort after a cancelled run,
	// right after; exactly one of the three ends up emitting it, whichever
	// is first, and the others stand down.
	terminalEmitted bool
	// deltaTimer is the pending flush of coalesced deltas.
	deltaTimer *time.Timer
	// answerPending and reasoningPending are the coalescer's drafts: the text
	// received but not yet flushed. answerSoFar is what the current model
	// attempt has produced, and it is what a Snapshot keyframe carries;
	// reasoning has no keyframe because the contract's Snapshot holds the
	// answer text alone.
	answerPending    string
	reasoningPending string
	answerSoFar      string
	// seq and modelRef are the run's transcript sequence and the most recent
	// model, as the run's events carried them. They are the fields Finish
	// and Abort need to close a missing terminal event with.
	seq      int64
	modelRef string
}

// SetCancel records the run's cancel func on the logical session so the
// Service's Abort rpc can reach it.
func (s *runSession) SetCancel(cancel context.CancelFunc) {
	s.session.setCancel(cancel)
}

// Finish returns the logical session to idle and records the reply, and —
// the gap Finish owns — emits the run's terminal event when the engine
// never did, carrying the final text the reply received. The coalesced
// deltas flush first, so the stream ends in run order.
//
// The synthesised terminal frame is COMPLETED because ReplyResult carries no
// error: the worker folds a Reply error into result.Text before calling
// Finish, so a run that failed before the engine emitted its own terminal
// event is closed as completed with the error text in place of an answer.
// Telling the two apart needs an error on the port itself, which is a
// separate change to the shared channel seam.
func (s *runSession) Finish(_ context.Context, result agent.ReplyResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished {
		// The run already ended once; a second call must not append a
		// second terminal event.
		return
	}
	s.finished = true
	s.flushDeltaBuffersLocked()
	if !s.terminalEmitted {
		s.appendTerminalRunFinishedLocked(result.Text, chatpb.RunStatus_RUN_STATUS_COMPLETED)
	}
	s.session.finish(result)
}

// Abort returns the logical session to idle after its run context was
// canceled, and ends the stream the contract wants every run to end with:
// exactly one terminal event, RUN_STATUS_ABORTED, and whatever full text the
// model had produced when the cancel landed, so a client recovers from one
// message even though nothing ran to completion.
func (s *runSession) Abort(_ context.Context, _ string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished {
		return
	}
	s.finished = true
	s.flushDeltaBuffersLocked()
	if !s.terminalEmitted {
		s.appendTerminalRunFinishedLocked(s.answerSoFar, chatpb.RunStatus_RUN_STATUS_ABORTED)
	}
	s.session.abortRun()
}

var _ channelport.CancellableAgentSession = (*runSession)(nil)
