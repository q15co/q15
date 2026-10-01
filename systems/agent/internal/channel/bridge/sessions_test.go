package bridge

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/q15co/q15/libs/chat-contract/chatpb"
	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/bus"
	channelport "github.com/q15co/q15/systems/agent/internal/channel"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakePublisher records every inbound publish, standing in for the bus in
// tests that have no worker running.
type fakePublisher struct {
	mu       sync.Mutex
	messages []bus.InboundMessage
	err      error
}

func (f *fakePublisher) PublishInbound(
	_ context.Context,
	msg bus.InboundMessage,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.messages = append(f.messages, msg)
	return f.err
}

func (f *fakePublisher) published() []bus.InboundMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]bus.InboundMessage(nil), f.messages...)
}

// newTestEndpoint opens the Service under test with the publisher its sends
// land on and returns both, so a test can drive the worker's side of the seam
// by calling OpenSession on the messages the rpcs published.
func newTestEndpoint(publisher *fakePublisher) (*Service, *AgentEndpoint) {
	endpoint := NewAgentEndpoint(publisher)
	return NewService(&fakeTurnLister{}, endpoint), endpoint
}

func TestServiceOpenSessionAllocatesIdleSession(t *testing.T) {
	service, _ := newTestEndpoint(&fakePublisher{})

	response, err := service.OpenSession(
		context.Background(), &chatpb.OpenSessionRequest{ChatId: "conv-7"},
	)
	if err != nil {
		t.Fatalf("OpenSession() error = %v", err)
	}
	session := response.GetSession()
	// The id scheme mirrors systems/exec/internal/service: one counter, one
	// sess-<n> id per allocation.
	if session.GetSessionId() != "sess-1" {
		t.Fatalf("OpenSession() id = %q, want sess-1", session.GetSessionId())
	}
	if session.GetChatId() != "conv-7" {
		t.Fatalf("OpenSession() chat id = %q, want conv-7", session.GetChatId())
	}
	if session.GetState() != chatpb.SessionState_SESSION_STATE_IDLE {
		t.Fatalf(
			"OpenSession() state = %v, want SESSION_STATE_IDLE",
			session.GetState(),
		)
	}

	// The second allocation moves the same counter.
	second, err := service.OpenSession(
		context.Background(), &chatpb.OpenSessionRequest{ChatId: "conv-8"},
	)
	if err != nil {
		t.Fatalf("OpenSession() second error = %v", err)
	}
	if got := second.GetSession().GetSessionId(); got != "sess-2" {
		t.Fatalf("OpenSession() second id = %q, want sess-2", got)
	}
}

// TestServiceOpenSessionDefaultsEmptyChatID pins the default conversation: an
// empty chat_id binds the session to the agent's single transcript, named by
// defaultBridgeChatID because the runtime has exactly one.
func TestServiceOpenSessionDefaultsEmptyChatID(t *testing.T) {
	service, _ := newTestEndpoint(&fakePublisher{})

	response, err := service.OpenSession(
		context.Background(), &chatpb.OpenSessionRequest{},
	)
	if err != nil {
		t.Fatalf("OpenSession() error = %v", err)
	}
	if got := response.GetSession().GetChatId(); got != defaultBridgeChatID {
		t.Fatalf("OpenSession() chat id = %q, want %q", got, defaultBridgeChatID)
	}
}

func TestServiceSendMessagePublishesInboundOnTheBus(t *testing.T) {
	publisher := &fakePublisher{}
	service, _ := newTestEndpoint(publisher)
	ctx := context.Background()

	opened, err := service.OpenSession(
		ctx, &chatpb.OpenSessionRequest{ChatId: "conv-7"},
	)
	if err != nil {
		t.Fatalf("OpenSession() error = %v", err)
	}
	sessionID := opened.GetSession().GetSessionId()

	sent, err := service.SendMessage(ctx, &chatpb.SendMessageRequest{
		SessionId:   sessionID,
		ClientMsgId: "client-1",
		Text:        "hello",
	})
	if err != nil {
		t.Fatalf("SendMessage() error = %v", err)
	}
	if sent.GetClientMsgId() != "client-1" {
		t.Fatalf("SendMessage() client msg id = %q, want client-1",
			sent.GetClientMsgId())
	}
	if sent.GetQueued() {
		t.Fatal("SendMessage() queued = true, want false for the first send")
	}
	if sent.GetSession().GetState() != chatpb.SessionState_SESSION_STATE_IDLE {
		t.Fatalf(
			"SendMessage() state = %v, want SESSION_STATE_IDLE before the worker runs",
			sent.GetSession().GetState(),
		)
	}

	messages := publisher.published()
	if len(messages) != 1 {
		t.Fatalf("published messages = %d, want 1", len(messages))
	}
	published := messages[0]
	// Session routing is separate from the stable conversation and owner.
	if published.Channel != bus.ChannelBridge || published.ChatID != "conv-7" ||
		published.SessionID != sessionID {
		t.Fatalf(
			"published = (channel %q chat %q), want bridge/%s",
			published.Channel,
			published.ChatID,
			sessionID,
		)
	}
	if published.MessageID != "client-1" || published.Text != "hello" {
		t.Fatalf(
			"published = (message %q text %q), want client-1/hello",
			published.MessageID,
			published.Text,
		)
	}
	if published.UserID != bridgeOwnerID {
		t.Fatalf("published user id = %q, want the stable bridge owner", published.UserID)
	}
	if published.SentAt.IsZero() {
		t.Fatal("published SentAt = zero, want the send's timestamp")
	}
}

// TestServiceSendMessageReportsQueuedWhileRunInFlight drives the worker's
// half of the seam by hand: OpenSession is what turns a published message into
// a run, and while that run is in flight the next send must report queued.
func TestServiceSendMessageReportsQueuedWhileRunInFlight(t *testing.T) {
	publisher := &fakePublisher{}
	service, endpoint := newTestEndpoint(publisher)
	ctx := context.Background()

	opened, err := service.OpenSession(
		ctx, &chatpb.OpenSessionRequest{ChatId: "conv-7"},
	)
	if err != nil {
		t.Fatalf("OpenSession() error = %v", err)
	}
	sessionID := opened.GetSession().GetSessionId()
	if _, err := service.SendMessage(ctx, &chatpb.SendMessageRequest{
		SessionId:   sessionID,
		ClientMsgId: "client-1",
		Text:        "first",
	}); err != nil {
		t.Fatalf("SendMessage() first error = %v", err)
	}

	// The worker's call: one inbound message per run, resolved by chat id.
	run, err := endpoint.OpenSession(ctx, publisher.published()[0])
	if err != nil {
		t.Fatalf("OpenSession() worker seam error = %v", err)
	}
	if run == nil {
		t.Fatal("OpenSession() worker seam = nil session, want one")
	}
	if got := endpoint.SessionSnapshot(sessionID).GetState(); got != chatpb.SessionState_SESSION_STATE_RUNNING {
		t.Fatalf("state while running = %v, want SESSION_STATE_RUNNING", got)
	}

	second, err := service.SendMessage(ctx, &chatpb.SendMessageRequest{
		SessionId:   sessionID,
		ClientMsgId: "client-2",
		Text:        "second",
	})
	if err != nil {
		t.Fatalf("SendMessage() second error = %v", err)
	}
	if !second.GetQueued() {
		t.Fatal("SendMessage() queued = false, want true while the run is in flight")
	}
	if got := second.GetSession().GetState(); got != chatpb.SessionState_SESSION_STATE_RUNNING {
		t.Fatalf("second state = %v, want SESSION_STATE_RUNNING", got)
	}
}

// TestServiceSendMessageSurfacesPublishFailure pins the no-swallow rule: a
// failed bus publish is an rpc error, with a dead context mapped onto itself.
func TestServiceSendMessageSurfacesPublishFailure(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name       string
		publishErr error
		want       codes.Code
	}{
		{"generic failure", errors.New("bus closed"), codes.Internal},
		{"canceled context", context.Canceled, codes.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			publisher := &fakePublisher{err: tc.publishErr}
			service, _ := newTestEndpoint(publisher)
			opened, err := service.OpenSession(
				ctx, &chatpb.OpenSessionRequest{ChatId: "conv-7"},
			)
			if err != nil {
				t.Fatalf("OpenSession() error = %v", err)
			}

			_, err = service.SendMessage(ctx, &chatpb.SendMessageRequest{
				SessionId:   opened.GetSession().GetSessionId(),
				ClientMsgId: "client-1",
				Text:        "hello",
			})
			if status.Code(err) != tc.want {
				t.Fatalf(
					"SendMessage() code = %v, want %v (err=%v)",
					status.Code(err),
					tc.want,
					err,
				)
			}
		})
	}
}

// TestBridgeRunSessionLifecycleThroughSeam walks the run lifecycle at the
// endpoint the way the worker drives it: OpenSession marks the logical session
// running, OnRunEvent is a no-op, and Finish or Abort returns it to idle with
// the rpc-reported state following.
func TestBridgeRunSessionLifecycleThroughSeam(t *testing.T) {
	endpoint := NewAgentEndpoint(&fakePublisher{})
	logical := endpoint.newSession("conv-7")

	run, err := endpoint.OpenSession(
		context.Background(),
		bus.InboundMessage{
			Channel:   bus.ChannelBridge,
			SessionID: logical.id,
			Text:      "hello",
		},
	)
	if err != nil {
		t.Fatalf("OpenSession() error = %v", err)
	}
	if _, ok := run.(channelport.CancellableAgentSession); !ok {
		t.Fatalf("OpenSession() session = %T, want a CancellableAgentSession", run)
	}
	if !logical.isRunning() {
		t.Fatal("logical session running = false after OpenSession, want true")
	}
	if got := endpoint.SessionSnapshot(logical.id).GetState(); got != chatpb.SessionState_SESSION_STATE_RUNNING {
		t.Fatalf("state after OpenSession = %v, want SESSION_STATE_RUNNING", got)
	}

	// The event stream lives on this layer now: OnRunEvent coalesces and
	// appends, and must not disturb the session's run state.
	run.OnRunEvent(context.Background(), agent.RunEvent{Type: agent.RunEventRunStarted})
	if got := endpoint.SessionSnapshot(logical.id).GetState(); got != chatpb.SessionState_SESSION_STATE_RUNNING {
		t.Fatalf("state after OnRunEvent = %v, want SESSION_STATE_RUNNING", got)
	}

	run.Finish(context.Background(), agent.ReplyResult{Text: "done"})
	if logical.isRunning() {
		t.Fatal("logical session running = true after Finish, want false")
	}
	if got := endpoint.SessionSnapshot(logical.id).GetState(); got != chatpb.SessionState_SESSION_STATE_IDLE {
		t.Fatalf("state after Finish = %v, want SESSION_STATE_IDLE", got)
	}
	// reply recording is this layer's test seam; Deliver publishes nothing
	// yet, while the stream ends in the run's terminal event.
	if logical.reply.Text != "done" {
		t.Fatalf("recorded reply = %q, want done", logical.reply.Text)
	}

	// The next run goes through the same lifecycle and may abort instead.
	aborted, err := endpoint.OpenSession(
		context.Background(),
		bus.InboundMessage{Channel: bus.ChannelBridge, SessionID: logical.id, Text: "again"},
	)
	if err != nil {
		t.Fatalf("OpenSession() second error = %v", err)
	}
	aborted.Abort(context.Background(), "canceled")
	if logical.isRunning() {
		t.Fatal("logical session running = true after Abort, want false")
	}
	if got := endpoint.SessionSnapshot(logical.id).GetState(); got != chatpb.SessionState_SESSION_STATE_IDLE {
		t.Fatalf("state after Abort = %v, want SESSION_STATE_IDLE", got)
	}
}

// TestAgentEndpointSkipsUnknownSessionMessage pins the worker's skip rule: a
// message for a session that was never opened resolves to no session at all.
func TestAgentEndpointSkipsUnknownSessionMessage(t *testing.T) {
	endpoint := NewAgentEndpoint(&fakePublisher{})

	run, err := endpoint.OpenSession(
		context.Background(),
		bus.InboundMessage{Channel: bus.ChannelBridge, SessionID: "sess-404", Text: "hello"},
	)
	if err != nil {
		t.Fatalf("OpenSession() error = %v", err)
	}
	if run != nil {
		t.Fatalf("OpenSession() = %v, want nil for the worker to skip", run)
	}
}

// TestServiceAbortCancelsRecordedRun pins the RPC half of aborting. The fake
// cancel func replaces the one the worker's SetCancel records; the idle
// transition itself is proven against the real worker in the app package.
func TestServiceAbortCancelsRecordedRun(t *testing.T) {
	publisher := &fakePublisher{}
	service, endpoint := newTestEndpoint(publisher)
	ctx := context.Background()

	opened, err := service.OpenSession(
		ctx, &chatpb.OpenSessionRequest{ChatId: "conv-7"},
	)
	if err != nil {
		t.Fatalf("OpenSession() error = %v", err)
	}
	sessionID := opened.GetSession().GetSessionId()
	if _, err := service.SendMessage(ctx, &chatpb.SendMessageRequest{
		SessionId: sessionID,
		Text:      "first",
	}); err != nil {
		t.Fatalf("SendMessage() error = %v", err)
	}
	run, err := endpoint.OpenSession(ctx, publisher.published()[0])
	if err != nil {
		t.Fatalf("OpenSession() worker seam error = %v", err)
	}
	// The worker asserts this conformance once per run before SetCancel.
	cancelable, ok := run.(channelport.CancellableAgentSession)
	if !ok {
		t.Fatalf("worker seam session = %T, want a CancellableAgentSession", run)
	}

	canceled := 0
	cancelable.SetCancel(func() { canceled++ })

	run.OnRunEvent(ctx, agent.RunEvent{Type: agent.RunEventRunStarted, Seq: 9})
	aborted, err := service.Abort(ctx, &chatpb.AbortRequest{SessionId: sessionID, TurnSeq: 9})
	if err != nil {
		t.Fatalf("Abort() error = %v", err)
	}
	if canceled != 1 {
		t.Fatalf("Abort() cancel calls = %d, want 1", canceled)
	}
	if got := aborted.GetSession().GetSessionId(); got != sessionID {
		t.Fatalf("Abort() session id = %q, want %q", got, sessionID)
	}
	if got := endpoint.SessionSnapshot(sessionID).GetState(); got != chatpb.SessionState_SESSION_STATE_RUNNING {
		t.Fatalf(
			"state right after Abort() = %v, want SESSION_STATE_RUNNING until the worker notices",
			got,
		)
	}
}

// TestServiceAbortWithoutRunInFlightIsNoOp pins the racing-client rule:
// aborting a session whose run already finished returns the session, not an
// error.
func TestServiceAbortWithoutRunInFlightIsNoOp(t *testing.T) {
	service, _ := newTestEndpoint(&fakePublisher{})
	ctx := context.Background()

	opened, err := service.OpenSession(
		ctx, &chatpb.OpenSessionRequest{ChatId: "conv-7"},
	)
	if err != nil {
		t.Fatalf("OpenSession() error = %v", err)
	}
	sessionID := opened.GetSession().GetSessionId()

	aborted, err := service.Abort(ctx, &chatpb.AbortRequest{SessionId: sessionID})
	if err != nil {
		t.Fatalf("Abort() error = %v, want none with nothing in flight", err)
	}
	if got := aborted.GetSession().GetState(); got != chatpb.SessionState_SESSION_STATE_IDLE {
		t.Fatalf("Abort() state = %v, want SESSION_STATE_IDLE", got)
	}
}

func TestServiceSendMessageAndAbortRejectUnknownSessions(t *testing.T) {
	publisher := &fakePublisher{}
	service, _ := newTestEndpoint(publisher)
	ctx := context.Background()

	_, err := service.SendMessage(ctx, &chatpb.SendMessageRequest{
		SessionId: "sess-404",
		Text:      "hello",
	})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("SendMessage() code = %v, want NotFound (err=%v)", status.Code(err), err)
	}

	_, err = service.Abort(ctx, &chatpb.AbortRequest{SessionId: "sess-404"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("Abort() code = %v, want NotFound (err=%v)", status.Code(err), err)
	}
	if len(publisher.published()) != 0 {
		t.Fatalf("published messages = %d, want none", len(publisher.published()))
	}
}

// TestServiceOpenSessionRecordsOpenedAt pins the contract's opened_at field:
// the session carries the time it was opened rather than a zero timestamp.
func TestServiceOpenSessionRecordsOpenedAt(t *testing.T) {
	service := NewService(nil, NewAgentEndpoint(&fakePublisher{}))

	opened, err := service.OpenSession(
		context.Background(),
		&chatpb.OpenSessionRequest{ChatId: "conv-opened-at"},
	)
	if err != nil {
		t.Fatalf("OpenSession() error = %v", err)
	}
	stamp := opened.GetSession().GetOpenedAt()
	if stamp == nil {
		t.Fatal("Session.opened_at = nil, want the open time")
	}
	if age := time.Since(stamp.AsTime()); age < 0 || age > time.Minute {
		t.Fatalf("Session.opened_at is %v from now, want it to be the open time", age)
	}
}

// TestServiceSendMessageRejectsEmptyText covers the silent-drop hole: a send
// with no text produces no message parts, and the worker skips a message with
// no parts. The rpc must reject it rather than report success.
func TestServiceSendMessageRejectsEmptyText(t *testing.T) {
	service := NewService(nil, NewAgentEndpoint(&fakePublisher{}))
	ctx := context.Background()

	opened, err := service.OpenSession(ctx, &chatpb.OpenSessionRequest{ChatId: "conv-empty"})
	if err != nil {
		t.Fatalf("OpenSession() error = %v", err)
	}
	_, err = service.SendMessage(ctx, &chatpb.SendMessageRequest{
		SessionId: opened.GetSession().GetSessionId(),
		Text:      "   ",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("SendMessage() code = %v, want InvalidArgument (err=%v)", status.Code(err), err)
	}
}

// TestServiceAbortBeforeSetCancelIsNotLost covers the window between the
// worker marking a session running and handing the run its cancel func. An
// Abort landing in that window must still stop the run; it used to be dropped
// because there was no cancel func to invoke yet.
func TestServiceAbortBeforeSetCancelIsNotLost(t *testing.T) {
	endpoint := NewAgentEndpoint(&fakePublisher{})
	service := NewService(nil, endpoint)
	ctx := context.Background()

	opened, err := service.OpenSession(ctx, &chatpb.OpenSessionRequest{ChatId: "conv-window"})
	if err != nil {
		t.Fatalf("OpenSession() error = %v", err)
	}
	sessionID := opened.GetSession().GetSessionId()

	run, err := endpoint.OpenSession(ctx, bus.InboundMessage{
		Channel:   bus.ChannelBridge,
		SessionID: sessionID,
		Text:      "hello",
	})
	if err != nil {
		t.Fatalf("endpoint OpenSession() error = %v", err)
	}
	cancellable, ok := run.(channelport.CancellableAgentSession)
	if !ok {
		t.Fatalf("OpenSession() session = %T, want a CancellableAgentSession", run)
	}

	// The worker has marked the session running but not yet reached SetCancel.
	if _, err := service.Abort(ctx, &chatpb.AbortRequest{SessionId: sessionID}); err != nil {
		t.Fatalf("Abort() error = %v", err)
	}

	canceled := false
	cancellable.SetCancel(func() { canceled = true })
	if !canceled {
		t.Fatal("SetCancel did not honour the Abort that arrived before it")
	}
}

func TestServiceAbortBeforeSecondRunSetCancelIsNotLost(t *testing.T) {
	ctx := context.Background()
	service, endpoint := newTestEndpoint(&fakePublisher{})
	logical := endpoint.newSession("conv-7")
	first, err := endpoint.OpenSession(ctx, bus.InboundMessage{SessionID: logical.id})
	if err != nil {
		t.Fatal(err)
	}
	_, firstCancel := context.WithCancel(ctx)
	first.(channelport.CancellableAgentSession).SetCancel(firstCancel)
	first.Finish(ctx, agent.ReplyResult{Text: "first done"})
	firstCancel()
	second, err := endpoint.OpenSession(ctx, bus.InboundMessage{SessionID: logical.id})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Abort(ctx, &chatpb.AbortRequest{SessionId: logical.id}); err != nil {
		t.Fatal(err)
	}
	secondCtx, secondCancel := context.WithCancel(ctx)
	defer secondCancel()
	second.(channelport.CancellableAgentSession).SetCancel(secondCancel)
	if secondCtx.Err() != context.Canceled {
		t.Fatal("the acknowledged Stop did not cancel the second run")
	}
}

func TestBridgeOwnerAndConversationSurviveNewSessions(t *testing.T) {
	ctx := context.Background()
	publisher := &fakePublisher{}
	service, _ := newTestEndpoint(publisher)
	for range 2 {
		opened, err := service.OpenSession(ctx, &chatpb.OpenSessionRequest{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.SendMessage(ctx, &chatpb.SendMessageRequest{SessionId: opened.GetSession().GetSessionId(), Text: "hello"}); err != nil {
			t.Fatal(err)
		}
	}
	messages := publisher.published()
	if messages[0].SessionID == messages[1].SessionID {
		t.Fatal("new sessions reused a routing identity")
	}
	for _, msg := range messages {
		if msg.ChatID != defaultBridgeChatID || msg.UserID != bridgeOwnerID {
			t.Fatalf(
				"conversation/owner = %q/%q, want stable default/owner",
				msg.ChatID,
				msg.UserID,
			)
		}
	}
}

func TestServiceAbortTargetsOnlyTheNamedActiveRun(t *testing.T) {
	for _, tc := range []struct {
		name      string
		requested int64
		wantAbort bool
	}{
		{name: "stale", requested: 41},
		{name: "future", requested: 43},
		{name: "active", requested: 42, wantAbort: true},
		{name: "current", wantAbort: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, endpoint := newTestEndpoint(&fakePublisher{})
			logical := endpoint.newSession("")
			first := startTestRun(t, endpoint, logical.id)
			first.OnRunEvent(
				context.Background(),
				agent.RunEvent{Type: agent.RunEventRunStarted, Seq: 41},
			)
			first.Finish(context.Background(), agent.ReplyResult{})

			second := startTestRun(t, endpoint, logical.id)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			second.SetCancel(cancel)
			second.OnRunEvent(ctx, agent.RunEvent{Type: agent.RunEventRunStarted, Seq: 42})
			if _, err := service.Abort(ctx, &chatpb.AbortRequest{
				SessionId: logical.id, TurnSeq: tc.requested,
			}); err != nil {
				t.Fatal(err)
			}
			if got := ctx.Err() != nil; got != tc.wantAbort {
				t.Fatalf(
					"Abort(%d) canceled active turn 42 = %v, want %v",
					tc.requested,
					got,
					tc.wantAbort,
				)
			}
			second.Abort(context.Background(), "test cleanup")
		})
	}
}

func TestServiceStaleAbortDoesNotLatchDuringNextRunStartup(t *testing.T) {
	service, endpoint := newTestEndpoint(&fakePublisher{})
	logical := endpoint.newSession("")
	first := startTestRun(t, endpoint, logical.id)
	first.OnRunEvent(context.Background(), agent.RunEvent{Type: agent.RunEventRunStarted, Seq: 41})
	first.Abort(context.Background(), "first stopped")

	second := startTestRun(t, endpoint, logical.id)
	if _, err := service.Abort(context.Background(), &chatpb.AbortRequest{
		SessionId: logical.id, TurnSeq: 41,
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	second.SetCancel(cancel)
	if ctx.Err() != nil {
		t.Fatal("stale Stop was remembered for the next run's cancel function")
	}
	second.Abort(context.Background(), "test cleanup")
}
