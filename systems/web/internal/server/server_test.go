package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/q15co/q15/libs/chat-contract/chatpb"
	"github.com/q15co/q15/systems/web/internal/assets"
	"github.com/q15co/q15/systems/web/internal/bridge"
	"github.com/q15co/q15/systems/web/internal/gate"
	"github.com/q15co/q15/systems/web/internal/protocol"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var fixtureTime = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

type fakeChat struct {
	chatpb.UnimplementedChatServiceServer
	mu          sync.Mutex
	head        int64
	turns       []*chatpb.Turn
	events      []*chatpb.SessionEvent
	subscribers map[chan *chatpb.SessionEvent]struct{}
	opens       int
	sends       chan *chatpb.SendMessageRequest
	aborts      chan *chatpb.AbortRequest
	watches     chan *chatpb.WatchEventsRequest
	drops       chan struct{}
	gone        bool
	outbound    chan *chatpb.DeliverResponse
}

func newFakeChat() *fakeChat {
	return &fakeChat{
		subscribers: make(map[chan *chatpb.SessionEvent]struct{}),
		sends:       make(chan *chatpb.SendMessageRequest, 16),
		aborts: make(
			chan *chatpb.AbortRequest,
			16,
		),
		watches:  make(chan *chatpb.WatchEventsRequest, 16),
		drops:    make(chan struct{}, 1),
		outbound: make(chan *chatpb.DeliverResponse, 16),
	}
}

func (f *fakeChat) GetRuntimeInfo(
	context.Context,
	*chatpb.GetRuntimeInfoRequest,
) (*chatpb.GetRuntimeInfoResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &chatpb.GetRuntimeInfoResponse{
		ProtocolVersion: chatpb.ProtocolVersion,
		HeadSeq:         f.head,
	}, nil
}

func (f *fakeChat) OpenSession(
	context.Context,
	*chatpb.OpenSessionRequest,
) (*chatpb.OpenSessionResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opens++
	return &chatpb.OpenSessionResponse{
		Session: &chatpb.Session{
			SessionId: fmt.Sprintf("session-%d", f.opens),
			State:     chatpb.SessionState_SESSION_STATE_IDLE,
		},
	}, nil
}

func (f *fakeChat) SendMessage(
	_ context.Context,
	r *chatpb.SendMessageRequest,
) (*chatpb.SendMessageResponse, error) {
	f.sends <- r
	return &chatpb.SendMessageResponse{
		ClientMsgId: r.GetClientMsgId(),
		Session:     &chatpb.Session{SessionId: r.GetSessionId()},
	}, nil
}

func (f *fakeChat) Abort(_ context.Context, r *chatpb.AbortRequest) (*chatpb.AbortResponse, error) {
	f.aborts <- r
	return &chatpb.AbortResponse{Session: &chatpb.Session{SessionId: r.GetSessionId()}}, nil
}

func (f *fakeChat) WatchEvents(
	r *chatpb.WatchEventsRequest,
	stream grpc.ServerStreamingServer[chatpb.WatchEventsResponse],
) error {
	f.mu.Lock()
	if f.gone {
		f.mu.Unlock()
		f.watches <- r
		return status.Error(codes.NotFound, "agent restarted")
	}
	ch := make(chan *chatpb.SessionEvent, 1024)
	f.subscribers[ch] = struct{}{}
	for _, event := range f.events {
		if event.GetEventIndex() > r.GetAfterEventIndex() {
			ch <- event
		}
	}
	f.mu.Unlock()
	f.watches <- r
	defer func() { f.mu.Lock(); delete(f.subscribers, ch); f.mu.Unlock() }()
	for {
		select {
		case <-stream.Context().Done():
			return nil
		case <-f.drops:
			return nil
		case event := <-ch:
			if err := stream.Send(&chatpb.WatchEventsResponse{Event: event}); err != nil {
				return err
			}
		}
	}
}

func (f *fakeChat) ListTurns(
	_ context.Context,
	r *chatpb.ListTurnsRequest,
) (*chatpb.ListTurnsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	page := &chatpb.ListTurnsResponse{HeadSeq: f.head}
	limit := int(r.GetLimit())
	if limit == 0 {
		limit = 50
	}
	for _, turn := range f.turns {
		if r.GetAfterSeq() != 0 && turn.GetSeq() >= r.GetAfterSeq() {
			continue
		}
		if len(page.Turns) == limit {
			page.HasMore = true
			break
		}
		page.Turns = append(page.Turns, turn)
	}
	return page, nil
}

func (f *fakeChat) Deliver(
	_ *chatpb.DeliverRequest,
	stream grpc.ServerStreamingServer[chatpb.DeliverResponse],
) error {
	for {
		select {
		case <-stream.Context().Done():
			return nil
		case message := <-f.outbound:
			if err := stream.Send(message); err != nil {
				return err
			}
		}
	}
}

func (f *fakeChat) emit(event *chatpb.SessionEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	event = proto.Clone(event).(*chatpb.SessionEvent)
	event.EventIndex = int64(len(f.events) + 1)
	event.OccurredAt = timestamppb.New(fixtureTime)
	f.events = append(f.events, event)
	for ch := range f.subscribers {
		ch <- event
	}
}

func setup(t *testing.T, fake *fakeChat) (*Server, *httptest.Server) {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	rpc := grpc.NewServer()
	chatpb.RegisterChatServiceServer(rpc, fake)
	go func() { _ = rpc.Serve(listener) }()
	t.Cleanup(rpc.Stop)
	client, err := bridge.NewClient(
		context.Background(),
		"unix:///bufconn",
		grpc.WithContextDialer(
			func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) },
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	valid := &atomic.Bool{}
	valid.Store(true)
	authorizer := testAuthorizer{valid: valid}
	content, err := assets.New(fstest.MapFS{"index.html": {Data: []byte("<html>chat</html>")}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = content.Close() })
	s, err := New(
		context.Background(),
		client,
		Config{Origin: "https://chat.example", Authorizer: authorizer, Assets: content},
	)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(s)
	t.Cleanup(httpServer.Close)
	t.Cleanup(s.Close)
	return s, httpServer
}

func dial(t *testing.T, server *httptest.Server) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, response, err := websocket.Dial(
		ctx,
		"ws"+strings.TrimPrefix(server.URL, "http")+"/ws",
		&websocket.DialOptions{HTTPHeader: http.Header{
			"Cookie": {
				"test-session=owner",
			}, "Origin": {"https://chat.example"}, "Sec-Fetch-Site": {"same-origin"},
		}},
	)
	if err != nil {
		if response != nil {
			t.Fatalf("dial %v, status %d", err, response.StatusCode)
		}
		t.Fatal(err)
	}
	conn.SetReadLimit(16 << 20)
	t.Cleanup(func() { _ = conn.CloseNow() })
	return conn
}

func send(t *testing.T, conn *websocket.Conn, kind string, payload any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := wsjson.Write(ctx, conn, protocol.New(kind, "request-1", 0, fixtureTime, payload)); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, conn *websocket.Conn) protocol.Frame {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var frame protocol.Frame
	if err := wsjson.Read(ctx, conn, &frame); err != nil {
		t.Fatal(err)
	}
	return frame
}

func hello(t *testing.T, conn *websocket.Conn, cursor int64) []protocol.Frame {
	t.Helper()
	send(t, conn, protocol.Hello, protocol.Cursor{Cursor: cursor})
	var frames []protocol.Frame
	for {
		frame := read(t, conn)
		if frame.Type == protocol.Ready {
			return frames
		}
		if frame.Type == protocol.Error {
			t.Fatalf("hello error %s", frame.Payload)
		}
		frames = append(frames, frame)
	}
}

func take[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for RPC")
		var zero T
		return zero
	}
}

func golden(t *testing.T, name string, value any) {
	t.Helper()
	expected, err := os.ReadFile("testdata/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	actual, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, expected); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, compact.Bytes()) {
		t.Fatalf("%s\ngot: %s\nwant: %s", name, actual, compact.Bytes())
	}
}

func TestStreamedTurnAndDeviceFanout(t *testing.T) {
	fake := newFakeChat()
	_, httpServer := setup(t, fake)
	first, second := dial(t, httpServer), dial(t, httpServer)
	hello(t, first, 0)
	hello(t, second, 0)
	send(t, first, protocol.Send, protocol.SendRequest{ClientMsgID: "client-1", Text: "hello"})
	request := take(t, fake.sends)
	watch := take(t, fake.watches)
	if request.GetSessionId() != watch.GetSessionId() || request.GetText() != "hello" {
		t.Fatalf("RPC mismatch %v/%v", request, watch)
	}
	call := &chatpb.ToolCall{Id: "call-1", Name: "bash", Arguments: `{"command":"pwd"}`}
	events := []*chatpb.SessionEvent{
		{
			Event: &chatpb.SessionEvent_SessionOpened{
				SessionOpened: &chatpb.SessionOpened{SessionId: "session-1"},
			},
		},
		{TurnSeq: 42, Event: &chatpb.SessionEvent_RunStarted{RunStarted: &chatpb.RunStarted{}}},
		{
			TurnSeq: 42,
			Event: &chatpb.SessionEvent_ModelTurnStarted{
				ModelTurnStarted: &chatpb.ModelTurnStarted{LoopTurn: 1, ModelRef: "model-1"},
			},
		},
		{
			TurnSeq: 42,
			Event: &chatpb.SessionEvent_ModelReasoningDelta{
				ModelReasoningDelta: &chatpb.ModelReasoningDelta{Delta: "thinking"},
			},
		},
		{
			TurnSeq: 42,
			Event: &chatpb.SessionEvent_ModelTurnDelta{
				ModelTurnDelta: &chatpb.ModelTurnDelta{Delta: "answer"},
			},
		},
		{
			TurnSeq: 42,
			Event:   &chatpb.SessionEvent_ToolStarted{ToolStarted: &chatpb.ToolStarted{Call: call}},
		},
		{
			TurnSeq: 42,
			Event: &chatpb.SessionEvent_ToolFinished{
				ToolFinished: &chatpb.ToolFinished{Call: call, Output: "/workspace", IsError: true},
			},
		},
		{
			TurnSeq: 42,
			Event:   &chatpb.SessionEvent_Snapshot{Snapshot: &chatpb.Snapshot{Text: "answer"}},
		},
		{
			TurnSeq: 42,
			Event: &chatpb.SessionEvent_RunFinished{
				RunFinished: &chatpb.RunFinished{
					FullText: "answer",
					Status:   chatpb.RunStatus_RUN_STATUS_COMPLETED,
					ModelRef: "model-1",
				},
			},
		},
	}
	for _, event := range events {
		fake.emit(event)
	}
	for _, conn := range []*websocket.Conn{first, second} {
		var frames []protocol.Frame
		for len(frames) < len(events) {
			frame := read(t, conn)
			if frame.Seq > 0 {
				frames = append(frames, frame)
			}
		}
		golden(t, "streamed", frames)
	}
	// A second device sends through the same logical session and stream.
	send(t, second, protocol.Send, protocol.SendRequest{ClientMsgID: "client-2", Text: "again"})
	if next := take(t, fake.sends); next.GetSessionId() != request.GetSessionId() {
		t.Fatal("device opened another session")
	}
	fake.mu.Lock()
	opens := fake.opens
	fake.mu.Unlock()
	if opens != 1 || len(fake.watches) != 0 {
		t.Fatalf("opens=%d extra watches=%d", opens, len(fake.watches))
	}
}

func TestAbortAndFailedFinals(t *testing.T) {
	fake := newFakeChat()
	_, httpServer := setup(t, fake)
	conn := dial(t, httpServer)
	hello(t, conn, 0)
	send(t, conn, protocol.Send, protocol.SendRequest{ClientMsgID: "client-1", Text: "hello"})
	take(t, fake.sends)
	take(t, fake.watches)
	send(t, conn, protocol.Abort, protocol.AbortRequest{Turn: 42})
	if abort := take(t, fake.aborts); abort.GetTurnSeq() != 42 ||
		abort.GetSessionId() != "session-1" {
		t.Fatalf("abort %v", abort)
	}
	fake.emit(
		&chatpb.SessionEvent{
			TurnSeq: 42,
			Event: &chatpb.SessionEvent_RunFinished{
				RunFinished: &chatpb.RunFinished{
					FullText: "partial answer",
					Status:   chatpb.RunStatus_RUN_STATUS_ABORTED,
				},
			},
		},
	)
	for {
		frame := read(t, conn)
		if frame.Type == protocol.Final {
			golden(t, "aborted", frame)
			break
		}
	}
	fake.emit(
		&chatpb.SessionEvent{
			TurnSeq: 43,
			Event: &chatpb.SessionEvent_RunFailed{
				RunFailed: &chatpb.RunFailed{
					FullText: "partial failure",
					Error:    "internal secret must not escape",
					Status:   chatpb.RunStatus_RUN_STATUS_FAILED,
				},
			},
		},
	)
	for {
		frame := read(t, conn)
		if frame.Type == protocol.Final {
			golden(t, "failed", frame)
			break
		}
	}
}

func TestHistoryAndResumeExcludeLiveTurn(t *testing.T) {
	fake := newFakeChat()
	fake.head = 43
	fake.turns = []*chatpb.Turn{
		{Seq: 42, CreatedAt: timestamppb.New(fixtureTime), Messages: []*chatpb.Message{
			{
				Ordinal: 0,
				Role:    "user",
				Parts:   []*chatpb.MessagePart{{Ordinal: 0, PartType: "text", Text: "hello"}},
			},
			{
				Ordinal: 1,
				Role:    "assistant",
				Parts: []*chatpb.MessagePart{
					{Ordinal: 0, PartType: "reasoning", Text: "thinking"},
					{Ordinal: 1, PartType: "text", Text: "answer", Disposition: "final"},
				},
			},
		}},
		{Seq: 40, CreatedAt: timestamppb.New(fixtureTime)},
	}
	_, httpServer := setup(t, fake)
	r, err := http.NewRequest("GET", httpServer.URL+"/api/turns?limit=1", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Cookie", "test-session=owner")
	response, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var page protocol.Page
	if err := json.NewDecoder(response.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	golden(t, "history", page)
	conn := dial(t, httpServer)
	frames := hello(t, conn, 40)
	golden(t, "resumed", frames)
	send(t, conn, protocol.Sync, protocol.Cursor{Cursor: 42})
	ready := read(t, conn)
	if ready.Type != protocol.Ready {
		t.Fatalf("sync %s", ready.Type)
	}
	var payload protocol.ReadyPayload
	if err := json.Unmarshal(ready.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.HeadSeq != 43 || payload.Cursor != 42 {
		t.Fatalf("allocated head became readable cursor: %v", payload)
	}
}

func TestUnauthorizedRouteMatrix(t *testing.T) {
	_, httpServer := setup(t, newFakeChat())
	for _, path := range []string{"/", "/ws", "/api/turns", "/api/nope", "/assets/app.js", "/deep/link", "/sw.js", "/healthz/"} {
		for _, method := range []string{"GET", "POST", "HEAD"} {
			r, err := http.NewRequest(method, httpServer.URL+path, nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := http.DefaultClient.Do(r)
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			if response.StatusCode != 401 {
				t.Errorf("%s %s = %d", method, path, response.StatusCode)
			}
			if response.Header.Get("Content-Security-Policy") == "" {
				t.Errorf("%s missing policy", path)
			}
		}
	}
	response, err := http.Get(httpServer.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("health %d", response.StatusCode)
	}
}

func TestBadHistoryQueriesAndSocketOrigin(t *testing.T) {
	_, httpServer := setup(t, newFakeChat())
	for _, query := range []string{"after_seq=-1", "after_seq=garbage", "after_seq=1&after_seq=2", "limit=0", "limit=501", "limit=x"} {
		r, err := http.NewRequest("GET", httpServer.URL+"/api/turns?"+query, nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Cookie", "test-session=owner")
		response, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != 400 {
			t.Errorf("%s = %d", query, response.StatusCode)
		}
	}
	for _, origin := range []string{"", "https://evil.example", "https://chat.example"} {
		r, err := http.NewRequest("GET", httpServer.URL+"/ws", nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Cookie", "test-session=owner")
		r.Header.Set("Origin", origin)
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		response, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		want := 403
		if origin == "https://chat.example" {
			want = 426
		}
		if response.StatusCode != want {
			t.Errorf("origin %q = %d, want %d", origin, response.StatusCode, want)
		}
	}
}

// Transport tests use an isolated fake authorizer; auth's tests verify WebAuthn.
type testAuthorizer struct{ valid *atomic.Bool }

func (a testAuthorizer) SessionValid(context.Context) bool { return a.valid.Load() }

func (a testAuthorizer) RequireScope(scope string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("test-session")
		if err != nil || cookie.Value != "owner" || !a.valid.Load() {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if scope != "chat" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(
			w,
			r.WithContext(gate.WithPrincipal(r.Context(), gate.Principal{ID: "owner"})),
		)
	})
}

func TestSocketRevalidatesSession(t *testing.T) {
	for _, input := range []bool{false, true} {
		t.Run(fmt.Sprint(input), func(t *testing.T) {
			fake := newFakeChat()
			s, httpServer := setup(t, fake)
			conn := dial(t, httpServer)
			hello(t, conn, 0)
			s.config.Authorizer.(testAuthorizer).valid.Store(false)
			if input {
				send(
					t,
					conn,
					"msg.send",
					map[string]string{"text": "refused", "client_msg_id": "revoked"},
				)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, _, err := conn.Read(ctx)
			if websocket.CloseStatus(err) != websocket.StatusCode(4401) {
				t.Fatalf("revoked socket: %v", err)
			}
			select {
			case request := <-fake.sends:
				t.Fatalf("revoked input reached bridge: %v", request)
			default:
			}
		})
	}
}
