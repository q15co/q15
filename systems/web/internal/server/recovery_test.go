package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/q15co/q15/libs/chat-contract/chatpb"
	"github.com/q15co/q15/systems/web/internal/protocol"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type fakeConnection struct {
	frames  []protocol.Frame
	slow    bool
	stopped bool
}

func (f *fakeConnection) enqueue(frame protocol.Frame) bool {
	if f.slow {
		return false
	}
	f.frames = append(f.frames, frame)
	return true
}

func (f *fakeConnection) stop() { f.stopped = true }

func TestRegistryFanoutIsolationAndSlowDevice(t *testing.T) {
	r := newRegistry()
	first, second, other, slow := &fakeConnection{}, &fakeConnection{}, &fakeConnection{}, &fakeConnection{
		slow: true,
	}
	for _, device := range []*fakeConnection{first, second, slow} {
		if !r.add("owner", device) {
			t.Fatal("registration failed")
		}
	}
	r.add("another-owner", other)
	r.broadcast("owner", protocol.New(protocol.Pong, "ping", 0, fixtureTime, struct{}{}))
	if len(first.frames) != 1 || len(second.frames) != 1 || len(other.frames) != 0 ||
		!slow.stopped {
		t.Fatal("fanout isolation or slow-device eviction failed")
	}
	r.broadcast("owner", protocol.New(protocol.Pong, "ping", 0, fixtureTime, struct{}{}))
	if len(first.frames) != 2 || len(second.frames) != 2 {
		t.Fatal("slow device blocked healthy devices")
	}
	r.close()
	if !first.stopped || !second.stopped || !other.stopped {
		t.Fatal("shutdown left devices open")
	}
}

func TestReplayWindowCountsRecordsRatherThanSequenceDistance(t *testing.T) {
	for _, test := range []struct {
		name         string
		count        int
		step, cursor int64
		want         string
	}{
		{"empty", 0, 1, 0, protocol.Ready}, {"exact limit", 500, 1, 0, protocol.Ready},
		{"over limit", 501, 1, 0, protocol.Error}, {"cursor boundary", 501, 1, 1, protocol.Ready},
		{"large sparse gap", 2, 1000000000000, 0, protocol.Ready}, {"future cursor", 2, 1, 3, protocol.Error},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := newFakeChat()
			fake.head = int64(test.count) * test.step
			for i := test.count; i > 0; i-- {
				fake.turns = append(fake.turns, &chatpb.Turn{Seq: int64(i) * test.step})
			}
			_, httpServer := setup(t, fake)
			conn := dial(t, httpServer)
			send(t, conn, protocol.Hello, protocol.Cursor{Cursor: test.cursor})
			frame := read(t, conn)
			if frame.Type != test.want {
				t.Fatalf("type %s, want %s: %s", frame.Type, test.want, frame.Payload)
			}
			if frame.Type == protocol.Error {
				var payload protocol.ErrorPayload
				if err := json.Unmarshal(frame.Payload, &payload); err != nil {
					t.Fatal(err)
				}
				if payload.Code != "resync_from_head" {
					t.Fatalf("error %s", payload.Code)
				}
			}
		})
	}
}

func TestWatcherReconnectAndActiveDraftOnNewDevice(t *testing.T) {
	fake := newFakeChat()
	fake.head = 42
	_, httpServer := setup(t, fake)
	first := dial(t, httpServer)
	hello(t, first, 0)
	send(t, first, protocol.Send, protocol.SendRequest{ClientMsgID: "client-1", Text: "hello"})
	take(t, fake.sends)
	take(t, fake.watches)
	fake.emit(
		&chatpb.SessionEvent{
			TurnSeq: 42,
			Event:   &chatpb.SessionEvent_RunStarted{RunStarted: &chatpb.RunStarted{}},
		},
	)
	fake.emit(
		&chatpb.SessionEvent{
			TurnSeq: 42,
			Event: &chatpb.SessionEvent_ModelTurnDelta{
				ModelTurnDelta: &chatpb.ModelTurnDelta{Delta: "partial"},
			},
		},
	)
	for {
		if read(t, first).Seq == 2 {
			break
		}
	}
	second := dial(t, httpServer)
	hello(t, second, 0)
	snapshot := read(t, second)
	var progress protocol.ProgressPayload
	if err := json.Unmarshal(snapshot.Payload, &progress); err != nil {
		t.Fatal(err)
	}
	if snapshot.Type != protocol.Snapshot || progress.Text != "partial" || snapshot.Seq != 2 {
		t.Fatalf("active snapshot %s/%s", snapshot.Type, snapshot.Payload)
	}
	if len(fake.watches) != 0 {
		t.Fatal("new device started another watcher")
	}
	// EOF is deliberate flow control in the bridge. Its replacement must resume
	// at the receiver's last event index, independently of transcript seq 42.
	fake.drops <- struct{}{}
	watch := take(t, fake.watches)
	if watch.GetAfterEventIndex() != 2 {
		t.Fatalf("resume cursor %d, want event index 2", watch.GetAfterEventIndex())
	}
	fake.emit(
		&chatpb.SessionEvent{
			TurnSeq: 42,
			Event: &chatpb.SessionEvent_ModelTurnDelta{
				ModelTurnDelta: &chatpb.ModelTurnDelta{Delta: " answer"},
			},
		},
	)
	frame := read(t, second)
	if frame.Type != protocol.Delta || frame.Seq != 3 {
		t.Fatalf("reconnected watcher = %s/%d", frame.Type, frame.Seq)
	}
}

func TestSocketProtocolValidationAndPing(t *testing.T) {
	_, httpServer := setup(t, newFakeChat())
	conn := dial(t, httpServer)
	send(t, conn, protocol.Ping, struct{}{})
	frame := read(t, conn)
	if !strings.Contains(string(frame.Payload), "hello_required") {
		t.Fatalf("first frame %s", frame.Payload)
	}
	hello(t, conn, 0)
	send(t, conn, protocol.Ping, struct{}{})
	if frame := read(t, conn); frame.Type != protocol.Pong {
		t.Fatalf("ping = %s", frame.Type)
	}
	for _, test := range []struct {
		kind    string
		payload any
		code    string
	}{
		{protocol.Ack, protocol.AckRequest{Seq: 1}, "invalid_ack"},
		{protocol.Send, protocol.SendRequest{ClientMsgID: "client-1", Text: " "}, "invalid_message"},
		{protocol.Abort, protocol.AbortRequest{Turn: -1}, "invalid_turn"},
		{"unknown", struct{}{}, "unknown_type"},
	} {
		send(t, conn, test.kind, test.payload)
		frame := read(t, conn)
		if frame.Type != protocol.Error || !strings.Contains(string(frame.Payload), test.code) {
			t.Fatalf("%s = %s/%s", test.kind, frame.Type, frame.Payload)
		}
	}
}

func TestHeartbeatAndQueueBounds(t *testing.T) {
	if HeartbeatInterval <= 0 || HeartbeatInterval >= time.Minute {
		t.Fatalf("heartbeat %s", HeartbeatInterval)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &socketConn{ctx: ctx, cancel: cancel, queue: make(chan []byte, 1)}
	frame := protocol.New(protocol.Pong, "ping", 0, fixtureTime, struct{}{})
	if !c.enqueue(frame) || c.enqueue(frame) {
		t.Fatal("queue frame limit not enforced")
	}
	large := protocol.New(
		protocol.Notice,
		"large",
		0,
		fixtureTime,
		protocol.NoticePayload{Text: strings.Repeat("x", maxQueueBytes)},
	)
	c = &socketConn{ctx: ctx, cancel: cancel, queue: make(chan []byte, 1)}
	if c.enqueue(large) || c.queuedBytes.Load() != 0 {
		t.Fatal("queue byte limit not enforced")
	}
}

func TestAgentRestartDiscardsSessionHandle(t *testing.T) {
	fake := newFakeChat()
	_, httpServer := setup(t, fake)
	conn := dial(t, httpServer)
	hello(t, conn, 0)
	send(t, conn, protocol.Send, protocol.SendRequest{ClientMsgID: "before", Text: "hello"})
	first := take(t, fake.sends)
	take(t, fake.watches)
	fake.mu.Lock()
	fake.gone = true
	fake.mu.Unlock()
	fake.drops <- struct{}{}
	take(t, fake.watches)
	for {
		frame := read(t, conn)
		if frame.Type == protocol.Error {
			if !strings.Contains(string(frame.Payload), "resync_from_head") {
				t.Fatalf("restart %s", frame.Payload)
			}
			break
		}
	}
	fake.mu.Lock()
	fake.gone = false
	fake.mu.Unlock()
	send(t, conn, protocol.Send, protocol.SendRequest{ClientMsgID: "after", Text: "hello again"})
	if next := take(t, fake.sends); next.GetSessionId() == first.GetSessionId() {
		t.Fatal("reused stale session handle")
	}
	take(t, fake.watches)
}

func TestProactiveDeliveryAndRetainedSnapshot(t *testing.T) {
	fake := newFakeChat()
	s, httpServer := setup(t, fake)
	conn := dial(t, httpServer)
	hello(t, conn, 0)
	fake.outbound <- &chatpb.DeliverResponse{ChatId: "default", Text: "scheduled output", QueuedAt: timestamppb.New(fixtureTime)}
	frame := read(t, conn)
	if frame.Type != protocol.Notice ||
		!strings.Contains(string(frame.Payload), "scheduled output") {
		t.Fatalf("deliver %s/%s", frame.Type, frame.Payload)
	}
	// A retained window may begin after run.start. Its keyframe still proves
	// that a run is active and supplies the draft to a new device.
	session := s.principalSession("owner")
	session.event(
		&chatpb.SessionEvent{
			EventIndex: 100,
			TurnSeq:    42,
			OccurredAt: timestamppb.New(fixtureTime),
			Event: &chatpb.SessionEvent_Snapshot{
				Snapshot: &chatpb.Snapshot{Text: "retained draft"},
			},
		},
	)
	snapshot, ok := session.snapshot()
	if !ok || snapshot.Seq != 100 || !strings.Contains(string(snapshot.Payload), "retained draft") {
		t.Fatal("retained keyframe lost active draft")
	}
}
