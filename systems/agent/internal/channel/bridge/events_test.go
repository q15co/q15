package bridge

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/q15co/q15/libs/chat-contract/chatpb"
	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/bus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// allRunEventTypes is every RunEventType constant, in the order
// run_events.go:15 to run_events.go:22 declares them. The golden mapping test
// walks it and fails when a constant has no row, so a constant added to the
// engine cannot skip the contract mapping unnoticed.
var allRunEventTypes = []agent.RunEventType{
	agent.RunEventRunStarted,          // run_events.go:15
	agent.RunEventModelTurnStarted,    // run_events.go:16
	agent.RunEventModelTurnDelta,      // run_events.go:17
	agent.RunEventModelReasoningDelta, // run_events.go:18
	agent.RunEventToolStarted,         // run_events.go:19
	agent.RunEventToolFinished,        // run_events.go:20
	agent.RunEventRunFinished,         // run_events.go:21
	agent.RunEventRunFailed,           // run_events.go:22
}

// startEventBridgeClient serves one already-built service over a bufconn and
// returns its client. It is the streaming sibling of startBridgeService: the
// event tests need to drive the endpoint the worker drives, so they build the
// endpoint themselves instead of letting the helper own it.
func startEventBridgeClient(t *testing.T, service *Service) chatpb.ChatServiceClient {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	chatpb.RegisterChatServiceServer(server, service)
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(server.Stop)

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return listener.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient() error = %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
	})
	return chatpb.NewChatServiceClient(conn)
}

// recvWatchFrame reads one stream frame on a deadline so a missing or extra
// frame fails the test instead of hanging it. A returned error is a real
// stream end, not a test failure with a name.
func recvWatchFrame(
	t *testing.T,
	stream chatpb.ChatService_WatchEventsClient,
) (*chatpb.SessionEvent, error) {
	t.Helper()
	type frame struct {
		event *chatpb.SessionEvent
		err   error
	}
	done := make(chan frame, 1)
	go func() {
		response, err := stream.Recv()
		if err != nil {
			done <- frame{err: err}
			return
		}
		done <- frame{event: response.GetEvent()}
	}()
	select {
	case got := <-done:
		return got.event, got.err
	case <-time.After(2 * time.Second):
		t.Fatal("WatchEvents Recv() timed out waiting for a frame")
		return nil, nil
	}
}

// mustRecvWatchFrame is recvWatchFrame for a frame the test knows must come.
func mustRecvWatchFrame(
	t *testing.T,
	stream chatpb.ChatService_WatchEventsClient,
) *chatpb.SessionEvent {
	t.Helper()
	event, err := recvWatchFrame(t, stream)
	if err != nil {
		t.Fatalf("WatchEvents Recv() error = %v, want a frame", err)
	}
	return event
}

// assertEventClock pins the two fields every mapped event carries: the event
// time and the transcript turn sequence the run reserved.
func assertEventClock(t *testing.T, event *chatpb.SessionEvent, at time.Time, seq int64) {
	t.Helper()
	if event.GetTurnSeq() != seq {
		t.Fatalf(
			"%s turn_seq = %d, want %d",
			frameMember(t, event),
			event.GetTurnSeq(),
			seq,
		)
	}
	if !event.GetOccurredAt().AsTime().Equal(at) {
		t.Fatalf(
			"%s occurred_at = %v, want %v",
			frameMember(t, event),
			event.GetOccurredAt().AsTime(),
			at,
		)
	}
}

// frameMember names the oneof member a frame carries, for failure output.
func frameMember(t *testing.T, event *chatpb.SessionEvent) string {
	t.Helper()
	switch event.GetEvent().(type) {
	case *chatpb.SessionEvent_SessionOpened:
		return "session_opened"
	case *chatpb.SessionEvent_RunStarted:
		return "run_started"
	case *chatpb.SessionEvent_ModelTurnStarted:
		return "model_turn_started"
	case *chatpb.SessionEvent_ModelTurnDelta:
		return "model_turn_delta"
	case *chatpb.SessionEvent_ModelReasoningDelta:
		return "model_reasoning_delta"
	case *chatpb.SessionEvent_ToolStarted:
		return "tool_started"
	case *chatpb.SessionEvent_ToolFinished:
		return "tool_finished"
	case *chatpb.SessionEvent_RunFinished:
		return "run_finished"
	case *chatpb.SessionEvent_RunFailed:
		return "run_failed"
	case *chatpb.SessionEvent_Snapshot:
		return "snapshot"
	case *chatpb.SessionEvent_Notice:
		return "notice"
	default:
		return fmt.Sprintf("unknown oneof member %T", event.GetEvent())
	}
}

// TestRunEventMappingCoversEveryRunEventType is the golden mapping table:
// one row for each of the eight RunEventType constants of
// run_events.go:15..22, naming the constant and its source line and pinning
// the SessionEvent oneof member the contract receives for it. Eight entries,
// not seven: a seventh-row world means an event type streams as nothing.
func TestRunEventMappingCoversEveryRunEventType(t *testing.T) {
	eventAt := time.Date(2026, time.April, 9, 10, 15, 0, 0, time.UTC)
	rows := map[agent.RunEventType]struct {
		source string
		event  agent.RunEvent
		assert func(t *testing.T, got *chatpb.SessionEvent)
	}{
		agent.RunEventRunStarted: {
			source: "run_events.go:15",
			event:  agent.RunEvent{Type: agent.RunEventRunStarted, At: eventAt, Seq: 7},
			assert: func(t *testing.T, got *chatpb.SessionEvent) {
				if got.GetRunStarted() == nil {
					t.Fatalf("run_started mapped to %v, want RunStarted", frameMember(t, got))
				}
				assertEventClock(t, got, eventAt, 7)
			},
		},
		agent.RunEventModelTurnStarted: {
			source: "run_events.go:16",
			event: agent.RunEvent{
				Type:     agent.RunEventModelTurnStarted,
				At:       eventAt,
				Turn:     2,
				ModelRef: "claude-x",
				Seq:      7,
			},
			assert: func(t *testing.T, got *chatpb.SessionEvent) {
				started := got.GetModelTurnStarted()
				if started == nil {
					t.Fatalf(
						"model_turn_started mapped to %v, want ModelTurnStarted",
						frameMember(t, got),
					)
				}
				if started.GetLoopTurn() != 2 {
					t.Fatalf("loop_turn = %d, want 2", started.GetLoopTurn())
				}
				if started.GetModelRef() != "claude-x" {
					t.Fatalf("model_ref = %q, want claude-x", started.GetModelRef())
				}
				assertEventClock(t, got, eventAt, 7)
			},
		},
		agent.RunEventModelTurnDelta: {
			source: "run_events.go:17",
			event:  agent.RunEvent{Type: agent.RunEventModelTurnDelta, At: eventAt, Seq: 7, Delta: "hello"},
			assert: func(t *testing.T, got *chatpb.SessionEvent) {
				if got.GetModelTurnDelta() == nil {
					t.Fatalf(
						"model_turn_delta mapped to %v, want ModelTurnDelta",
						frameMember(t, got),
					)
				}
				if delta := got.GetModelTurnDelta().GetDelta(); delta != "hello" {
					t.Fatalf("delta = %q, want hello", delta)
				}
				assertEventClock(t, got, eventAt, 7)
			},
		},
		agent.RunEventModelReasoningDelta: {
			source: "run_events.go:18",
			event: agent.RunEvent{
				Type:  agent.RunEventModelReasoningDelta,
				At:    eventAt,
				Seq:   7,
				Delta: "because",
			},
			assert: func(t *testing.T, got *chatpb.SessionEvent) {
				if got.GetModelReasoningDelta() == nil {
					t.Fatalf(
						"model_reasoning_delta mapped to %v, want ModelReasoningDelta",
						frameMember(t, got),
					)
				}
				if delta := got.GetModelReasoningDelta().GetDelta(); delta != "because" {
					t.Fatalf("reasoning delta = %q, want because", delta)
				}
				assertEventClock(t, got, eventAt, 7)
			},
		},
		agent.RunEventToolStarted: {
			source: "run_events.go:19",
			event: agent.RunEvent{
				Type:     agent.RunEventToolStarted,
				At:       eventAt,
				Seq:      7,
				ToolCall: agent.ToolCall{ID: "call-1", Name: "read_file", Arguments: "{}"},
			},
			assert: func(t *testing.T, got *chatpb.SessionEvent) {
				started := got.GetToolStarted()
				if started == nil {
					t.Fatalf("tool_started mapped to %v, want ToolStarted", frameMember(t, got))
				}
				if started.GetCall().GetId() != "call-1" ||
					started.GetCall().GetName() != "read_file" ||
					started.GetCall().GetArguments() != "{}" {
					t.Fatalf(
						"started call = %v, want call-1/read_file",
						started.GetCall(),
					)
				}
				assertEventClock(t, got, eventAt, 7)
			},
		},
		agent.RunEventToolFinished: {
			source: "run_events.go:20",
			event: agent.RunEvent{
				Type:       agent.RunEventToolFinished,
				At:         eventAt,
				Seq:        7,
				ToolCall:   agent.ToolCall{ID: "call-1", Name: "read_file"},
				ToolOutput: "tool error: boom",
				Err:        fmt.Errorf("boom"),
			},
			assert: func(t *testing.T, got *chatpb.SessionEvent) {
				finished := got.GetToolFinished()
				if finished == nil {
					t.Fatalf(
						"tool_finished mapped to %v, want ToolFinished",
						frameMember(t, got),
					)
				}
				if finished.GetCall().GetName() != "read_file" {
					t.Fatalf("finished call = %v, want read_file", finished.GetCall())
				}
				if finished.GetOutput() != "tool error: boom" {
					t.Fatalf("output = %q, want the folded tool error text",
						finished.GetOutput())
				}
				if !finished.GetIsError() {
					t.Fatal("is_error = false, want true for a failed tool invocation")
				}
				assertEventClock(t, got, eventAt, 7)
				// The same row without an engine error maps to a clean
				// result: is_error follows the one signal the engine sets.
				clean := runEventToSessionEvent(agent.RunEvent{
					Type:       agent.RunEventToolFinished,
					ToolCall:   agent.ToolCall{ID: "call-2", Name: "exec"},
					ToolOutput: "ok",
				})
				if clean.GetToolFinished().GetIsError() {
					t.Fatal("clean tool_finished is_error = true, want false")
				}
			},
		},
		agent.RunEventRunFinished: {
			source: "run_events.go:21",
			event: agent.RunEvent{
				Type:      agent.RunEventRunFinished,
				At:        eventAt,
				Seq:       7,
				ModelRef:  "claude-x",
				FinalText: "done",
			},
			assert: func(t *testing.T, got *chatpb.SessionEvent) {
				finished := got.GetRunFinished()
				if finished == nil {
					t.Fatalf("run_finished mapped to %v, want RunFinished", frameMember(t, got))
				}
				if finished.GetStatus() != chatpb.RunStatus_RUN_STATUS_COMPLETED {
					t.Fatalf("status = %v, want RUN_STATUS_COMPLETED", finished.GetStatus())
				}
				if finished.GetFullText() != "done" || finished.GetModelRef() != "claude-x" {
					t.Fatalf(
						"terminal = (text %q ref %q), want done/claude-x",
						finished.GetFullText(),
						finished.GetModelRef(),
					)
				}
				assertEventClock(t, got, eventAt, 7)
			},
		},
		agent.RunEventRunFailed: {
			source: "run_events.go:22",
			event: agent.RunEvent{
				Type:      agent.RunEventRunFailed,
				At:        eventAt,
				Seq:       7,
				FinalText: "so far",
				Err:       fmt.Errorf("upstream broke"),
			},
			assert: func(t *testing.T, got *chatpb.SessionEvent) {
				failed := got.GetRunFailed()
				if failed == nil {
					t.Fatalf("run_failed mapped to %v, want RunFailed", frameMember(t, got))
				}
				if failed.GetStatus() != chatpb.RunStatus_RUN_STATUS_FAILED {
					t.Fatalf("status = %v, want RUN_STATUS_FAILED", failed.GetStatus())
				}
				if failed.GetError() != "upstream broke" || failed.GetFullText() != "so far" {
					t.Fatalf(
						"failure = (text %q error %q), want so far/upstream broke",
						failed.GetFullText(),
						failed.GetError(),
					)
				}
				assertEventClock(t, got, eventAt, 7)
			},
		},
	}

	if len(rows) != len(allRunEventTypes) {
		t.Fatalf("golden table rows = %d, want %d", len(rows), len(allRunEventTypes))
	}
	for _, eventType := range allRunEventTypes {
		row, ok := rows[eventType]
		if !ok {
			t.Fatalf("golden table has no row for RunEventType %q", eventType)
		}
		got := runEventToSessionEvent(row.event)
		if got == nil {
			t.Fatalf(
				"%s (%s) mapped to nil, want a SessionEvent member",
				eventType,
				row.source,
			)
		}
		if got.GetEventIndex() != 0 {
			t.Fatalf(
				"%s mapped event_index = %d, want 0: the log assigns indexes, not the mapping",
				row.source,
				got.GetEventIndex(),
			)
		}
		row.assert(t, got)
	}
}

// TestRunEventMappingDropsUnknownTypes pins the additive-only rule: a
// RunEventType the frozen contract has no member for maps to nil and never
// reaches the log.
func TestRunEventMappingDropsUnknownTypes(t *testing.T) {
	if got := runEventToSessionEvent(agent.RunEvent{Type: "future_event"}); got != nil {
		t.Fatalf("unknown event type mapped to %v, want nil", got)
	}
}

// TestServiceOpenSessionOpensTheLogWithTheSession pins the log's first
// entry: the session opening, at event_index 1, before any run event.
func TestServiceOpenSessionOpensTheLogWithTheSession(t *testing.T) {
	endpoint := NewAgentEndpoint(&fakePublisher{})

	logical := endpoint.newSession("conv-open")
	frames := logical.events.retained()
	if len(frames) != 1 {
		t.Fatalf("retained events = %d, want the single session opening", len(frames))
	}
	opening := frames[0]
	if opening.GetEventIndex() != 1 {
		t.Fatalf("opening event_index = %d, want 1", opening.GetEventIndex())
	}
	if opening.GetSessionOpened() == nil ||
		opening.GetSessionOpened().GetSessionId() != logical.id {
		t.Fatalf(
			"opening = %v, want session_opened/%s",
			frameMember(t, opening),
			logical.id,
		)
	}
	if opening.GetTurnSeq() != 0 {
		t.Fatalf("opening turn_seq = %d, want 0 before any run", opening.GetTurnSeq())
	}
	if opening.GetOccurredAt() == nil {
		t.Fatal("opening occurred_at = nil, want the open time")
	}
}

// TestServiceWatchEventsRejectsUnknownSessions pins the rpc's session rule:
// the same NotFound SendMessage and Abort return, for both a fabricated id
// and an empty one. The stream's status surfaces on the first Recv, so that
// is where the test reads it.
func TestServiceWatchEventsRejectsUnknownSessions(t *testing.T) {
	publisher := &fakePublisher{}
	client := startEventBridgeClient(
		t,
		NewService(&fakeTurnLister{}, NewAgentEndpoint(publisher)),
	)
	for _, sessionID := range []string{"sess-404", "  "} {
		watch, err := client.WatchEvents(
			context.Background(),
			&chatpb.WatchEventsRequest{SessionId: sessionID},
		)
		if err != nil {
			t.Fatalf("WatchEvents() error = %v", err)
		}
		_, recvErr := recvWatchFrame(t, watch)
		if status.Code(recvErr) != codes.NotFound {
			t.Fatalf(
				"WatchEvents(session %q) code = %v, want NotFound (err=%v)",
				sessionID,
				status.Code(recvErr),
				recvErr,
			)
		}
	}
}

// TestServiceWatchEventsStreamsInOrderAndResumesWithoutGap walks the stream
// the way the web tier does: subscribe, watch a run's events arrive in push
// order with contiguous event_index values, lose the stream, and resume from
// the last acknowledged index with no frame missing in between.
func TestServiceWatchEventsStreamsInOrderAndResumesWithoutGap(t *testing.T) {
	publisher := &fakePublisher{}
	endpoint := NewAgentEndpoint(publisher)
	client := startEventBridgeClient(t, NewService(&fakeTurnLister{}, endpoint))
	ctx := context.Background()

	opened, err := client.OpenSession(ctx, &chatpb.OpenSessionRequest{ChatId: "conv-7"})
	if err != nil {
		t.Fatalf("OpenSession() error = %v", err)
	}
	sessionID := opened.GetSession().GetSessionId()

	watchCtx, cancelWatch := context.WithCancel(ctx)
	defer cancelWatch()
	watch, err := client.WatchEvents(
		watchCtx,
		&chatpb.WatchEventsRequest{SessionId: sessionID},
	)
	if err != nil {
		t.Fatalf("WatchEvents() error = %v", err)
	}
	first := mustRecvWatchFrame(t, watch)
	if first.GetEventIndex() != 1 || first.GetSessionOpened() == nil {
		t.Fatalf(
			"first frame = %v/%d, want session_opened at 1",
			frameMember(t, first),
			first.GetEventIndex(),
		)
	}

	if _, err := client.SendMessage(ctx, &chatpb.SendMessageRequest{
		SessionId:   sessionID,
		ClientMsgId: "client-1",
		Text:        "hello",
	}); err != nil {
		t.Fatalf("SendMessage() error = %v", err)
	}
	run, err := endpoint.OpenSession(ctx, publisher.published()[0])
	if err != nil {
		t.Fatalf("OpenSession() worker seam error = %v", err)
	}
	toolCall := agent.ToolCall{ID: "call-1", Name: "read_file", Arguments: "{}"}
	for _, event := range []agent.RunEvent{
		{Type: agent.RunEventRunStarted, Seq: 42},
		{Type: agent.RunEventModelTurnStarted, Turn: 1, ModelRef: "claude-x", Seq: 42},
		{Type: agent.RunEventToolStarted, ToolCall: toolCall, Seq: 42},
		{Type: agent.RunEventToolFinished, ToolCall: toolCall, ToolOutput: "ok", Seq: 42},
	} {
		run.OnRunEvent(ctx, event)
	}

	// The live stream delivers what was appended after it subscribed, in
	// push order, with contiguous indexes.
	wantOrder := []string{"run_started", "model_turn_started", "tool_started", "tool_finished"}
	last := first
	for _, wantMember := range wantOrder {
		event := mustRecvWatchFrame(t, watch)
		if frameMember(t, event) != wantMember {
			t.Fatalf(
				"stream member = %v, want %v",
				frameMember(t, event),
				wantMember,
			)
		}
		if event.GetTurnSeq() != 42 {
			t.Fatalf("%s turn_seq = %d, want 42", wantMember, event.GetTurnSeq())
		}
		if event.GetEventIndex() != last.GetEventIndex()+1 {
			t.Fatalf(
				"%s event_index = %d, want %d: the stream must stay contiguous",
				wantMember,
				event.GetEventIndex(),
				last.GetEventIndex()+1,
			)
		}
		last = event
	}
	if last.GetEventIndex() != 5 {
		t.Fatalf("tool_finished event_index = %d, want 5", last.GetEventIndex())
	}

	// The client drops its stream before the run ends, exactly like a
	// dropped connection, and keeps its last acknowledged index.
	cancelWatch()

	run.OnRunEvent(ctx, agent.RunEvent{
		Type:      agent.RunEventRunFinished,
		ModelRef:  "claude-x",
		FinalText: "done",
		Seq:       42,
	})

	resumed, err := client.WatchEvents(ctx, &chatpb.WatchEventsRequest{
		SessionId:       sessionID,
		AfterEventIndex: 5,
	})
	if err != nil {
		t.Fatalf("WatchEvents(resume) error = %v", err)
	}
	resumedFirst := mustRecvWatchFrame(t, resumed)
	if resumedFirst.GetEventIndex() != 6 {
		t.Fatalf(
			"resume first event_index = %d, want 6, the run_finished the drop hid",
			resumedFirst.GetEventIndex(),
		)
	}
	if resumedFirst.GetRunFinished() == nil ||
		resumedFirst.GetRunFinished().GetFullText() != "done" {
		t.Fatalf("resume first frame = %v, want the terminal run_finished", resumedFirst)
	}

	// A third subscriber from the beginning proves the whole log reads back
	// in order with contiguous indexes.
	head, err := client.WatchEvents(
		ctx, &chatpb.WatchEventsRequest{SessionId: sessionID},
	)
	if err != nil {
		t.Fatalf("WatchEvents(from start) error = %v", err)
	}
	for wantIndex, wantMember := range []string{
		"session_opened",
		"run_started",
		"model_turn_started",
		"tool_started",
		"tool_finished",
		"run_finished",
	} {
		event := mustRecvWatchFrame(t, head)
		if event.GetEventIndex() != int64(wantIndex+1) {
			t.Fatalf(
				"frame %d event_index = %d, want %d",
				wantIndex,
				event.GetEventIndex(),
				wantIndex+1,
			)
		}
		if got := frameMember(t, event); got != wantMember {
			t.Fatalf("frame %d member = %v, want %v", wantIndex, got, wantMember)
		}
	}
}

// TestServiceWatchEventsNoticesResyncWhenCursorFallsOutOfHistory pushes past
// the retention bound and reconnects with a cursor that can no longer reach
// the retained history: the stream opens with the resync Notice and restarts
// at the oldest retained index.
func TestServiceWatchEventsNoticesResyncWhenCursorFallsOutOfHistory(t *testing.T) {
	publisher := &fakePublisher{}
	endpoint := NewAgentEndpoint(publisher)
	client := startEventBridgeClient(t, NewService(&fakeTurnLister{}, endpoint))
	ctx := context.Background()

	opened, err := client.OpenSession(ctx, &chatpb.OpenSessionRequest{})
	if err != nil {
		t.Fatalf("OpenSession() error = %v", err)
	}
	sessionID := opened.GetSession().GetSessionId()

	if _, err := client.SendMessage(ctx, &chatpb.SendMessageRequest{
		SessionId: sessionID,
		Text:      "hello",
	}); err != nil {
		t.Fatalf("SendMessage() error = %v", err)
	}
	run, err := endpoint.OpenSession(ctx, publisher.published()[0])
	if err != nil {
		t.Fatalf("OpenSession() worker seam error = %v", err)
	}
	// Tool starts and finishes come through in pairs; enough of them walk
	// the log past its retention window so the session's first entries are
	// evicted.
	toolCall := agent.ToolCall{ID: "call-1", Name: "exec", Arguments: "{}"}
	pushed := (sessionEventRetention + 1) / 2
	for i := 0; i < pushed; i++ {
		run.OnRunEvent(ctx, agent.RunEvent{Type: agent.RunEventToolStarted, ToolCall: toolCall})
		run.OnRunEvent(ctx, agent.RunEvent{
			Type:       agent.RunEventToolFinished,
			ToolCall:   toolCall,
			ToolOutput: "ok",
		})
	}
	session := endpoint.lookupSession(sessionID)
	oldestRetained := session.events.retained()[0].GetEventIndex()
	if session.events.retained()[0].GetEventIndex() <= 1 {
		t.Fatalf(
			"oldest retained index = %d, want the window to have evicted the opening",
			oldestRetained,
		)
	}

	watch, err := client.WatchEvents(
		ctx,
		&chatpb.WatchEventsRequest{SessionId: sessionID, AfterEventIndex: 0},
	)
	if err != nil {
		t.Fatalf("WatchEvents() error = %v", err)
	}
	notice := mustRecvWatchFrame(t, watch)
	if notice.GetNotice() == nil {
		t.Fatalf("first resume frame = %v, want a resync Notice", frameMember(t, notice))
	}
	if notice.GetNotice().GetCode() != resyncNoticeCode {
		t.Fatalf("notice code = %q, want %q", notice.GetNotice().GetCode(), resyncNoticeCode)
	}

	restart := mustRecvWatchFrame(t, watch)
	if restart.GetEventIndex() != oldestRetained {
		t.Fatalf(
			"restart event_index = %d, want %d, the oldest retained entry",
			restart.GetEventIndex(),
			oldestRetained,
		)
	}
	for _, event := range []*chatpb.SessionEvent{
		mustRecvWatchFrame(t, watch),
		mustRecvWatchFrame(t, watch),
		mustRecvWatchFrame(t, watch),
	} {
		if event.GetEventIndex() != restart.GetEventIndex()+1 {
			t.Fatalf(
				"frame after %d = %d, want contiguous",
				restart.GetEventIndex(),
				event.GetEventIndex(),
			)
		}
		restart = event
	}

	// A cursor inside the window is not stale: the replay starts one past it
	// with no notice, because nothing it needs was evicted.
	insideCursor := restart.GetEventIndex()
	midStream, err := client.WatchEvents(ctx, &chatpb.WatchEventsRequest{
		SessionId:       sessionID,
		AfterEventIndex: insideCursor,
	})
	if err != nil {
		t.Fatalf("WatchEvents(mid-stream) error = %v", err)
	}
	next := mustRecvWatchFrame(t, midStream)
	if next.GetNotice() != nil || next.GetEventIndex() != insideCursor+1 {
		t.Fatalf(
			"mid-stream first frame = %v/%d, want event %d without a notice",
			frameMember(t, next),
			next.GetEventIndex(),
			insideCursor+1,
		)
	}
}

// TestSessionEventLogRetainsABoundedWindow pins the retention bound: a log
// keeps the newest window and nothing else, the window stays contiguous, and
// subscribers of a cursor inside it resume exactly one past it.
func TestSessionEventLogRetainsABoundedWindow(t *testing.T) {
	log := newSessionEventLog()
	log.append(&chatpb.SessionEvent{
		Event: &chatpb.SessionEvent_SessionOpened{SessionOpened: &chatpb.SessionOpened{}},
	})

	for i := 0; i < sessionEventRetention+25; i++ {
		log.append(&chatpb.SessionEvent{
			Event: &chatpb.SessionEvent_RunStarted{RunStarted: &chatpb.RunStarted{}},
		})
	}

	retained := log.retained()
	if len(retained) != sessionEventRetention {
		t.Fatalf(
			"retained events = %d, want the window of %d",
			len(retained), sessionEventRetention,
		)
	}
	// The opening event and sessionEventRetention+25 run events were
	// appended oldest first, so the window keeps the newest bound and drops
	// everything before it.
	totalAppended := int64(1 + sessionEventRetention + 25)
	wantFirst := totalAppended - int64(sessionEventRetention) + 1
	if retained[0].GetEventIndex() != wantFirst {
		t.Fatalf("oldest retained index = %d, want %d", retained[0].GetEventIndex(), wantFirst)
	}
	last := retained[0].GetEventIndex()
	for _, event := range retained[1:] {
		if event.GetEventIndex() != last+1 {
			t.Fatalf(
				"retained %d follows %d, want %d: the window must stay contiguous",
				event.GetEventIndex(),
				last,
				last+1,
			)
		}
		last = event.GetEventIndex()
	}

	_, replay := log.subscribe(0)
	if got := replay[0].GetNotice(); got == nil || got.GetCode() != resyncNoticeCode {
		t.Fatalf("replay from a stale cursor starts = %v, want the resync Notice", replay[0])
	}

	_, inWindow := log.subscribe(retained[0].GetEventIndex() - 1)
	if inWindow[0].GetNotice() != nil ||
		inWindow[0].GetEventIndex() != retained[0].GetEventIndex() {
		t.Fatalf(
			"replay at the window's edge starts = %v/%d, want %d untouched by a notice",
			frameMember(t, inWindow[0]),
			inWindow[0].GetEventIndex(),
			retained[0].GetEventIndex(),
		)
	}
}

// TestSessionEventLogDropsSlowSubscriberWithoutBlockingTheRun pins the
// subscriber contract: a backlog a subscriber never drains drops that
// subscriber instead of stalling the log's append, and the reconnecting
// client with the subscriber's last index replays the whole tail without a
// gap.
func TestSessionEventLogDropsSlowSubscriberWithoutBlockingTheRun(t *testing.T) {
	log := newSessionEventLog()
	log.append(&chatpb.SessionEvent{
		Event: &chatpb.SessionEvent_SessionOpened{SessionOpened: &chatpb.SessionOpened{}},
	})

	subscriber, replay := log.subscribe(1)
	if len(replay) != 0 {
		t.Fatalf("replay = %d frames, want an empty replay at the newest index", len(replay))
	}

	// Fill the subscriber's buffer without reading, then push one event
	// past the bound: the next append drops the subscriber on the buffer
	// overflow instead of blocking on it.
	for i := 0; i < sessionSubscriberBuffer; i++ {
		log.append(&chatpb.SessionEvent{
			Event: &chatpb.SessionEvent_SessionOpened{SessionOpened: &chatpb.SessionOpened{}},
		})
	}
	unblocked := make(chan struct{})
	go func() {
		log.append(&chatpb.SessionEvent{
			Event: &chatpb.SessionEvent_RunStarted{RunStarted: &chatpb.RunStarted{}},
		})
		close(unblocked)
	}()
	select {
	case <-unblocked:
	case <-time.After(2 * time.Second):
		t.Fatal("append blocked on a subscriber that never drained its channel")
	}

	received := 0
	for event := range subscriber.events {
		if event.GetEventIndex() != int64(2+received) {
			t.Fatalf(
				"buffered frame %d = event_index %d, want %d",
				received,
				event.GetEventIndex(),
				2+received,
			)
		}
		received++
		if received > sessionSubscriberBuffer {
			t.Fatalf(
				"dropped subscriber delivered %d frames, want at most the buffer of %d",
				received, sessionSubscriberBuffer,
			)
		}
	}
	if received != sessionSubscriberBuffer {
		t.Fatalf(
			"dropped subscriber received %d frames, want the buffer of %d then the close",
			received, sessionSubscriberBuffer,
		)
	}
	// The range's end is the close the drop performed: the stream the client
	// holds ends after what it already received, which is the signal to
	// reconnect on.

	resumed, resumedReplay := log.subscribe(int64(received + 1))
	if got := resumedReplay[0].GetEventIndex(); got != int64(sessionSubscriberBuffer+2) {
		t.Fatalf(
			"resumed first event_index = %d, want %d: the frames the live stream hid resume without a gap",
			got,
			sessionSubscriberBuffer+2,
		)
	}
	for i, event := range resumedReplay[1:] {
		want := event.GetEventIndex()
		if previous := resumedReplay[i].GetEventIndex(); want != previous+1 {
			t.Fatalf(
				"resumed frame %d event_index = %d, want %d",
				i,
				want,
				previous+1,
			)
		}
	}
	log.unsubscribe(subscriber)
	log.unsubscribe(resumed)
}

// TestSessionEventLogSubscriberSeesAppendAfterSubscribe pins the handoff
// that must not drop or duplicate an event: the replay and the live channel
// are filled in one critical section.
func TestSessionEventLogDropsNoEventBetweenReplayAndLive(t *testing.T) {
	log := newSessionEventLog()
	log.append(&chatpb.SessionEvent{
		Event: &chatpb.SessionEvent_SessionOpened{SessionOpened: &chatpb.SessionOpened{}},
	})

	subscriber, replay := log.subscribe(0)
	if len(replay) != 1 {
		t.Fatalf("replay = %d frames, want the opening only", len(replay))
	}
	log.append(&chatpb.SessionEvent{
		Event: &chatpb.SessionEvent_RunStarted{RunStarted: &chatpb.RunStarted{}},
	})
	if event, ok := <-subscriber.events; !ok || event.GetEventIndex() != 2 {
		t.Fatalf("live first frame = %v/%v, want event_index 2", event, ok)
	}
	log.unsubscribe(subscriber)
}

// startTestRun opens one run session the way the worker does, through the
// endpoint's seam, for tests that drive run events by hand.
func startTestRun(t *testing.T, endpoint *AgentEndpoint, sessionID string) *runSession {
	t.Helper()
	run, err := endpoint.OpenSession(context.Background(), bus.InboundMessage{
		Channel: bus.ChannelBridge,
		ChatID:  sessionID,
		Text:    "hello",
	})
	if err != nil {
		t.Fatalf("OpenSession() worker seam error = %v", err)
	}
	if run == nil {
		t.Fatal("OpenSession() worker seam = nil session, want one")
	}
	runSession, ok := run.(*runSession)
	if !ok {
		t.Fatalf("OpenSession() worker seam = %T, want *runSession", run)
	}
	return runSession
}

// terminalFrames returns a log's terminal run events, the count the contract
// allows exactly one of per run.
func terminalFrames(frames []*chatpb.SessionEvent) []*chatpb.SessionEvent {
	terminals := make([]*chatpb.SessionEvent, 0, 1)
	for _, event := range frames {
		if event.GetRunFinished() != nil || event.GetRunFailed() != nil {
			terminals = append(terminals, event)
		}
	}
	return terminals
}

// frameAt asserts frames[which] is the member named, for the ordering tests.
func frameAt(
	t *testing.T,
	frames []*chatpb.SessionEvent,
	which int,
	wantMember string,
) *chatpb.SessionEvent {
	t.Helper()
	if which >= len(frames) {
		t.Fatalf(
			"frame %d missing: %d frames, want %s next",
			which, len(frames), wantMember,
		)
	}
	event := frames[which]
	if got := frameMember(t, event); got != wantMember {
		t.Fatalf("frame %d = %v, want %v", which, got, wantMember)
	}
	return event
}

// TestRunSessionFlushesAndResetsDraftsAtAttemptBoundaries pins the stream's
// ordering rules: non-delta events flush the pending draft first, a Snapshot
// keyframe carries the answer so far, a new model attempt replaces the
// drafts, and the run's terminal event carries the engine's final text.
func TestRunSessionFlushesAndResetsDraftsAtAttemptBoundaries(t *testing.T) {
	endpoint := NewAgentEndpoint(&fakePublisher{})
	logical := endpoint.newSession("conv-drafts")
	run := startTestRun(t, endpoint, logical.id)
	ctx := context.Background()

	run.OnRunEvent(ctx, agent.RunEvent{Type: agent.RunEventRunStarted, Seq: 9})
	run.OnRunEvent(ctx, agent.RunEvent{
		Type: agent.RunEventModelTurnStarted, Turn: 1, ModelRef: "claude-x", Seq: 9,
	})
	run.OnRunEvent(ctx, agent.RunEvent{Type: agent.RunEventModelTurnDelta, Delta: "hel", Seq: 9})
	run.OnRunEvent(ctx, agent.RunEvent{Type: agent.RunEventModelTurnDelta, Delta: "lo", Seq: 9})
	// Tool activity is a non-delta event: the pending "hello" must reach the
	// stream before it, or a client's replay would interleave wrongly.
	run.OnRunEvent(ctx, agent.RunEvent{
		Type: agent.RunEventToolStarted,
		ToolCall: agent.ToolCall{
			ID: "call-1", Name: "read_file", Arguments: `{"path":"x"}`,
		},
		Seq: 9,
	})
	run.OnRunEvent(ctx, agent.RunEvent{
		Type:       agent.RunEventToolFinished,
		ToolCall:   agent.ToolCall{ID: "call-1", Name: "read_file"},
		ToolOutput: "ok",
		Seq:        9,
	})
	run.OnRunEvent(ctx, agent.RunEvent{
		Type: agent.RunEventModelReasoningDelta, Delta: "because", Seq: 9,
	})
	run.OnRunEvent(ctx, agent.RunEvent{
		Type: agent.RunEventModelTurnStarted, Turn: 2, ModelRef: "claude-next", Seq: 9,
	})
	run.OnRunEvent(ctx, agent.RunEvent{Type: agent.RunEventModelTurnDelta, Delta: "there", Seq: 9})
	run.OnRunEvent(ctx, agent.RunEvent{
		Type:      agent.RunEventRunFinished,
		ModelRef:  "claude-next",
		FinalText: "hello there",
		Seq:       9,
	})

	frames := logical.events.retained()

	// The two answer tokens arrived as one coalesced frame ahead of the tool
	// activity that ended the draft, followed by the keyframe carrying the
	// draft as full text.
	if coalesced := frameAt(t, frames, 3, "model_turn_delta"); coalesced.GetModelTurnDelta().GetDelta() != "hello" {
		t.Fatalf(
			"coalesced delta = %q, want hello: two tokens, one frame",
			coalesced.GetModelTurnDelta().GetDelta(),
		)
	}
	keyframe := frameAt(t, frames, 4, "snapshot")
	if keyframe.GetSnapshot().GetText() != "hello" {
		t.Fatalf(
			"first keyframe = %q, want hello: the full answer so far",
			keyframe.GetSnapshot().GetText(),
		)
	}
	if keyframe.GetTurnSeq() != 9 {
		t.Fatalf("keyframe turn_seq = %d, want 9", keyframe.GetTurnSeq())
	}
	frameAt(t, frames, 5, "tool_started")
	frameAt(t, frames, 6, "tool_finished")

	// The reasoning delta flushed ahead of the next attempt's boundary, the
	// attempt start followed, and the draft the next Snapshot will carry
	// restarts there.
	if flushed := frameAt(t, frames, 7, "model_reasoning_delta"); flushed.GetModelReasoningDelta().GetDelta() != "because" {
		t.Fatalf(
			"flushed reasoning = %q, want because before the attempt restart",
			flushed.GetModelReasoningDelta().GetDelta(),
		)
	}
	if keyframe = frameAt(t, frames, 8, "snapshot"); keyframe.GetSnapshot().GetText() != "hello" {
		t.Fatalf(
			"frame before the restart = snapshot %q, want the answer unchanged by reasoning",
			keyframe.GetSnapshot().GetText(),
		)
	}
	if restart := frameAt(t, frames, 9, "model_turn_started"); restart.GetModelTurnStarted().GetLoopTurn() != 2 {
		t.Fatalf("second attempt loop_turn = %d, want 2", restart.GetModelTurnStarted().GetLoopTurn())
	}
	if there := frameAt(t, frames, 10, "model_turn_delta"); there.GetModelTurnDelta().GetDelta() != "there" {
		t.Fatalf(
			"next attempt delta = %q, want there on the fresh draft",
			there.GetModelTurnDelta().GetDelta(),
		)
	}
	if got := frameAt(t, frames, 11, "snapshot").GetSnapshot().GetText(); got != "there" {
		t.Fatalf("post-restart keyframe = %q, want there: the draft restarted", got)
	}
	terminal := frameAt(t, frames, 12, "run_finished").GetRunFinished()
	if terminal.GetStatus() != chatpb.RunStatus_RUN_STATUS_COMPLETED {
		t.Fatalf("terminal status = %v, want RUN_STATUS_COMPLETED", terminal.GetStatus())
	}
	if terminal.GetFullText() != "hello there" || terminal.GetModelRef() != "claude-next" {
		t.Fatalf(
			"terminal = (text %q ref %q), want the engine's final text and model",
			terminal.GetFullText(),
			terminal.GetModelRef(),
		)
	}
	if got := len(terminalFrames(frames)); got != 1 {
		t.Fatalf("terminal frames = %d, want exactly one", got)
	}
	if got := len(frames); got != 13 {
		t.Fatalf("frames = %d, want 13: deltas coalesce, non-deltas do not duplicate", got)
	}
	if got := len(run.answerPending) + len(run.reasoningPending); got != 0 {
		t.Fatalf("pending drafts = %d chars after a terminal event, want flushed", got)
	}
}

// TestRunSessionAbortedRunEmitsExactlyOneAbortedTerminal pins the abort
// path: the coalesced draft flushes, and the run ends with exactly one
// RUN_STATUS_ABORTED terminal carrying the text produced before the cancel.
func TestRunSessionAbortedRunEmitsExactlyOneAbortedTerminal(t *testing.T) {
	endpoint := NewAgentEndpoint(&fakePublisher{})
	logical := endpoint.newSession("conv-abort")
	run := startTestRun(t, endpoint, logical.id)
	ctx := context.Background()

	run.OnRunEvent(ctx, agent.RunEvent{Type: agent.RunEventRunStarted, Seq: 3})
	run.OnRunEvent(ctx, agent.RunEvent{
		Type: agent.RunEventModelTurnStarted, Turn: 1, ModelRef: "claude-x", Seq: 3,
	})
	run.OnRunEvent(ctx, agent.RunEvent{Type: agent.RunEventModelTurnDelta, Delta: "strea", Seq: 3})
	run.OnRunEvent(ctx, agent.RunEvent{Type: agent.RunEventModelTurnDelta, Delta: "ming…", Seq: 3})

	// The cancel lands mid-stream, as the worker's Abort does.
	run.Abort(ctx, "canceled")

	frames := logical.events.retained()
	frameAt(t, frames, 2, "model_turn_started")
	if flushed := frameAt(t, frames, 3, "model_turn_delta"); flushed.GetModelTurnDelta().GetDelta() != "streaming…" {
		t.Fatalf(
			"abort flush delta = %q, want the whole draft, not a dropped tail",
			flushed.GetModelTurnDelta().GetDelta(),
		)
	}
	terminal := frameAt(t, frames, 5, "run_finished").GetRunFinished()
	if terminal.GetStatus() != chatpb.RunStatus_RUN_STATUS_ABORTED {
		t.Fatalf("terminal status = %v, want RUN_STATUS_ABORTED", terminal.GetStatus())
	}
	if terminal.GetFullText() != "streaming…" || terminal.GetModelRef() != "claude-x" {
		t.Fatalf(
			"terminal = (text %q ref %q), want streaming…/claude-x",
			terminal.GetFullText(),
			terminal.GetModelRef(),
		)
	}
	if got := len(terminalFrames(frames)); got != 1 {
		t.Fatalf("terminal frames after abort = %d, want exactly one", got)
	}

	// Whatever arrives after the run ended, late event or late timer tick,
	// appends nothing: the stream is closed.
	run.OnRunEvent(ctx, agent.RunEvent{Type: agent.RunEventModelTurnDelta, Delta: "late", Seq: 3})
	run.flushDeltaBuffers()
	if got := len(logical.events.retained()); got != len(frames) {
		t.Fatalf("frames after the run ended = %d, want the same %d", got, len(frames))
	}
}

// TestRunSessionAbortsAnUnstreamedRunOnce pins the no-run-events edge: an
// abort that lands before any frame still ends the stream in one terminal
// event, with empty full text the run honestly produced nothing.
func TestRunSessionAbortsAnUnstreamedRunWithOneTerminal(t *testing.T) {
	endpoint := NewAgentEndpoint(&fakePublisher{})
	logical := endpoint.newSession("conv-abort-empty")
	run := startTestRun(t, endpoint, logical.id)

	run.Abort(context.Background(), "canceled")

	frames := logical.events.retained()
	if got := len(frames); got != 2 {
		t.Fatalf("frames = %d, want the opening and one terminal", got)
	}
	terminal := terminalFrames(frames)
	if len(terminal) != 1 || terminal[0].GetRunFinished().GetStatus() != chatpb.RunStatus_RUN_STATUS_ABORTED {
		t.Fatalf("terminal = %v, want one RunFinished RUN_STATUS_ABORTED", terminal)
	}
}

// TestRunSessionEngineTerminalIsNotDoubled pins the normal path: the engine
// already emitted run_finished or run_failed, and the worker's Finish adds
// no second terminal event.
func TestRunSessionEngineTerminalIsNotDoubled(t *testing.T) {
	endpoint := NewAgentEndpoint(&fakePublisher{})
	logical := endpoint.newSession("conv-once")
	run := startTestRun(t, endpoint, logical.id)
	ctx := context.Background()

	run.OnRunEvent(ctx, agent.RunEvent{Type: agent.RunEventRunStarted, Seq: 6})
	run.OnRunEvent(ctx, agent.RunEvent{Type: agent.RunEventModelTurnDelta, Delta: "real", Seq: 6})
	run.OnRunEvent(ctx, agent.RunEvent{
		Type:      agent.RunEventRunFinished,
		ModelRef:  "claude-x",
		FinalText: "real answer",
		Seq:       6,
	})

	// The worker hands the same run's reply over; Finish must stand down.
	run.Finish(ctx, agent.ReplyResult{Text: "real"})

	frames := logical.events.retained()
	if got := len(terminalFrames(frames)); got != 1 {
		t.Fatalf("terminal frames = %d, want the engine's one", got)
	}
	engine := terminalFrames(frames)[0].GetRunFinished()
	if engine.GetFullText() != "real answer" ||
		engine.GetStatus() != chatpb.RunStatus_RUN_STATUS_COMPLETED {
		t.Fatalf("terminal = %v, want the engine's COMPLETED text", engine)
	}

	// The failed normal path ends the same way: one terminal, the engine's,
	// with the error preserved rather than rewritten by the worker's Finish.
	failSession := endpoint.newSession("conv-fail")
	failed := startTestRun(t, endpoint, failSession.id)
	failed.OnRunEvent(ctx, agent.RunEvent{Type: agent.RunEventRunStarted, Seq: 8})
	failed.OnRunEvent(ctx, agent.RunEvent{
		Type:      agent.RunEventRunFailed,
		ModelRef:  "claude-x",
		FinalText: "partial",
		Err:       fmt.Errorf("upstream broke"),
	})
	failed.Finish(ctx, agent.ReplyResult{Text: "reported failure"})

	failFrames := failSession.events.retained()
	failedTerminal := terminalFrames(failFrames)
	if len(failedTerminal) != 1 {
		t.Fatalf("failed run terminal frames = %d, want one", len(failedTerminal))
	}
	if got := failedTerminal[0].GetRunFailed(); got == nil || got.GetError() != "upstream broke" {
		t.Fatalf("failed terminal = %v, want the engine's RunFailed with its error", failedTerminal[0])
	}
}

// TestRunSessionFinishClosesTheTerminalGap pins the abnormal path in the
// other direction: a run whose engine never said run_finished still ends the
// stream in exactly one COMPLETED terminal carrying the reply's text.
func TestRunSessionFinishClosesTheTerminalGap(t *testing.T) {
	endpoint := NewAgentEndpoint(&fakePublisher{})
	logical := endpoint.newSession("conv-gap")
	run := startTestRun(t, endpoint, logical.id)
	ctx := context.Background()

	run.OnRunEvent(ctx, agent.RunEvent{Type: agent.RunEventRunStarted, Seq: 4})
	run.OnRunEvent(ctx, agent.RunEvent{
		Type: agent.RunEventModelTurnStarted, Turn: 1, ModelRef: "claude-x", Seq: 4,
	})
	run.Finish(ctx, agent.ReplyResult{Text: "recovered answer"})

	frames := logical.events.retained()
	terminal := terminalFrames(frames)
	if len(terminal) != 1 || terminal[0].GetRunFinished() == nil {
		t.Fatalf("terminal frames = %v, want one RunFinished", terminal)
	}
	finished := terminal[0].GetRunFinished()
	if finished.GetFullText() != "recovered answer" ||
		finished.GetStatus() != chatpb.RunStatus_RUN_STATUS_COMPLETED {
		t.Fatalf(
			"gap terminal = %v, want COMPLETED with the reply's final text",
			finished,
		)
	}
	if terminal[0].GetTurnSeq() != 4 {
		t.Fatalf(
			"gap terminal turn_seq = %d, want the run's transcript sequence",
			terminal[0].GetTurnSeq(),
		)
	}
}

// TestServiceWatchEventsCoalescesADeltaFloodIntoKeyedFrames is the long-run
// check: many raw deltas pushed through a live WatchEvents stream arrive as
// coalesced delta frames plus the Snapshot keyframes between them, not one
// frame per token, and never sooner than the named flush interval.
func TestServiceWatchEventsCoalescesADeltaFloodIntoKeyedFrames(t *testing.T) {
	publisher := &fakePublisher{}
	endpoint := NewAgentEndpoint(publisher)
	client := startEventBridgeClient(t, NewService(&fakeTurnLister{}, endpoint))
	ctx := context.Background()

	opened, err := client.OpenSession(ctx, &chatpb.OpenSessionRequest{ChatId: "conv-flood"})
	if err != nil {
		t.Fatalf("OpenSession() error = %v", err)
	}
	sessionID := opened.GetSession().GetSessionId()
	run := startTestRun(t, endpoint, sessionID)

	// The subscriber joins while the run holds its first frames, so it
	// replays them synchronously and then watches the burst live.
	run.OnRunEvent(ctx, agent.RunEvent{Type: agent.RunEventRunStarted, Seq: 5})
	watch, err := client.WatchEvents(
		ctx,
		&chatpb.WatchEventsRequest{SessionId: sessionID},
	)
	if err != nil {
		t.Fatalf("WatchEvents() error = %v", err)
	}
	for _, wantMember := range []string{"session_opened", "run_started"} {
		if got := mustRecvWatchFrame(t, watch); frameMember(t, got) != wantMember {
			t.Fatalf("opening frame = %v, want %v", frameMember(t, got), wantMember)
		}
	}

	// Three bursts of raw deltas, with a pause over the flush interval
	// between them. Each burst reads back exactly one coalesced delta frame
	// and one Snapshot keyframe.
	answer := ""
	deltaFrames, keyframes := 0, 0
	for _, tokens := range []int{30, 30} {
		pushed := strings.Builder{}
		for i := 0; i < tokens; i++ {
			pushed.WriteString(fmt.Sprintf("tok%d ", i))
		}
		// The coalesced delta is the interval's own pending text; the
		// snapshot is the whole answer accumulated so far.
		batch := pushed.String()
		answer += batch

		lastPushedAt := time.Now()
		for i := 0; i < tokens; i++ {
			run.OnRunEvent(ctx, agent.RunEvent{
				Type:  agent.RunEventModelTurnDelta,
				Seq:   5,
				Delta: fmt.Sprintf("tok%d ", i),
			})
		}

		coalesced, err := recvWatchFrame(t, watch)
		if err != nil {
			t.Fatalf("WatchEvents Recv() error = %v in the delta burst", err)
		}
		if got := coalesced.GetModelTurnDelta().GetDelta(); got != batch {
			t.Fatalf("coalesced delta = %q, want %q", got, batch)
		}
		if since := time.Since(lastPushedAt); since < bridgeDeltaFlushInterval-20*time.Millisecond {
			t.Fatalf(
				"coalesced delta arrived %v after the last delta, want at least the flush interval %v",
				since, bridgeDeltaFlushInterval,
			)
		}
		deltaFrames++

		keyframe, err := recvWatchFrame(t, watch)
		if err != nil {
			t.Fatalf("WatchEvents Recv() error = %v in the delta burst", err)
		}
		if got := keyframe.GetSnapshot().GetText(); got != answer {
			t.Fatalf("keyframe = %q, want %q, the whole answer so far", got, answer)
		}
		keyframes++
	}

	// The last burst flushes when the terminal event lands mid-stream.
	fifteen := strings.Builder{}
	for i := 0; i < 15; i++ {
		fifteen.WriteString(fmt.Sprintf("tok%d ", i))
	}
	answer += fifteen.String()
	for i := 0; i < 15; i++ {
		run.OnRunEvent(ctx, agent.RunEvent{
			Type:  agent.RunEventModelTurnDelta,
			Seq:   5,
			Delta: fmt.Sprintf("tok%d ", i),
		})
	}
	run.OnRunEvent(ctx, agent.RunEvent{
		Type:      agent.RunEventRunFinished,
		ModelRef:  "claude-x",
		FinalText: strings.TrimSpace(answer),
		Seq:       5,
	})

	sweep := []string{
		"model_turn_delta", "snapshot",
	}
	for _, wantMember := range sweep {
		if got := mustRecvWatchFrame(t, watch); frameMember(t, got) != wantMember {
			t.Fatalf("burst tail frame = %v, want %v", frameMember(t, got), wantMember)
		}
		if wantMember == "model_turn_delta" {
			deltaFrames++
		} else {
			keyframes++
		}
	}
	if got := mustRecvWatchFrame(t, watch).GetRunFinished(); got == nil ||
		got.GetStatus() != chatpb.RunStatus_RUN_STATUS_COMPLETED ||
		got.GetFullText() != strings.TrimSpace(answer) {
		t.Fatalf(
			"burst terminal = %v, want COMPLETED with the full %d tokens",
			got, len(strings.Split(strings.TrimSpace(answer), " ")),
		)
	}

	// The acceptance count in one number: 75 tokens, six answer frames.
	if got := deltaFrames + keyframes; got != 6 {
		t.Fatalf(
			"answer frames for 75 tokens = %d deltas + %d keyframes = %d, want 3 + 3",
			deltaFrames, keyframes, got,
		)
	}
	if deltaFrames+keyframes >= 75/4 {
		t.Fatalf(
			"answer frames = %d, want far below the %d deltas pushed",
			deltaFrames+keyframes, 30+30+15,
		)
	}

	// The run ended, so nothing more may arrive: the late flush timer was
	// stopped with the terminal event, and any that fired anyway would be a
	// no-op against the finished gate.
	silence := make(chan error, 1)
	go func() {
		_, err := watch.Recv()
		silence <- err
	}()
	select {
	case err := <-silence:
		t.Fatalf("a frame arrived after the terminal event (err=%v), want silence", err)
	case <-time.After(3 * bridgeDeltaFlushInterval):
	}
}
