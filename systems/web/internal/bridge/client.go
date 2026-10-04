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

// Service exposes only opaque browser traffic to the web tier.
type Service interface {
	GetRuntimeInfo(context.Context) (*chatpb.GetRuntimeInfoResponse, error)
	BrowserChannel(
		context.Context,
	) (grpc.BidiStreamingClient[chatpb.BrowserPacket, chatpb.BrowserPacket], error)
	BrowserHistory(context.Context, *chatpb.BrowserHistoryRequest) (*chatpb.BrowserPacket, error)
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

// BrowserChannel confines web traffic to opaque frames and authorized session identity.
func (c *Client) BrowserChannel(
	ctx context.Context,
) (grpc.BidiStreamingClient[chatpb.BrowserPacket, chatpb.BrowserPacket], error) {
	return c.client.BrowserChannel(ctx)
}

// BrowserHistory pages content using an agent-owned key that this client never receives.
func (c *Client) BrowserHistory(
	ctx context.Context,
	req *chatpb.BrowserHistoryRequest,
) (*chatpb.BrowserPacket, error) {
	return c.client.BrowserHistory(ctx, req)
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
