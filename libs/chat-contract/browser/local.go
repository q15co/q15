package browser

import (
	"context"
	"io"

	"github.com/q15co/q15/libs/chat-contract/chatpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

type watchStream interface {
	Recv() (*chatpb.WatchEventsResponse, error)
}
type deliverStream interface {
	Recv() (*chatpb.DeliverResponse, error)
}

type localBackend struct{ service chatpb.ChatServiceServer }

// NewLocal calls the agent service in process. Plaintext never traverses the
// bridge socket on the browser path.
func NewLocal(
	service chatpb.ChatServiceServer,
) *Endpoint {
	return newEndpoint(localBackend{service})
}

func (b localBackend) OpenSession(
	ctx context.Context,
	req *chatpb.OpenSessionRequest,
) (*chatpb.OpenSessionResponse, error) {
	return b.service.OpenSession(ctx, req)
}

func (b localBackend) SendMessage(
	ctx context.Context,
	req *chatpb.SendMessageRequest,
) (*chatpb.SendMessageResponse, error) {
	return b.service.SendMessage(ctx, req)
}

func (b localBackend) Abort(
	ctx context.Context,
	req *chatpb.AbortRequest,
) (*chatpb.AbortResponse, error) {
	return b.service.Abort(ctx, req)
}

func (b localBackend) ListTurns(
	ctx context.Context,
	req *chatpb.ListTurnsRequest,
) (*chatpb.ListTurnsResponse, error) {
	return b.service.ListTurns(ctx, req)
}

func (b localBackend) WatchEvents(
	ctx context.Context,
	req *chatpb.WatchEventsRequest,
) (watchStream, error) {
	return localCall(
		ctx,
		func(stream grpc.ServerStreamingServer[chatpb.WatchEventsResponse]) error {
			return b.service.WatchEvents(req, stream)
		},
	), nil
}
func (b localBackend) Deliver(ctx context.Context) (deliverStream, error) {
	return localCall(ctx, func(stream grpc.ServerStreamingServer[chatpb.DeliverResponse]) error {
		return b.service.Deliver(&chatpb.DeliverRequest{}, stream)
	}), nil
}

type localStream[T any] struct {
	ctx    context.Context
	values chan *T
	err    error
}

func localCall[T any](
	ctx context.Context,
	call func(grpc.ServerStreamingServer[T]) error,
) *localStream[T] {
	s := &localStream[T]{ctx: ctx, values: make(chan *T)}
	go func() { s.err = call(s); close(s.values) }()
	return s
}

func (s *localStream[T]) Context() context.Context     { return s.ctx }
func (s *localStream[T]) SetHeader(metadata.MD) error  { return nil }
func (s *localStream[T]) SendHeader(metadata.MD) error { return nil }
func (s *localStream[T]) SetTrailer(metadata.MD)       {}
func (s *localStream[T]) SendMsg(any) error            { return nil }
func (s *localStream[T]) RecvMsg(any) error            { return io.EOF }
func (s *localStream[T]) Send(value *T) error {
	select {
	case <-s.ctx.Done():
		return s.ctx.Err()
	case s.values <- value:
		return nil
	}
}
func (s *localStream[T]) Recv() (*T, error) {
	select {
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	case value, ok := <-s.values:
		if ok {
			return value, nil
		}
		if s.err != nil {
			return nil, s.err
		}
		return nil, io.EOF
	}
}
