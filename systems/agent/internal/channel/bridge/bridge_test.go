package bridge

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/q15co/q15/libs/chat-contract/chatpb"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/memory"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

type fakeTurnLister struct {
	page      memory.TurnPage
	err       error
	head      int64
	headErr   error
	calls     int
	afterSeq  int64
	limit     int
	headCalls int
}

func (f *fakeTurnLister) ListTurns(
	ctx context.Context,
	afterSeq int64,
	limit int,
) (memory.TurnPage, error) {
	_ = ctx
	f.calls++
	f.afterSeq = afterSeq
	f.limit = limit
	return f.page, f.err
}

func (f *fakeTurnLister) LoadHead(ctx context.Context) (int64, time.Time, error) {
	_ = ctx
	f.headCalls++
	return f.head, time.Time{}, f.headErr
}

func startBridgeService(t *testing.T, lister TurnLister) chatpb.ChatServiceClient {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	chatpb.RegisterChatServiceServer(server, NewService(lister))
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

func fixtureBridgePage() memory.TurnPage {
	return memory.TurnPage{
		Turns: []memory.Turn{{
			Seq:       11,
			CreatedAt: time.Date(2026, time.April, 8, 12, 30, 0, 123456789, time.UTC),
			Messages: []conversation.Message{
				conversation.UserMessageParts(
					conversation.Text("fixture question", ""),
				),
				conversation.AssistantMessage(
					conversation.Text("fixture answer", conversation.TextDispositionFinal),
					conversation.Reasoning("fixture thought", nil),
					conversation.Image("media://fixture/sha256/abc", ""),
					conversation.ToolCall("call-1", "fixture_tool", `{"path":"fixture"}`),
				),
				conversation.ToolResultMessage("call-1", "fixture content", true),
			},
		}},
		HeadSeq: 12,
		HasMore: true,
	}
}

func TestServiceListTurnsTranscribesTurnsForClients(t *testing.T) {
	lister := &fakeTurnLister{page: fixtureBridgePage()}
	client := startBridgeService(t, lister)

	response, err := client.ListTurns(context.Background(), &chatpb.ListTurnsRequest{
		AfterSeq: 12,
		Limit:    20,
	})
	if err != nil {
		t.Fatalf("ListTurns() error = %v", err)
	}
	if lister.calls != 1 || lister.afterSeq != 12 || lister.limit != 20 {
		t.Fatalf(
			"ListTurns() forwarded (after=%d limit=%d calls=%d), want after=12 limit=20 calls=1",
			lister.afterSeq,
			lister.limit,
			lister.calls,
		)
	}
	if response.GetHeadSeq() != 12 || !response.GetHasMore() {
		t.Fatalf(
			"ListTurns() header = (head=%d more=%v), want head=12 more=true",
			response.GetHeadSeq(),
			response.GetHasMore(),
		)
	}

	if got := len(response.GetTurns()); got != 1 {
		t.Fatalf("ListTurns() turns = %d, want 1", got)
	}
	turn := response.GetTurns()[0]
	if turn.GetSeq() != 11 {
		t.Fatalf("Turn seq = %d, want 11", turn.GetSeq())
	}
	wantCreatedAt := time.Date(2026, time.April, 8, 12, 30, 0, 123456789, time.UTC)
	if !turn.GetCreatedAt().AsTime().Equal(wantCreatedAt) {
		t.Fatalf(
			"Turn created at = %v, want %v", turn.GetCreatedAt().AsTime(), wantCreatedAt,
		)
	}

	// Message ordinals are the message identity: they are the index within
	// the turn, and the transcript has nothing else to identify a message by.
	if got := len(turn.GetMessages()); got != 3 {
		t.Fatalf("Turn messages = %d, want 3", got)
	}
	for i, wantRole := range []string{"user", "assistant", "tool"} {
		message := turn.GetMessages()[i]
		if message.GetOrdinal() != int32(i) {
			t.Fatalf(
				"message %d ordinal = %d, want %d", i, message.GetOrdinal(), i,
			)
		}
		if message.GetRole() != wantRole {
			t.Fatalf("message %d role = %q, want %q", i, message.GetRole(), wantRole)
		}
	}

	// Every part type the contract can carry, in one pass: ordinals are the
	// parts' identity, and type discrimination decides which fields are set.
	assistant := turn.GetMessages()[1]
	if got := len(assistant.GetParts()); got != 4 {
		t.Fatalf("assistant parts = %d, want 4", got)
	}
	for i, part := range assistant.GetParts() {
		if part.GetOrdinal() != int32(i) {
			t.Fatalf(
				"assistant part %d ordinal = %d, want %d",
				i,
				part.GetOrdinal(),
				i,
			)
		}
	}
	text := assistant.GetParts()[0]
	if text.GetPartType() != "text" || text.GetText() != "fixture answer" {
		t.Fatalf("text part = (type %q text %q), want text/fixture answer",
			text.GetPartType(), text.GetText())
	}
	if text.GetDisposition() != "final" {
		t.Fatalf("text disposition = %q, want final", text.GetDisposition())
	}
	reasoning := assistant.GetParts()[1]
	if reasoning.GetPartType() != "reasoning" ||
		reasoning.GetText() != "fixture thought" {
		t.Fatalf(
			"reasoning part = (type %q text %q), want reasoning/fixture thought",
			reasoning.GetPartType(),
			reasoning.GetText(),
		)
	}
	if reasoning.GetDisposition() != "" {
		t.Fatalf(
			"reasoning disposition = %q, want unset", reasoning.GetDisposition(),
		)
	}
	media := assistant.GetParts()[2]
	if media.GetPartType() != "media" ||
		media.GetMediaKind() != "image" ||
		media.GetMediaRef() != "media://fixture/sha256/abc" {
		t.Fatalf(
			"media part = (type %q kind %q ref %q), want image media ref",
			media.GetPartType(),
			media.GetMediaKind(),
			media.GetMediaRef(),
		)
	}
	if media.GetText() != "" || media.GetToolCall() != nil {
		t.Fatalf("media part leaked fields it does not carry: %v", media)
	}
	call := assistant.GetParts()[3]
	if call.GetPartType() != "tool_call" {
		t.Fatalf("tool call part type = %q, want tool_call", call.GetPartType())
	}
	if call.GetToolCall().GetId() != "call-1" ||
		call.GetToolCall().GetName() != "fixture_tool" ||
		call.GetToolCall().GetArguments() != `{"path":"fixture"}` {
		t.Fatalf("tool call = %v, want call-1/fixture_tool", call.GetToolCall())
	}
	if call.GetContent() != "" || call.GetToolCallId() != "" {
		t.Fatalf("tool call part leaked tool result fields: %v", call)
	}

	result := turn.GetMessages()[2].GetParts()[0]
	if result.GetPartType() != "tool_result" {
		t.Fatalf("tool result part type = %q, want tool_result", result.GetPartType())
	}
	if result.GetToolCallId() != "call-1" ||
		result.GetContent() != "fixture content" || !result.GetIsError() {
		t.Fatalf(
			"tool result = (id %q content %q error %v), want failed tool result",
			result.GetToolCallId(),
			result.GetContent(),
			result.GetIsError(),
		)
	}
	if result.GetToolCall() != nil {
		t.Fatalf("tool result part leaked a tool call: %v", result.GetToolCall())
	}
}

func TestServiceListTurnsLeavesRequestDefaultsUntouched(t *testing.T) {
	lister := &fakeTurnLister{page: memory.TurnPage{HeadSeq: 4}}
	client := startBridgeService(t, lister)

	response, err := client.ListTurns(
		context.Background(), &chatpb.ListTurnsRequest{},
	)
	if err != nil {
		t.Fatalf("ListTurns() error = %v", err)
	}
	// The bridge is a pure adapter: it forwards after_seq and limit verbatim
	// and lets the transcript pager own the default and the cap.
	if lister.afterSeq != 0 || lister.limit != 0 || lister.calls != 1 {
		t.Fatalf(
			"ListTurns() forwarded (after=%d limit=%d calls=%d), want zero defaults once",
			lister.afterSeq,
			lister.limit,
			lister.calls,
		)
	}
	if len(response.GetTurns()) != 0 || response.GetHeadSeq() != 4 {
		t.Fatalf("ListTurns() = (turns %d head %d), want empty page at head 4",
			len(response.GetTurns()), response.GetHeadSeq())
	}
}

func TestServiceListTurnsSurfacesListerFailure(t *testing.T) {
	lister := &fakeTurnLister{err: errors.New("transcript unreadable")}
	client := startBridgeService(t, lister)

	_, err := client.ListTurns(context.Background(), &chatpb.ListTurnsRequest{})
	if status.Code(err) != codes.Internal {
		t.Fatalf("ListTurns() code = %v, want Internal (err=%v)", status.Code(err), err)
	}
}

func TestServiceGetRuntimeInfoReportsHandshake(t *testing.T) {
	lister := &fakeTurnLister{page: memory.TurnPage{HeadSeq: 12}, head: 12}
	client := startBridgeService(t, lister)

	response, err := client.GetRuntimeInfo(
		context.Background(), &chatpb.GetRuntimeInfoRequest{},
	)
	if err != nil {
		t.Fatalf("GetRuntimeInfo() error = %v", err)
	}
	if response.GetProtocolVersion() != chatpb.ProtocolVersion {
		t.Fatalf(
			"GetRuntimeInfo() protocol = %d, want %d",
			response.GetProtocolVersion(),
			chatpb.ProtocolVersion,
		)
	}
	// head_seq is the transcript head as the store holds it. It may sit above
	// the newest turn record while a run is in flight; the bridge returns it
	// without reconciling.
	if response.GetHeadSeq() != 12 {
		t.Fatalf("GetRuntimeInfo() head = %d, want 12", response.GetHeadSeq())
	}
	if response.GetServiceVersion() == "" {
		t.Fatalf("GetRuntimeInfo() service version = %q, want non-empty",
			response.GetServiceVersion())
	}
	if got := len(response.GetCapabilities()); got != 0 {
		t.Fatalf(
			"GetRuntimeInfo() capabilities = %d entries, want none advertised",
			got,
		)
	}
	if lister.headCalls != 1 {
		t.Fatalf("LoadHead() calls = %d, want 1", lister.headCalls)
	}
}

func TestServiceGetRuntimeInfoSurfacesHeadFailure(t *testing.T) {
	lister := &fakeTurnLister{headErr: errors.New("head unreadable")}
	client := startBridgeService(t, lister)

	_, err := client.GetRuntimeInfo(
		context.Background(), &chatpb.GetRuntimeInfoRequest{},
	)
	if status.Code(err) != codes.Internal {
		t.Fatalf("GetRuntimeInfo() code = %v, want Internal (err=%v)", status.Code(err), err)
	}
}

func TestServiceStaysUnimplementedBeyondServedRPCs(t *testing.T) {
	client := startBridgeService(t, &fakeTurnLister{})

	for _, rpc := range []struct {
		name string
		call func(context.Context) error
	}{
		{"OpenSession", func(ctx context.Context) error {
			_, err := client.OpenSession(ctx, &chatpb.OpenSessionRequest{})
			return err
		}},
		{"SendMessage", func(ctx context.Context) error {
			_, err := client.SendMessage(ctx, &chatpb.SendMessageRequest{})
			return err
		}},
		{"Abort", func(ctx context.Context) error {
			_, err := client.Abort(ctx, &chatpb.AbortRequest{})
			return err
		}},
		{"WatchEvents", func(ctx context.Context) error {
			stream, err := client.WatchEvents(ctx, &chatpb.WatchEventsRequest{})
			if err != nil {
				return err
			}
			_, err = stream.Recv()
			return err
		}},
		{"Deliver", func(ctx context.Context) error {
			stream, err := client.Deliver(ctx, &chatpb.DeliverRequest{})
			if err != nil {
				return err
			}
			_, err = stream.Recv()
			return err
		}},
	} {
		err := rpc.call(context.Background())
		if status.Code(err) != codes.Unimplemented {
			t.Fatalf(
				"%s() code = %v, want Unimplemented (err=%v)",
				rpc.name,
				status.Code(err),
				err,
			)
		}
	}
}
