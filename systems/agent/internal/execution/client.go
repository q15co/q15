package execution

import (
	"context"
	"fmt"
	"strings"

	"github.com/q15co/q15/libs/exec-contract/execpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// WatchStream receives session events from the exec service.
type WatchStream interface {
	Recv() (*execpb.WatchSessionResponse, error)
}

// Service is the client surface the agent tools use.
type Service interface {
	GetRuntimeInfo(ctx context.Context) (*execpb.GetRuntimeInfoResponse, error)
	StartSession(
		ctx context.Context,
		req *execpb.StartSessionRequest,
	) (*execpb.StartSessionResponse, error)
	GetSession(
		ctx context.Context,
		req *execpb.GetSessionRequest,
	) (*execpb.GetSessionResponse, error)
	WatchSession(ctx context.Context, req *execpb.WatchSessionRequest) (WatchStream, error)
	WriteSessionStdin(
		ctx context.Context,
		req *execpb.WriteSessionStdinRequest,
	) (*execpb.WriteSessionStdinResponse, error)
	TerminateSession(
		ctx context.Context,
		req *execpb.TerminateSessionRequest,
	) (*execpb.TerminateSessionResponse, error)
	Close() error
}

// Client is a thin gRPC adapter for the exec service.
type Client struct {
	conn   *grpc.ClientConn
	client execpb.ExecutionServiceClient
}

// NewClient constructs a client for the remote exec service over plaintext gRPC.
func NewClient(ctx context.Context, target string) (*Client, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, fmt.Errorf("service address is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	conn, err := grpc.NewClient(
		target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, err
	}
	return &Client{
		conn:   conn,
		client: execpb.NewExecutionServiceClient(conn),
	}, nil
}

// Close closes the underlying gRPC connection.
func (c *Client) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// GetRuntimeInfo fetches service runtime metadata.
func (c *Client) GetRuntimeInfo(
	ctx context.Context,
) (*execpb.GetRuntimeInfoResponse, error) {
	return c.client.GetRuntimeInfo(ctx, &execpb.GetRuntimeInfoRequest{})
}

// StartSession forwards one start request to the exec service.
func (c *Client) StartSession(
	ctx context.Context,
	req *execpb.StartSessionRequest,
) (*execpb.StartSessionResponse, error) {
	return c.client.StartSession(ctx, req)
}

// GetSession returns the current session snapshot.
func (c *Client) GetSession(
	ctx context.Context,
	req *execpb.GetSessionRequest,
) (*execpb.GetSessionResponse, error) {
	return c.client.GetSession(ctx, req)
}

// WatchSession opens one event stream.
func (c *Client) WatchSession(
	ctx context.Context,
	req *execpb.WatchSessionRequest,
) (WatchStream, error) {
	return c.client.WatchSession(ctx, req)
}

// WriteSessionStdin forwards stdin data into one running session.
func (c *Client) WriteSessionStdin(
	ctx context.Context,
	req *execpb.WriteSessionStdinRequest,
) (*execpb.WriteSessionStdinResponse, error) {
	return c.client.WriteSessionStdin(ctx, req)
}

// TerminateSession requests termination for one running session.
func (c *Client) TerminateSession(
	ctx context.Context,
	req *execpb.TerminateSessionRequest,
) (*execpb.TerminateSessionResponse, error) {
	return c.client.TerminateSession(ctx, req)
}
