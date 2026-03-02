package app

import (
	"context"
	"os"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/config"
	"github.com/q15co/q15/systems/agent/internal/sandbox"
)

func TestBuildSandboxSettings_WithoutProxy(t *testing.T) {
	rt := config.AgentRuntime{
		Name:                 "agent-a",
		SandboxContainerName: "q15-agent-a",
		WorkspaceHostDir:     "/tmp/q15/agent-a",
		WorkspaceDir:         "/workspace",
		MemoryHostDir:        "/tmp/q15/agent-a/.q15-memory",
		MemoryDir:            "/memory",
	}

	got := buildSandboxSettings(rt, nil)
	if got.ContainerName != rt.SandboxContainerName {
		t.Fatalf("unexpected container name: %q", got.ContainerName)
	}
	if got.Proxy != nil {
		t.Fatalf("expected nil proxy settings, got %#v", got.Proxy)
	}
	if got.MemoryHostDir != rt.MemoryHostDir {
		t.Fatalf("unexpected memory host dir: %q", got.MemoryHostDir)
	}
	if got.MemoryDir != rt.MemoryDir {
		t.Fatalf("unexpected memory dir: %q", got.MemoryDir)
	}
}

func TestBuildSandboxSettings_WithProxySettings(t *testing.T) {
	rt := config.AgentRuntime{
		Name:                 "agent-a",
		SandboxContainerName: "q15-agent-a",
		WorkspaceHostDir:     "/tmp/q15/agent-a",
		WorkspaceDir:         "/workspace",
		MemoryHostDir:        "/tmp/q15/agent-a/.q15-memory",
		MemoryDir:            "/memory",
	}
	proxy := &sandbox.ProxySettings{
		Enabled:      true,
		HTTPProxyURL: "http://127.0.0.1:18080",
	}

	got := buildSandboxSettings(rt, proxy)
	if got.Proxy == nil {
		t.Fatalf("expected proxy settings to be attached")
	}
	if got.Proxy.HTTPProxyURL != "http://127.0.0.1:18080" {
		t.Fatalf("unexpected HTTP proxy URL: %q", got.Proxy.HTTPProxyURL)
	}
}

func TestStartSandboxProxy_BuildsSandboxProxySettings(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	handle, err := startSandboxProxy(ctx, &config.SandboxProxyRuntime{
		Enabled:              true,
		ListenAddr:           "127.0.0.1:0",
		ContainerProxyHost:   "10.0.2.2",
		CACertContainerPath:  "/run/q15-proxy/ca.crt",
		NoProxy:              []string{"localhost", "127.0.0.1"},
		SetLowercaseProxyEnv: true,
	})
	if err != nil {
		t.Fatalf("startSandboxProxy() error = %v", err)
	}
	if handle == nil || handle.sandboxSettings == nil {
		t.Fatalf("expected sandbox proxy handle/settings")
	}
	if handle.sandboxSettings.HTTPProxyURL == "" {
		t.Fatalf("expected HTTP proxy URL to be populated")
	}
	if handle.sandboxSettings.HTTPSProxyURL == "" || handle.sandboxSettings.AllProxyURL == "" {
		t.Fatalf("expected HTTPS/ALL proxy URLs to be populated: %#v", handle.sandboxSettings)
	}
	if handle.sandboxSettings.NoProxy != "localhost,127.0.0.1" {
		t.Fatalf("unexpected NO_PROXY: %q", handle.sandboxSettings.NoProxy)
	}
	if handle.sandboxSettings.CACertHostPath == "" {
		t.Fatalf("expected CA cert host path to be populated")
	}
	if handle.sandboxSettings.CACertContainerPath != "/run/q15-proxy/ca.crt" {
		t.Fatalf(
			"unexpected CA cert container path: %q",
			handle.sandboxSettings.CACertContainerPath,
		)
	}
	if _, err := os.Stat(handle.sandboxSettings.CACertHostPath); err != nil {
		t.Fatalf("expected CA cert host path to exist, stat error = %v", err)
	}
}

func TestStartSandboxProxy_RejectsReplacePlaceholderRules(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, err := startSandboxProxy(ctx, &config.SandboxProxyRuntime{
		Enabled:             true,
		ListenAddr:          "127.0.0.1:0",
		ContainerProxyHost:  "10.0.2.2",
		CACertContainerPath: "/run/q15-proxy/ca.crt",
		SecretValues:        map[string]string{"gh_token": "abc"},
		Rules: []config.SandboxProxyRule{
			{
				MatchHosts: []string{"api.github.com"},
				ReplacePlaceholder: []config.SandboxProxyPlaceholderReplacement{
					{Placeholder: "__X__", Secret: "gh_token", In: []string{"header"}},
				},
			},
		},
	})
	if err == nil {
		t.Fatalf("expected error for replace_placeholder rules")
	}
}
