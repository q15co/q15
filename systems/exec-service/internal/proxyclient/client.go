// Package proxyclient provides a thin gRPC client for q15 proxy-service.
package proxyclient

import (
	"context"
	"fmt"
	"strings"

	"github.com/q15co/q15/libs/proxy-contract/proxypb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Client is a thin gRPC adapter for the proxy admin service.
type Client struct {
	conn   *grpc.ClientConn
	client proxypb.ProxyServiceClient
}

// New constructs a client for the remote proxy admin service over plaintext gRPC.
func New(ctx context.Context, target string) (*Client, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, fmt.Errorf("proxy admin address is required")
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
		client: proxypb.NewProxyServiceClient(conn),
	}, nil
}

// Close closes the underlying gRPC connection.
func (c *Client) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// GetRuntimeInfo fetches proxy runtime metadata.
func (c *Client) GetRuntimeInfo(ctx context.Context) (*proxypb.GetRuntimeInfoResponse, error) {
	return c.client.GetRuntimeInfo(ctx, &proxypb.GetRuntimeInfoRequest{})
}
