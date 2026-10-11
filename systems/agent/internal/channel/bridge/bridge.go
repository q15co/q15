// Package bridge adapts the agent's durable transcript to the frozen chat
// contract. It is the only place that translates canonical conversation state
// into chatpb messages, and it is a pure transport adapter: everything below
// it stays in canonical types and everything above it speaks the contract.
package bridge

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/q15co/q15/libs/chat-contract/browser"
	"github.com/q15co/q15/libs/chat-contract/chatpb"
	"github.com/q15co/q15/systems/agent/internal/media"
	"github.com/q15co/q15/systems/agent/internal/memory"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// serviceVersion is what the bridge reports as service_version. The agent has
// no build-stamp mechanism in the tree, so this is a named constant rather
// than a build-injected value; it stays "dev" until one exists.
const serviceVersion = "dev"

// maxSendTextLen bounds one client message's text. 48 KiB is generous for a
// human message including a pasted block, and far below the transport's 4 MiB
// default receive limit, so an oversized send is rejected here as a clear
// contract error instead of being parsed, accepted, and then repeated into
// the transcript, the provider's context and the git history on every run.
const maxSendTextLen = 48 * 1024

// TurnLister is the transcript read surface the bridge serves. It is an
// interface rather than *memory.Store so tests can fake the page and the head
// and so the bridge carries no other store dependency.
type TurnLister interface {
	ListTurns(ctx context.Context, afterSeq int64, limit int) (memory.TurnPage, error)
	// LoadHead returns the transcript head as the store holds it. It may sit
	// above the newest turn record while a run is in flight.
	LoadHead(ctx context.Context) (int64, time.Time, error)
}

var _ TurnLister = (*memory.Store)(nil)

// Service keeps canonical host state behind both console and sealed browser adapters.
type Service struct {
	chatpb.UnimplementedChatServiceServer

	lister   TurnLister
	sessions *AgentEndpoint
	browser  *browser.Endpoint
	media    media.Store
}

// NewService constructs a bridge service over the transcript, session endpoint,
// and runtime-owned media store.
func NewService(lister TurnLister, sessions *AgentEndpoint, mediaStore media.Store) *Service {
	s := &Service{lister: lister, sessions: sessions, media: mediaStore}
	s.browser = browser.NewLocal(s)
	return s
}

// OpenSession allocates a logical bridge session. Empty chat_id selects
// defaultBridgeChatID, this agent's single durable transcript.
func (s *Service) OpenSession(
	_ context.Context,
	req *chatpb.OpenSessionRequest,
) (*chatpb.OpenSessionResponse, error) {
	return &chatpb.OpenSessionResponse{
		Session: s.sessions.NewSession(req.GetChatId()),
	}, nil
}

// SendMessage publishes one user message to the runtime bus and returns as
// soon as it is queued. The run is driven by the app worker, not by this rpc,
// so nothing here waits for a run. Because the worker is sequential, a send
// published while a run is in flight waits on the bus, and queued reports
// that to the client.
func (s *Service) SendMessage(
	ctx context.Context,
	req *chatpb.SendMessageRequest,
) (*chatpb.SendMessageResponse, error) {
	if strings.TrimSpace(req.GetText()) == "" && len(req.GetParts()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "send message parts are required")
	}
	text := req.GetText()
	if len(text) > maxSendTextLen {
		// The bound is at the rpc edge, before the send reaches the bus: one
		// message's text repeats into the transcript, the provider's context
		// and the git history, so the cap applies to every run it feeds.
		return nil, status.Errorf(
			codes.InvalidArgument,
			"send message text is %d bytes, over the %d byte limit",
			len(text),
			maxSendTextLen,
		)
	}
	session := s.sessions.lookupSession(strings.TrimSpace(req.GetSessionId()))
	if session == nil {
		return nil, status.Errorf(
			codes.NotFound,
			"bridge session %q not found",
			req.GetSessionId(),
		)
	}

	attachments, err := s.sendAttachments(req.GetParts())
	if err != nil {
		return nil, err
	}

	// Read before publishing: the run this send may join is already in flight
	// exactly when the session is running now.
	queued := session.isRunning()
	if err := s.sessions.publishSend(
		ctx,
		session,
		req.GetClientMsgId(),
		req.GetText(),
		attachments,
	); err != nil {
		return nil, publishSendError(err)
	}
	return &chatpb.SendMessageResponse{
		ClientMsgId: req.GetClientMsgId(),
		Queued:      queued,
		Session:     session.snapshot(),
	}, nil
}

// publishSendError maps a failed bus publish to its contract status: a dead
// publisher context surfaces as itself and anything else is Internal.
func publishSendError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "publish send: canceled")
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "publish send: timed out")
	default:
		return status.Errorf(codes.Internal, "publish send: %v", err)
	}
}

// Abort cancels the in-flight run of a session and returns it.
//
// Nothing in flight is deliberately not an error: a client racing a finished
// run still gets the session, not a failure. A nonzero turn_seq targets only
// that run, so a delayed Stop cannot cancel its successor. Zero targets the
// current run, including its startup before the observer receives a sequence.
func (s *Service) Abort(
	_ context.Context,
	req *chatpb.AbortRequest,
) (*chatpb.AbortResponse, error) {
	session := s.sessions.lookupSession(strings.TrimSpace(req.GetSessionId()))
	if session == nil {
		return nil, status.Errorf(
			codes.NotFound,
			"bridge session %q not found",
			req.GetSessionId(),
		)
	}
	session.cancelRun(req.GetTurnSeq())
	return &chatpb.AbortResponse{Session: session.snapshot()}, nil
}

// ListTurns pages the durable transcript, newest first. It is read-only on
// both sides: no request state changes and the page is translated in place.
func (s *Service) ListTurns(
	ctx context.Context,
	req *chatpb.ListTurnsRequest,
) (*chatpb.ListTurnsResponse, error) {
	page, err := s.lister.ListTurns(ctx, req.GetAfterSeq(), int(req.GetLimit()))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list turns: %v", err)
	}
	return turnsToProto(page), nil
}

// GetRuntimeInfo answers the startup handshake. head_seq is returned as the
// store holds it and is deliberately not reconciled against the turn files: a
// run reserves its sequence before it writes the turn record, so while a run
// is in flight head_seq names a turn whose file does not exist yet.
func (s *Service) GetRuntimeInfo(
	ctx context.Context,
	_ *chatpb.GetRuntimeInfoRequest,
) (*chatpb.GetRuntimeInfoResponse, error) {
	headSeq, _, err := s.lister.LoadHead(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read transcript head: %v", err)
	}
	return &chatpb.GetRuntimeInfoResponse{
		ServiceVersion:  serviceVersion,
		ProtocolVersion: chatpb.ProtocolVersion,
		HeadSeq:         headSeq,
		// Capabilities stay empty: they are advertised when a consumer needs
		// them, and this layer invents none.
		Capabilities: []*chatpb.RuntimeCapability{},
	}, nil
}

// BrowserChannel seals content before it leaves the agent and unseals before bus publication.
func (s *Service) BrowserChannel(
	stream grpc.BidiStreamingServer[chatpb.BrowserPacket, chatpb.BrowserPacket],
) error {
	return s.browser.Channel(stream)
}

// BrowserHistory wraps host records for an active connection without changing their storage format.
func (s *Service) BrowserHistory(
	ctx context.Context,
	req *chatpb.BrowserHistoryRequest,
) (*chatpb.BrowserPacket, error) {
	return s.browser.History(ctx, req)
}
