package app

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/q15co/q15/libs/chat-contract/chatpb"
	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/bus"
	"github.com/q15co/q15/systems/agent/internal/channel/bridge"
	"github.com/q15co/q15/systems/agent/internal/conversation"
)

// waitBridgeSessionState polls the endpoint's session snapshot until the run
// driver reached the wanted state. The rpcs return snapshots of their own, so
// this read mirrors what clients see.
func waitBridgeSessionState(
	t *testing.T,
	endpoint *bridge.AgentEndpoint,
	sessionID string,
	want chatpb.SessionState,
) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := endpoint.SessionSnapshot(sessionID).GetState(); got == want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("session %q did not reach %v in time", sessionID, want)
}

// TestRunAgentWorkerDrivesBridgeRunLifecycle pins the run lifecycle through
// the real worker: the worker's endpoint call marks the logical session
// running, the run session carries the run, and Finish returns the session to
// idle so the next queued send starts a fresh run in the same lifecycle.
func TestRunAgentWorkerDrivesBridgeRunLifecycle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	messageBus := bus.New(8)
	endpoint := bridge.NewAgentEndpoint(messageBus)
	// The transcript pager is not touched on the run path, so the lister is
	// left nil on purpose.
	service := bridge.NewService(nil, endpoint, nil)

	var replyCalls atomic.Int64
	release := make(chan struct{})
	release2 := make(chan struct{})
	agentImpl := &fakeObservedAgent{
		reply: func(ctx context.Context, _ conversation.Message, observer agent.RunObserver) (agent.ReplyResult, error) {
			if replyCalls.Add(1) == 2 {
				<-release2
				return agent.ReplyResult{Text: "second done"}, nil
			}
			observer.OnRunEvent(ctx, agent.RunEvent{Type: agent.RunEventRunStarted})
			<-release
			return agent.ReplyResult{Text: "first done"}, nil
		},
	}

	done := make(chan error, 1)
	go func() {
		done <- runAgentWorker(ctx, messageBus, agentImpl, endpoint)
	}()

	opened, err := service.OpenSession(ctx, &chatpb.OpenSessionRequest{ChatId: "conv-1"})
	if err != nil {
		t.Fatalf("OpenSession() error = %v", err)
	}
	sessionID := opened.GetSession().GetSessionId()
	if opened.GetSession().GetState() != chatpb.SessionState_SESSION_STATE_IDLE {
		t.Fatalf(
			"OpenSession() state = %v, want SESSION_STATE_IDLE",
			opened.GetSession().GetState(),
		)
	}

	if _, err := service.SendMessage(ctx, &chatpb.SendMessageRequest{
		SessionId:   sessionID,
		ClientMsgId: "client-1",
		Text:        "first",
	}); err != nil {
		t.Fatalf("SendMessage() error = %v", err)
	}
	// The worker's call to the endpoint must have marked the session running
	// while this run is held open.
	waitBridgeSessionState(t, endpoint, sessionID, chatpb.SessionState_SESSION_STATE_RUNNING)
	if got := agentImpl.Calls(); got != 1 {
		t.Fatalf("Reply() calls = %d, want 1", got)
	}

	// Letting the run finish returns the session to idle through the worker.
	close(release)
	waitBridgeSessionState(t, endpoint, sessionID, chatpb.SessionState_SESSION_STATE_IDLE)
	if got := replyCalls.Load(); got != 1 {
		t.Fatalf("Reply() calls after finish = %d, want 1", got)
	}

	// The second send starts the next run and ends idle again.
	if _, err := service.SendMessage(ctx, &chatpb.SendMessageRequest{
		SessionId:   sessionID,
		ClientMsgId: "client-2",
		Text:        "second",
	}); err != nil {
		t.Fatalf("SendMessage() second error = %v", err)
	}
	waitBridgeSessionState(t, endpoint, sessionID, chatpb.SessionState_SESSION_STATE_RUNNING)
	close(release2)
	waitBridgeSessionState(t, endpoint, sessionID, chatpb.SessionState_SESSION_STATE_IDLE)
	if got := replyCalls.Load(); got != 2 {
		t.Fatalf("Reply() calls = %d, want 2", got)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runAgentWorker() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for worker exit")
	}
}

// TestRunAgentWorkerAbortCancelsBridgeRun drives abort through the whole seam:
// the worker records its cancel func on the session, the rpc's Abort invokes
// it, the canceled run context ends the held Reply, and the worker aborts the
// run session so the end state is idle.
func TestRunAgentWorkerAbortCancelsBridgeRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	messageBus := bus.New(8)
	endpoint := bridge.NewAgentEndpoint(messageBus)
	service := bridge.NewService(nil, endpoint, nil)

	cancelNoticed := make(chan struct{}, 1)
	agentImpl := &fakeObservedAgent{
		reply: func(ctx context.Context, _ conversation.Message, _ agent.RunObserver) (agent.ReplyResult, error) {
			// This run only ever ends through the cancel func the rpc's
			// Abort reached, so returning here proves the cancel fired.
			<-ctx.Done()
			if ctx.Err() == context.Canceled {
				cancelNoticed <- struct{}{}
			}
			return agent.ReplyResult{}, ctx.Err()
		},
	}

	done := make(chan error, 1)
	go func() {
		done <- runAgentWorker(ctx, messageBus, agentImpl, endpoint)
	}()

	opened, err := service.OpenSession(ctx, &chatpb.OpenSessionRequest{ChatId: "conv-1"})
	if err != nil {
		t.Fatalf("OpenSession() error = %v", err)
	}
	sessionID := opened.GetSession().GetSessionId()
	if _, err := service.SendMessage(ctx, &chatpb.SendMessageRequest{
		SessionId:   sessionID,
		ClientMsgId: "client-1",
		Text:        "hello",
	}); err != nil {
		t.Fatalf("SendMessage() error = %v", err)
	}
	waitBridgeSessionState(t, endpoint, sessionID, chatpb.SessionState_SESSION_STATE_RUNNING)

	aborted, err := service.Abort(ctx, &chatpb.AbortRequest{SessionId: sessionID})
	if err != nil {
		t.Fatalf("Abort() error = %v", err)
	}
	if got := aborted.GetSession().GetSessionId(); got != sessionID {
		t.Fatalf("Abort() session id = %q, want %q", got, sessionID)
	}

	select {
	case <-cancelNoticed:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the canceled run to return")
	}
	waitBridgeSessionState(t, endpoint, sessionID, chatpb.SessionState_SESSION_STATE_IDLE)

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runAgentWorker() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for worker exit")
	}
}

// TestRunAgentWorkerQueuesSecondBridgeSendBehindFirstRun drives two sends
// through the real worker. The first run is held open, so the second send's
// rpc must report queued and its run may only start once the first run
// finished. That ordering belongs to the sequential worker plus the buffered
// bus, and the endpoint alone cannot prove it, which is why this test runs
// runAgentWorker instead of driving the endpoint by hand. It lives in this
// package because the worker is unexported here and the bridge package cannot
// import it back.
func TestRunAgentWorkerQueuesSecondBridgeSendBehindFirstRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	messageBus := bus.New(8)
	endpoint := bridge.NewAgentEndpoint(messageBus)
	service := bridge.NewService(nil, endpoint, nil)

	var firstFinished atomic.Bool
	release := make(chan struct{})
	var replyCalls atomic.Int64
	agentImpl := &fakeObservedAgent{
		reply: func(_ context.Context, _ conversation.Message, _ agent.RunObserver) (agent.ReplyResult, error) {
			if replyCalls.Add(1) == 2 {
				// The second run started, so the first run's Finish must
				// already have returned the session to idle.
				if !firstFinished.Load() {
					t.Error("second bridge run started before the first finished")
				}
				return agent.ReplyResult{Text: "second"}, nil
			}
			<-release
			firstFinished.Store(true)
			return agent.ReplyResult{Text: "first"}, nil
		},
	}

	done := make(chan error, 1)
	go func() {
		done <- runAgentWorker(ctx, messageBus, agentImpl, endpoint)
	}()

	opened, err := service.OpenSession(ctx, &chatpb.OpenSessionRequest{ChatId: "conv-1"})
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
	waitBridgeSessionState(t, endpoint, sessionID, chatpb.SessionState_SESSION_STATE_RUNNING)

	second, err := service.SendMessage(ctx, &chatpb.SendMessageRequest{
		SessionId:   sessionID,
		ClientMsgId: "client-2",
		Text:        "second",
	})
	if err != nil {
		t.Fatalf("SendMessage() second error = %v", err)
	}
	if !second.GetQueued() {
		t.Fatal("SendMessage() queued = false, want true while the first run is open")
	}
	if got := agentImpl.Calls(); got != 1 {
		t.Fatalf("Reply() calls while the first run is held = %d, want 1", got)
	}

	// Releasing the first run lets the worker take the queued send.
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && replyCalls.Load() < 2 {
		time.Sleep(2 * time.Millisecond)
	}
	if replyCalls.Load() != 2 {
		t.Fatalf("Reply() calls after release = %d, want 2", replyCalls.Load())
	}
	waitBridgeSessionState(t, endpoint, sessionID, chatpb.SessionState_SESSION_STATE_IDLE)

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runAgentWorker() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for worker exit")
	}
}
