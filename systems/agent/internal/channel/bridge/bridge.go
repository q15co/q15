// Package bridge adapts the agent's durable transcript to the frozen chat
// contract. It is the only place that translates canonical conversation state
// into chatpb messages, and it is a pure transport adapter: everything below
// it stays in canonical types and everything above it speaks the contract.
package bridge

import (
	"context"

	"github.com/q15co/q15/libs/chat-contract/chatpb"
	"github.com/q15co/q15/systems/agent/internal/memory"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TurnLister is the transcript read surface the bridge serves. It is an
// interface rather than *memory.Store so tests can fake the page and so the
// bridge carries no other store dependency.
type TurnLister interface {
	ListTurns(ctx context.Context, afterSeq int64, limit int) (memory.TurnPage, error)
}

var _ TurnLister = (*memory.Store)(nil)

// Service is the chat-contract server adapter. ListTurns is implemented; the
// remaining rpcs stay unimplemented until their layer arrives.
type Service struct {
	chatpb.UnimplementedChatServiceServer

	lister TurnLister
}

// NewService constructs a bridge service over one transcript lister.
func NewService(lister TurnLister) *Service {
	return &Service{lister: lister}
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
