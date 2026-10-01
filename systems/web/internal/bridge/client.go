// Package bridge is the web tier's credential-free Unix-socket chat client.
package bridge

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/q15co/q15/libs/chat-contract/chatpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// WatchStream receives progress from one logical session.
type WatchStream interface {
	Recv() (*chatpb.WatchEventsResponse, error)
}

// DeliverStream receives proactive agent output.
type DeliverStream interface {
	Recv() (*chatpb.DeliverResponse, error)
}

// Service is the bridge surface used by the browser server.
type Service interface {
	GetRuntimeInfo(context.Context) (*chatpb.GetRuntimeInfoResponse, error)
	OpenSession(context.Context, *chatpb.OpenSessionRequest) (*chatpb.OpenSessionResponse, error)
	SendMessage(context.Context, *chatpb.SendMessageRequest) (*chatpb.SendMessageResponse, error)
	Abort(context.Context, *chatpb.AbortRequest) (*chatpb.AbortResponse, error)
	WatchEvents(context.Context, *chatpb.WatchEventsRequest) (WatchStream, error)
	ListTurns(context.Context, *chatpb.ListTurnsRequest) (*chatpb.ListTurnsResponse, error)
	Deliver(context.Context) (DeliverStream, error)
}

// Client mirrors the exec client's thin gRPC adapter, restricted to local sockets.
type Client struct {
	conn   *grpc.ClientConn
	client chatpb.ChatServiceClient
}

// NewClient constructs a Unix-socket client without agent credentials.
func NewClient(ctx context.Context, target string, options ...grpc.DialOption) (*Client, error) {
	target = strings.TrimSpace(target)
	if !strings.HasPrefix(target, "unix://") ||
		!filepath.IsAbs(strings.TrimPrefix(target, "unix://")) {
		return nil, fmt.Errorf("bridge address must be unix:///absolute/path")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	options = append(options, grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(16<<20)))
	conn, err := grpc.NewClient(target, options...)
	if err != nil {
		return nil, err
	}
	return &Client{conn: conn, client: chatpb.NewChatServiceClient(conn)}, nil
}

// Close closes the underlying gRPC connection.
func (c *Client) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// GetRuntimeInfo fetches the startup handshake.
func (c *Client) GetRuntimeInfo(ctx context.Context) (*chatpb.GetRuntimeInfoResponse, error) {
	return c.client.GetRuntimeInfo(ctx, &chatpb.GetRuntimeInfoRequest{})
}

// OpenSession allocates a conversation session.
func (c *Client) OpenSession(
	ctx context.Context,
	req *chatpb.OpenSessionRequest,
) (*chatpb.OpenSessionResponse, error) {
	return c.client.OpenSession(ctx, req)
}

// SendMessage submits a message to the session.
func (c *Client) SendMessage(
	ctx context.Context,
	req *chatpb.SendMessageRequest,
) (*chatpb.SendMessageResponse, error) {
	return c.client.SendMessage(ctx, req)
}

// Abort cancels the requested run.
func (c *Client) Abort(
	ctx context.Context,
	req *chatpb.AbortRequest,
) (*chatpb.AbortResponse, error) {
	return c.client.Abort(ctx, req)
}

// WatchEvents opens a resumable session event stream.
func (c *Client) WatchEvents(
	ctx context.Context,
	req *chatpb.WatchEventsRequest,
) (WatchStream, error) {
	return c.client.WatchEvents(ctx, req)
}

// ListTurns pages the agent's durable transcript.
func (c *Client) ListTurns(
	ctx context.Context,
	req *chatpb.ListTurnsRequest,
) (*chatpb.ListTurnsResponse, error) {
	return c.client.ListTurns(ctx, req)
}

// Deliver subscribes to proactive agent output.
func (c *Client) Deliver(ctx context.Context) (DeliverStream, error) {
	return c.client.Deliver(ctx, &chatpb.DeliverRequest{})
}

// CheckVersion rejects every version other than the frozen contract's version.
func CheckVersion(info *chatpb.GetRuntimeInfoResponse) error {
	if info == nil || info.GetProtocolVersion() != chatpb.ProtocolVersion {
		return fmt.Errorf(
			"chat protocol mismatch: agent=%d web=%d",
			info.GetProtocolVersion(),
			chatpb.ProtocolVersion,
		)
	}
	return nil
}

var _ Service = (*Client)(nil)
