package service

import (
	"context"
	"net"
	"testing"

	"github.com/q15co/q15/libs/proxy-contract/proxypb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func TestGRPCServerReturnsRuntimeInfo(t *testing.T) {
	t.Parallel()

	api, err := NewGRPCServer(RuntimeInfo{
		ServiceVersion:       "test",
		AdvertiseProxyURL:    "http://proxy:18080",
		NoProxy:              "localhost,127.0.0.1",
		SetLowercaseProxyEnv: true,
		CACertPEM:            []byte("cert"),
		EnvValues:            map[string]string{"GH_TOKEN": "__Q15_PROXY_ENV_test__"},
		PolicyRevision:       "rev-123",
	})
	if err != nil {
		t.Fatalf("NewGRPCServer() error = %v", err)
	}

	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	api.Register(server)
	go func() {
		_ = server.Serve(listener)
	}()
	defer server.Stop()

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return listener.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient() error = %v", err)
	}
	defer conn.Close()

	client := proxypb.NewProxyServiceClient(conn)
	info, err := client.GetRuntimeInfo(context.Background(), &proxypb.GetRuntimeInfoRequest{})
	if err != nil {
		t.Fatalf("GetRuntimeInfo() error = %v", err)
	}
	if got := info.GetAdvertiseProxyUrl(); got != "http://proxy:18080" {
		t.Fatalf("unexpected advertise proxy url: %q", got)
	}
	if !info.GetSetLowercaseProxyEnv() {
		t.Fatalf("expected lowercase proxy env to be enabled")
	}
	if got := info.GetEnvValues()["GH_TOKEN"]; got != "__Q15_PROXY_ENV_test__" {
		t.Fatalf("unexpected GH_TOKEN placeholder: %q", got)
	}
	if got := info.GetPolicyRevision(); got != "rev-123" {
		t.Fatalf("unexpected policy revision: %q", got)
	}
}

func TestGRPCServerClonesRuntimeValues(t *testing.T) {
	t.Parallel()

	values := map[string]string{"GH_TOKEN": "__Q15_PROXY_ENV_test__"}
	api, err := NewGRPCServer(RuntimeInfo{
		ServiceVersion:    "test",
		AdvertiseProxyURL: "http://proxy:18080",
		EnvValues:         values,
	})
	if err != nil {
		t.Fatalf("NewGRPCServer() error = %v", err)
	}

	values["GH_TOKEN"] = "mutated"

	resp, err := api.GetRuntimeInfo(context.Background(), &proxypb.GetRuntimeInfoRequest{})
	if err != nil {
		t.Fatalf("GetRuntimeInfo() error = %v", err)
	}
	if got := resp.GetEnvValues()["GH_TOKEN"]; got != "__Q15_PROXY_ENV_test__" {
		t.Fatalf("expected cloned env map contents in response, got %q", got)
	}
}

func TestGRPCServerRejectsMissingAdvertiseProxyURL(t *testing.T) {
	t.Parallel()

	if _, err := NewGRPCServer(RuntimeInfo{}); err == nil {
		t.Fatalf("expected error")
	}
}
