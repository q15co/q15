package app

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/q15co/q15/libs/proxy-contract/proxypb"
	"google.golang.org/grpc"
)

type testProxyInfoServer struct {
	proxypb.UnimplementedProxyServiceServer
	info *proxypb.GetRuntimeInfoResponse
}

func (s testProxyInfoServer) GetRuntimeInfo(
	context.Context,
	*proxypb.GetRuntimeInfoRequest,
) (*proxypb.GetRuntimeInfoResponse, error) {
	return s.info, nil
}

func TestBootstrapProxyRuntimeSucceeds(t *testing.T) {
	systemBundlePath := filepath.Join(t.TempDir(), "system-ca.crt")
	systemBundle := "-----BEGIN CERTIFICATE-----\nSYSTEM\n-----END CERTIFICATE-----\n"
	if err := os.WriteFile(systemBundlePath, []byte(systemBundle), 0o600); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", systemBundlePath, err)
	}
	t.Setenv("SSL_CERT_FILE", systemBundlePath)
	t.Setenv("NIX_SSL_CERT_FILE", "")

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	server := grpc.NewServer()
	proxypb.RegisterProxyServiceServer(server, testProxyInfoServer{
		info: &proxypb.GetRuntimeInfoResponse{
			ServiceVersion:       "test",
			AdvertiseProxyUrl:    "http://proxy:8080",
			NoProxy:              "localhost,127.0.0.1",
			SetLowercaseProxyEnv: true,
			CaCertPem: []byte(
				"-----BEGIN CERTIFICATE-----\nTEST\n-----END CERTIFICATE-----\n",
			),
			EnvValues:      map[string]string{"GH_TOKEN": "__Q15_PROXY_ENV_test__"},
			PolicyRevision: "rev-123",
		},
	})
	go func() {
		_ = server.Serve(listener)
	}()
	defer server.Stop()
	defer listener.Close()

	profile, cleanup, err := bootstrapProxyRuntime(context.Background(), listener.Addr().String())
	if err != nil {
		t.Fatalf("bootstrapProxyRuntime() error = %v", err)
	}
	defer cleanup()

	if !profile.Enabled {
		t.Fatalf("expected proxy profile to be enabled")
	}
	if profile.PolicyRevision != "rev-123" {
		t.Fatalf("unexpected policy revision: %q", profile.PolicyRevision)
	}
	assertContainsEnv(t, profile.Env, "GH_TOKEN", "__Q15_PROXY_ENV_test__")
	assertContainsEnv(t, profile.Env, "HTTP_PROXY", "http://proxy:8080")
	assertContainsEnv(t, profile.Env, "http_proxy", "http://proxy:8080")
	assertContainsEnv(t, profile.Env, "NO_PROXY", "localhost,127.0.0.1")

	caPath := envValue(profile.Env, "SSL_CERT_FILE")
	if caPath == "" {
		t.Fatalf("expected SSL_CERT_FILE to be populated")
	}
	if filepath.Base(caPath) != "ca.crt" {
		t.Fatalf("unexpected CA path: %q", caPath)
	}
	if _, err := os.Stat(caPath); err != nil {
		t.Fatalf("expected CA file to exist: %v", err)
	}
	content, err := os.ReadFile(caPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", caPath, err)
	}
	if got := string(content); !strings.Contains(got, systemBundle) {
		t.Fatalf("expected combined CA bundle to include system roots: %q", got)
	}
	if got := string(content); !strings.Contains(
		got,
		"-----BEGIN CERTIFICATE-----\nTEST\n-----END CERTIFICATE-----\n",
	) {
		t.Fatalf("expected combined CA bundle to include proxy CA: %q", got)
	}

	cleanup()
	if _, err := os.Stat(caPath); !os.IsNotExist(err) {
		t.Fatalf("expected CA cleanup to remove file, got err=%v", err)
	}
}

func TestWriteProxyCABundleFallsBackWithoutSystemBundle(t *testing.T) {
	originalPaths := append([]string(nil), systemCABundlePaths...)
	systemCABundlePaths = nil
	defer func() {
		systemCABundlePaths = originalPaths
	}()

	t.Setenv("SSL_CERT_FILE", filepath.Join(t.TempDir(), "missing.crt"))
	t.Setenv("NIX_SSL_CERT_FILE", "")

	path, cleanup, err := writeProxyCABundle(
		[]byte("-----BEGIN CERTIFICATE-----\nPROXY\n-----END CERTIFICATE-----\n"),
	)
	if err != nil {
		t.Fatalf("writeProxyCABundle() error = %v", err)
	}
	defer cleanup()

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}
	if got := string(content); got != "-----BEGIN CERTIFICATE-----\nPROXY\n-----END CERTIFICATE-----\n" {
		t.Fatalf("bundle = %q", got)
	}
}

func TestBootstrapProxyRuntimeFailsWhenUnavailable(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	addr := listener.Addr().String()
	listener.Close()

	_, cleanup, err := bootstrapProxyRuntime(context.Background(), addr)
	cleanup()
	if err == nil {
		t.Fatalf("expected bootstrapProxyRuntime() error")
	}
}

func assertContainsEnv(t *testing.T, env []string, key string, want string) {
	t.Helper()
	if got := envValue(env, key); got != want {
		t.Fatalf("%s = %q, want %q", key, got, want)
	}
}

func envValue(env []string, key string) string {
	prefix := key + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}
	return ""
}
