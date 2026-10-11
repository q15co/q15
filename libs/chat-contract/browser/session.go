package browser

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/q15co/q15/libs/chat-contract/browser/protocol"
	"github.com/q15co/q15/libs/chat-contract/chatpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const streamRetryInterval = time.Second
const maxDraftBytes = 4 << 20

// session is one principal's shared bridge handle, independent of device
// lifetimes. Reconnecting devices do not open additional WatchEvents streams.
type session struct {
	owner     string
	server    *Endpoint
	commands  sync.Mutex
	mu        sync.Mutex
	id        string
	index     int64
	turn      int64
	running   bool
	text      string
	reasoning string
	lastTime  time.Time
}

func (s *session) send(ctx context.Context, ref string, request protocol.SendRequest) error {
	if strings.TrimSpace(request.Text) == "" && len(request.Parts) == 0 {
		return fmt.Errorf("message text is required")
	}
	s.commands.Lock()
	defer s.commands.Unlock()
	s.mu.Lock()
	id := s.id
	s.mu.Unlock()
	if id == "" {
		response, err := s.server.service.OpenSession(ctx, &chatpb.OpenSessionRequest{})
		if err != nil {
			return err
		}
		id = response.GetSession().GetSessionId()
		if id == "" {
			return fmt.Errorf("bridge returned no session")
		}
		s.mu.Lock()
		s.id = id
		s.mu.Unlock()
		if !s.server.start(func() { s.watch(id) }) {
			return context.Canceled
		}
	}
	parts := make([]*chatpb.MessagePart, 0, len(request.Parts))
	for _, part := range request.Parts {
		parts = append(
			parts,
			&chatpb.MessagePart{
				PartType:  part.PartType,
				MediaKind: part.MediaKind,
				MediaRef:  part.MediaRef,
			},
		)
	}
	response, err := s.server.service.SendMessage(ctx, &chatpb.SendMessageRequest{
		SessionId: id, ClientMsgId: request.ClientMsgID, Text: request.Text, Parts: parts})
	if err != nil {
		return err
	}
	state := "accepted"
	if response.GetQueued() {
		state = "queued"
	}
	s.mu.Lock()
	turn := s.turn
	s.mu.Unlock()
	s.server.registry.broadcast(s.owner, protocol.New(
		protocol.Status,
		ref,
		0,
		time.Now(),
		protocol.StatusPayload{
			Turn:        turn,
			State:       state,
			ClientMsgID: response.GetClientMsgId(),
			Queued:      response.GetQueued(),
		},
	))
	return nil
}

func (s *session) abort(ctx context.Context, turn int64) error {
	s.commands.Lock()
	defer s.commands.Unlock()
	s.mu.Lock()
	id := s.id
	s.mu.Unlock()
	if id == "" {
		return nil
	}
	_, err := s.server.service.Abort(
		ctx,
		&chatpb.AbortRequest{SessionId: id, TurnSeq: turn, Reason: "browser abort"},
	)
	return err
}

func (s *session) watch(id string) {
	ctx := s.server.ctx
	for ctx.Err() == nil {
		s.mu.Lock()
		index := s.index
		s.mu.Unlock()
		stream, err := s.server.service.WatchEvents(
			ctx,
			&chatpb.WatchEventsRequest{SessionId: id, AfterEventIndex: index},
		)
		if err == nil {
			for {
				response, recvErr := stream.Recv()
				if recvErr != nil {
					err = recvErr
					break
				}
				s.event(response.GetEvent())
			}
		}
		if status.Code(err) == codes.NotFound {
			// A restarted agent lost its ephemeral sessions. Discard the handle
			// before any later send; history still comes from the durable agent.
			s.commands.Lock()
			s.mu.Lock()
			s.id, s.index, s.turn, s.running, s.text, s.reasoning = "", 0, 0, false, "", ""
			s.mu.Unlock()
			s.commands.Unlock()
			s.server.registry.broadcast(s.owner, protocol.New(protocol.Error, "", 0, time.Now(),
				protocol.ErrorPayload{Code: "resync_from_head", Ref: ""}))
			return
		}
		if !retry(ctx) {
			return
		}
	}
}

func (s *session) event(event *chatpb.SessionEvent) {
	if event == nil {
		return
	}
	frame, ok := protocol.FromEvent(event)
	s.mu.Lock()
	if event.GetEventIndex() <= s.index {
		// A retention notice may sort below the last cursor; surface it while
		// retaining the monotonic receiver cursor used for stream recovery.
		if event.GetNotice() == nil {
			s.mu.Unlock()
			return
		}
	} else {
		s.index = event.GetEventIndex()
	}
	s.lastTime = frame.TS
	if event.GetTurnSeq() != 0 {
		s.turn = event.GetTurnSeq()
	}
	switch value := event.GetEvent().(type) {
	case *chatpb.SessionEvent_RunStarted:
		s.running, s.text, s.reasoning = true, "", ""
	case *chatpb.SessionEvent_ModelTurnStarted:
		s.running = true
		s.text, s.reasoning = "", ""
	case *chatpb.SessionEvent_ModelTurnDelta:
		s.running = true
		s.text = boundedAppend(s.text, value.ModelTurnDelta.GetDelta())
	case *chatpb.SessionEvent_ModelReasoningDelta:
		s.running = true
		s.reasoning = boundedAppend(s.reasoning, value.ModelReasoningDelta.GetDelta())
	case *chatpb.SessionEvent_Snapshot:
		s.running = true
		s.text = value.Snapshot.GetText()
		if len(s.text) > maxDraftBytes {
			s.text = s.text[:maxDraftBytes]
		}
	case *chatpb.SessionEvent_ToolStarted, *chatpb.SessionEvent_ToolFinished:
		s.running = true
	case *chatpb.SessionEvent_RunFinished, *chatpb.SessionEvent_RunFailed:
		s.running, s.text, s.reasoning = false, "", ""
	}
	s.mu.Unlock()
	if ok {
		s.server.registry.broadcast(s.owner, frame)
	}
}

func boundedAppend(current, delta string) string {
	remaining := maxDraftBytes - len(current)
	if len(delta) > remaining {
		delta = delta[:remaining]
	}
	return current + delta
}

func (s *session) snapshot() (protocol.Frame, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return protocol.Frame{}, false
	}
	return protocol.New(protocol.Snapshot, fmt.Sprintf("event:%d", s.index), s.index, s.lastTime,
		protocol.ProgressPayload{Msg: protocol.MessageID{Turn: s.turn, Ordinal: -1}, Seq: s.index,
			Kind: "text", Text: s.text, Reasoning: s.reasoning}), true
}

func (s *session) statusFrame(ref string) protocol.Frame {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := "idle"
	if s.running {
		state = "running"
	}
	return protocol.New(
		protocol.Status,
		ref,
		0,
		time.Now(),
		protocol.StatusPayload{Turn: s.turn, State: state},
	)
}

func (s *session) validAck(index int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return index >= 0 && index <= s.index
}

func retry(ctx context.Context) bool {
	timer := time.NewTimer(streamRetryInterval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (s *Endpoint) deliver() {
	for s.ctx.Err() == nil {
		stream, err := s.service.Deliver(s.ctx)
		if err == nil {
			for {
				message, err := stream.Recv()
				if err != nil {
					break
				}
				s.registry.broadcast(
					"owner",
					protocol.New(protocol.Notice, "", 0, message.GetQueuedAt().AsTime(),
						protocol.NoticePayload{Code: "outbound", Text: message.GetText()}),
				)
			}
		}
		if !retry(s.ctx) {
			return
		}
	}
}
