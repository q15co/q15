package bridge

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/q15co/q15/libs/chat-contract/chatpb"
	"github.com/q15co/q15/systems/agent/internal/agent"
)

// These tests pin the defects the layer's two-axis review found. Each one
// fails against the implementation the review was shown, and each covers a
// path the rest of the suite does not reach.

// TestServiceWatchEventsKeyframesAStreamThatNeverPauses is the case a trailing
// debounce cannot serve. The rest of the suite pushes bursts that end, and a
// debounce still fires once the stream goes quiet, so those bursts cannot tell
// the two behaviours apart. Here the deltas never pause for a whole interval,
// and the keyframe has to arrive while the stream is still running.
func TestServiceWatchEventsKeyframesAStreamThatNeverPauses(t *testing.T) {
	publisher := &fakePublisher{}
	endpoint := NewAgentEndpoint(publisher)
	client := startEventBridgeClient(t, NewService(&fakeTurnLister{}, endpoint, nil))
	ctx := context.Background()

	opened, err := client.OpenSession(ctx, &chatpb.OpenSessionRequest{ChatId: "conv-relentless"})
	if err != nil {
		t.Fatalf("OpenSession() error = %v", err)
	}
	sessionID := opened.GetSession().GetSessionId()
	run := startTestRun(t, endpoint, sessionID)

	run.OnRunEvent(ctx, agent.RunEvent{Type: agent.RunEventRunStarted, Seq: 5})
	watch, err := client.WatchEvents(ctx, &chatpb.WatchEventsRequest{SessionId: sessionID})
	if err != nil {
		t.Fatalf("WatchEvents() error = %v", err)
	}
	for _, wantMember := range []string{"session_opened", "run_started"} {
		if got := mustRecvWatchFrame(t, watch); frameMember(t, got) != wantMember {
			t.Fatalf("opening frame = %v, want %v", frameMember(t, got), wantMember)
		}
	}

	// Push well inside every interval, for far longer than the deadline
	// below, so the stream never goes quiet long enough for a debounce to
	// fire on its own.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		ticker := time.NewTicker(bridgeDeltaFlushInterval / 10)
		defer ticker.Stop()
		for i := 0; i < 60; i++ {
			select {
			case <-stop:
				return
			case <-ticker.C:
				run.OnRunEvent(ctx, agent.RunEvent{
					Type:  agent.RunEventModelTurnDelta,
					Seq:   5,
					Delta: "tok ",
				})
			}
		}
	}()

	frames := make(chan *chatpb.SessionEvent, 64)
	recvErr := make(chan error, 1)
	go func() {
		for {
			frame, err := watch.Recv()
			if err != nil {
				recvErr <- err
				return
			}
			frames <- frame.GetEvent()
		}
	}()

	// The deadline sits well inside the push window: a paced flush keyframes
	// the stream after one interval, while a debounce would not emit until
	// the stream finally paused.
	deadline := time.NewTimer(3 * bridgeDeltaFlushInterval)
	defer deadline.Stop()
	sawKeyframe := false
	for !sawKeyframe {
		select {
		case <-deadline.C:
			t.Fatal(
				"no Snapshot keyframe arrived while the delta stream was still running: " +
					"the flush is being re-aimed by every delta instead of paced by the interval",
			)
		case err := <-recvErr:
			t.Fatalf("WatchEvents Recv() error = %v", err)
		case frame := <-frames:
			if snapshot := frame.GetSnapshot(); snapshot != nil {
				if snapshot.GetText() == "" {
					t.Fatal("Snapshot keyframe carries no text")
				}
				sawKeyframe = true
			}
		}
	}
}

// TestRunSessionDeltaFlushIsPacedNotReAimed is the same property without the
// clock: the first pending delta arms the flush and later deltas leave that
// exact timer standing. Re-arming it would be a trailing debounce, which emits
// nothing at all while a stream keeps talking.
func TestRunSessionDeltaFlushIsPacedNotReAimed(t *testing.T) {
	session := NewAgentEndpoint(&fakePublisher{}).newSession("conv-paced")
	run := &runSession{session: session}
	ctx := context.Background()

	run.OnRunEvent(ctx, agent.RunEvent{Type: agent.RunEventModelTurnDelta, Seq: 3, Delta: "first "})
	armed := run.deltaTimer
	if armed == nil {
		t.Fatal("deltaTimer = nil after the first delta, want an armed flush")
	}

	for i := 0; i < 20; i++ {
		run.OnRunEvent(ctx, agent.RunEvent{
			Type:  agent.RunEventModelTurnDelta,
			Seq:   3,
			Delta: fmt.Sprintf("tok%d ", i),
		})
		if run.deltaTimer != armed {
			t.Fatalf("delta %d re-armed the flush, want the first delta's timer still standing", i)
		}
	}

	run.mu.Lock()
	defer run.mu.Unlock()
	if run.deltaTimer != nil {
		run.deltaTimer.Stop()
		run.deltaTimer = nil
	}
}

// TestSessionEventLogResyncNoticeSortsBelowTheReplayedWindow pins the fix for
// a Notice that took a fresh index. It then outranked every frame replayed
// after it, so a client acknowledging it skipped the very history the Notice
// introduced, and the counter burned a value no event would ever fill.
func TestSessionEventLogResyncNoticeSortsBelowTheReplayedWindow(t *testing.T) {
	session := NewAgentEndpoint(&fakePublisher{}).newSession("conv-resync")

	// Fill past the retention window so the oldest events are evicted. The
	// session opening took index 1, so the loop's events take 2 onwards and
	// the next append after all of this is total+2.
	total := int64(sessionEventRetention + 40)
	for i := int64(0); i < total; i++ {
		session.appendEvent(&chatpb.SessionEvent{
			Event: &chatpb.SessionEvent_RunStarted{RunStarted: &chatpb.RunStarted{}},
		})
	}

	subscriber, replay := session.events.subscribe(0)
	defer session.events.unsubscribe(subscriber)

	if len(replay) < 2 || replay[0].GetNotice() == nil {
		t.Fatalf("replay from a stale cursor = %d frames, want the Notice first", len(replay))
	}
	noticeIndex := replay[0].GetEventIndex()
	firstReplayed := replay[1].GetEventIndex()
	if noticeIndex >= firstReplayed {
		t.Fatalf(
			"notice index = %d, want it below the window's first replayed frame %d",
			noticeIndex, firstReplayed,
		)
	}
	for i := 1; i < len(replay); i++ {
		if replay[i].GetEventIndex() != firstReplayed+int64(i-1) {
			t.Fatalf(
				"replayed frame %d carries index %d, want %d: the window must stay contiguous",
				i, replay[i].GetEventIndex(), firstReplayed+int64(i-1),
			)
		}
	}

	// The Notice must not consume a counter value, so the next append
	// continues the sequence with no hole. A burned value here would leave
	// every later client a cursor it can never match.
	session.appendEvent(&chatpb.SessionEvent{
		Event: &chatpb.SessionEvent_RunStarted{RunStarted: &chatpb.RunStarted{}},
	})
	retained := session.events.retained()
	if got := retained[len(retained)-1].GetEventIndex(); got != total+2 {
		t.Fatalf("next appended index = %d, want %d with no burned value", got, total+2)
	}
}

// TestRunEventMappingClassifiesACancelledRunAsAborted pins the fix for the
// abort status. The engine reports every error through run_failed, including
// the context cancellation a client's Abort produces, so a stopped run used to
// reach the UI as a failure rather than as the contract's abort.
func TestRunEventMappingClassifiesACancelledRunAsAborted(t *testing.T) {
	cancelled := runEventToSessionEvent(agent.RunEvent{
		Type: agent.RunEventRunFailed,
		Err:  context.Canceled,
	})
	if got := cancelled.GetRunFailed().GetStatus(); got != chatpb.RunStatus_RUN_STATUS_ABORTED {
		t.Fatalf("cancelled run status = %v, want RUN_STATUS_ABORTED", got)
	}

	failed := runEventToSessionEvent(agent.RunEvent{
		Type: agent.RunEventRunFailed,
		Err:  errors.New("model unavailable"),
	})
	if got := failed.GetRunFailed().GetStatus(); got != chatpb.RunStatus_RUN_STATUS_FAILED {
		t.Fatalf("failed run status = %v, want RUN_STATUS_FAILED", got)
	}
}
