package sandbox

import (
	"context"
	"reflect"
	"testing"
)

func TestHelperCommandInvokesHelperDirectly(t *testing.T) {
	t.Parallel()

	cmd := helperCommand(context.Background(), "/tmp/q15-sandbox-helper", "prepare")

	if got, want := cmd.Path, "/tmp/q15-sandbox-helper"; got != want {
		t.Fatalf("cmd.Path = %q, want %q", got, want)
	}
	if got, want := cmd.Args, []string{"/tmp/q15-sandbox-helper", "prepare"}; !reflect.DeepEqual(
		got,
		want,
	) {
		t.Fatalf("cmd.Args = %v, want %v", got, want)
	}
}

func TestSettingsValidateRequiresAbsolutePaths(t *testing.T) {
	cfg := Settings{
		ContainerName:    "q15-test",
		WorkspaceHostDir: "/tmp/q15-test",
		WorkspaceDir:     "/workspace",
		MemoryHostDir:    "/tmp/q15-test/.q15-memory",
		MemoryDir:        "/memory",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}

	cfg.WorkspaceHostDir = "relative"
	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected validation error for relative workspace host dir")
	}
}

func TestToContractSettingsMapsCoreFields(t *testing.T) {
	cfg := Settings{
		ContainerName:    "q15-test",
		WorkspaceHostDir: "/tmp/q15-test",
		WorkspaceDir:     "/workspace",
		MemoryHostDir:    "/tmp/q15-test/.q15-memory",
		MemoryDir:        "/memory",
		Proxy: &ProxySettings{
			Enabled:      true,
			HTTPProxyURL: "http://127.0.0.1:18080",
		},
	}

	got := toContractSettings(cfg)
	if got.ContainerName != cfg.ContainerName {
		t.Fatalf("unexpected container name in contract settings: %q", got.ContainerName)
	}
	if got.Proxy == nil {
		t.Fatalf("expected proxy settings in contract payload")
	}
	if got.Proxy.HTTPProxyURL != cfg.Proxy.HTTPProxyURL {
		t.Fatalf("unexpected proxy URL in contract settings: %q", got.Proxy.HTTPProxyURL)
	}
}
