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
	}
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
	// reply records the finished run's reply. Nothing publishes it in this
	// layer; the event stream is the next layer's job.
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
// it, and the worker finishes or aborts it when the run ends.
type runSession struct {
	session *logicalSession
}

// OnRunEvent is a no-op. Translating agent.RunEvent values into the
// chatpb.SessionEvent stream is the next layer; this method exists only
// because the RunObserver seam requires it, and this layer invents no
// buffering for it.
func (s *runSession) OnRunEvent(_ context.Context, _ agent.RunEvent) {}

// SetCancel records the run's cancel func on the logical session so the
// Service's Abort rpc can reach it.
func (s *runSession) SetCancel(cancel context.CancelFunc) {
	s.session.setCancel(cancel)
}

// Finish returns the logical session to idle and records the reply. Recording
// the reply is this layer's test seam; the next layer, which owns the event
// stream, is what publishes the finished run.
func (s *runSession) Finish(_ context.Context, result agent.ReplyResult) {
	s.session.finish(result)
}

// Abort returns the logical session to idle. The worker calls it after the run
// context is already canceled, so there is nothing left to stop here.
func (s *runSession) Abort(_ context.Context, _ string) {
	s.session.abortRun()
}

var _ channelport.CancellableAgentSession = (*runSession)(nil)
