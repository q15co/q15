// Package bridge adapts the agent's durable transcript to the frozen chat
// contract. It is the only place that translates canonical conversation state
// into chatpb messages, and it is a pure transport adapter: everything below
// it stays in canonical types and everything above it speaks the contract.
package bridge

import (
	"context"
	"time"

	"github.com/q15co/q15/libs/chat-contract/chatpb"
	"github.com/q15co/q15/systems/agent/internal/memory"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// serviceVersion is what the bridge reports as service_version. The agent has
// no build-stamp mechanism in the tree, so this is a named constant rather
// than a build-injected value; it stays "dev" until one exists.
const serviceVersion = "dev"

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

// Service is the chat-contract server adapter. ListTurns and GetRuntimeInfo
// are implemented; the remaining rpcs stay unimplemented until their layer
// arrives.
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

// GetRuntimeInfo answers the startup handshake. head_seq is returned as the
// store holds it and is deliberately not reconciled against the turn files: a
// run reserves its sequence before it writes the turn record, so while a run
// is in flight head_seq names a turn whose file does not exist yet.
func (s *Service) GetRuntimeInfo(
	ctx context.Context,
	req *chatpb.GetRuntimeInfoRequest,
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
