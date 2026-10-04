package main

import (
	"context"
	"fmt"
	"sync"

	"github.com/q15co/q15/libs/chat-contract/browser"
	"github.com/q15co/q15/libs/chat-contract/chatpb"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type testAgent struct {
	chatpb.UnimplementedChatServiceServer
	content  *browser.Endpoint
	mu       sync.Mutex
	turns    []*chatpb.Turn
	events   []*chatpb.SessionEvent
	watchers map[chan *chatpb.SessionEvent]struct{}
}

func newTestAgent() *testAgent {
	s := &testAgent{watchers: make(map[chan *chatpb.SessionEvent]struct{})}
	s.turns = []*chatpb.Turn{
		{
			Seq:       41,
			CreatedAt: timestamppb.Now(),
			Messages: []*chatpb.Message{
				{
					Ordinal: 0,
					Role:    "assistant",
					Parts: []*chatpb.MessagePart{
						{Ordinal: 0, PartType: "text", Text: "Saved sealed history"},
					},
				},
			},
		},
	}
	s.content = browser.NewLocal(s)
	return s
}

func (s *testAgent) GetRuntimeInfo(
	context.Context,
	*chatpb.GetRuntimeInfoRequest,
) (*chatpb.GetRuntimeInfoResponse, error) {
	return &chatpb.GetRuntimeInfoResponse{ProtocolVersion: chatpb.ProtocolVersion}, nil
}

func (s *testAgent) BrowserChannel(
	stream grpc.BidiStreamingServer[chatpb.BrowserPacket, chatpb.BrowserPacket],
) error {
	return s.content.Channel(stream)
}

func (s *testAgent) BrowserHistory(
	ctx context.Context,
	req *chatpb.BrowserHistoryRequest,
) (*chatpb.BrowserPacket, error) {
	return s.content.History(ctx, req)
}

func (s *testAgent) OpenSession(
	context.Context,
	*chatpb.OpenSessionRequest,
) (*chatpb.OpenSessionResponse, error) {
	return &chatpb.OpenSessionResponse{Session: &chatpb.Session{SessionId: "browser-test"}}, nil
}

func (s *testAgent) ListTurns(
	_ context.Context,
	req *chatpb.ListTurnsRequest,
) (*chatpb.ListTurnsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	page := &chatpb.ListTurnsResponse{HeadSeq: s.turns[0].Seq}
	for _, turn := range s.turns {
		if req.AfterSeq == 0 || turn.Seq < req.AfterSeq {
			page.Turns = append(page.Turns, turn)
		}
	}
	return page, nil
}

func (s *testAgent) SendMessage(
	_ context.Context,
	req *chatpb.SendMessageRequest,
) (*chatpb.SendMessageResponse, error) {
	s.mu.Lock()
	seq := s.turns[0].Seq + 1
	turn := &chatpb.Turn{Seq: seq, CreatedAt: timestamppb.Now(), Messages: []*chatpb.Message{
		{
			Ordinal: 0,
			Role:    "user",
			Parts:   []*chatpb.MessagePart{{Ordinal: 0, PartType: "text", Text: req.Text}},
		},
		{
			Ordinal: 1,
			Role:    "assistant",
			Parts: []*chatpb.MessagePart{
				{Ordinal: 0, PartType: "text", Text: "Sealed agent reply"},
			},
		},
	}}
	s.turns = append([]*chatpb.Turn{turn}, s.turns...)
	for _, event := range []*chatpb.SessionEvent{
		{Event: &chatpb.SessionEvent_RunStarted{RunStarted: &chatpb.RunStarted{}}},
		{Event: &chatpb.SessionEvent_ModelTurnDelta{ModelTurnDelta: &chatpb.ModelTurnDelta{Delta: "Sealed "}}},
		{Event: &chatpb.SessionEvent_Snapshot{Snapshot: &chatpb.Snapshot{Text: "Sealed agent reply"}}},
		{Event: &chatpb.SessionEvent_RunFinished{RunFinished: &chatpb.RunFinished{FullText: "Sealed agent reply", Status: chatpb.RunStatus_RUN_STATUS_COMPLETED}}},
	} {
		event.EventIndex = int64(len(s.events) + 1)
		event.TurnSeq = seq
		event.OccurredAt = timestamppb.Now()
		s.events = append(s.events, event)
		for ch := range s.watchers {
			select {
			case ch <- event:
			default:
			}
		}
	}
	s.mu.Unlock()
	return &chatpb.SendMessageResponse{
		ClientMsgId: req.ClientMsgId,
		Session:     &chatpb.Session{SessionId: fmt.Sprint(seq)},
	}, nil
}

func (s *testAgent) WatchEvents(
	req *chatpb.WatchEventsRequest,
	stream grpc.ServerStreamingServer[chatpb.WatchEventsResponse],
) error {
	s.mu.Lock()
	ch := make(chan *chatpb.SessionEvent, 32)
	s.watchers[ch] = struct{}{}
	for _, event := range s.events {
		if event.EventIndex > req.AfterEventIndex {
			ch <- event
		}
	}
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.watchers, ch); s.mu.Unlock() }()
	for {
		select {
		case <-stream.Context().Done():
			return nil
		case event := <-ch:
			if err := stream.Send(&chatpb.WatchEventsResponse{Event: event}); err != nil {
				return err
			}
		}
	}
}

func (s *testAgent) Deliver(
	_ *chatpb.DeliverRequest,
	stream grpc.ServerStreamingServer[chatpb.DeliverResponse],
) error {
	<-stream.Context().Done()
	return nil
}
