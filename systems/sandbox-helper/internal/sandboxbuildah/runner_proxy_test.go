package sandboxbuildah

import (
	"path/filepath"
	"testing"
)

func TestSettingsValidateRejectsEnabledProxyWithNetworkDisabled(t *testing.T) {
	cfg := Settings{
		ContainerName:    "q15-test",
		FromImage:        "docker.io/library/debian:bookworm-slim",
		WorkspaceHostDir: "/tmp/q15-test",
		WorkspaceDir:     "/workspace",
		MemoryHostDir:    "/tmp/q15-test/.q15-memory",
		MemoryDir:        "/memory",
		Network:          "disabled",
		Proxy: &ProxySettings{
			Enabled:      true,
			HTTPProxyURL: "http://10.0.2.2:8080",
		},
	}

	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected validation error")
	}
}

func TestProxyRunEnvIncludesProxyAndTrustVars(t *testing.T) {
	cfg := Settings{
		Proxy: &ProxySettings{
			Enabled:              true,
			HTTPProxyURL:         "http://10.0.2.2:8080",
			HTTPSProxyURL:        "http://10.0.2.2:8080",
			AllProxyURL:          "http://10.0.2.2:8080",
			NoProxy:              "localhost,127.0.0.1",
			CACertContainerPath:  "/run/q15-proxy/ca.crt",
			SetLowercaseProxyEnv: true,
		},
	}

	env := proxyRunEnv(cfg)
	assertContainsKV(t, env, "HTTP_PROXY", "http://10.0.2.2:8080")
	assertContainsKV(t, env, "HTTPS_PROXY", "http://10.0.2.2:8080")
	assertContainsKV(t, env, "ALL_PROXY", "http://10.0.2.2:8080")
	assertContainsKV(t, env, "NO_PROXY", "localhost,127.0.0.1")
	assertContainsKV(t, env, "http_proxy", "http://10.0.2.2:8080")
	assertContainsKV(t, env, "https_proxy", "http://10.0.2.2:8080")
	assertContainsKV(t, env, "all_proxy", "http://10.0.2.2:8080")
	assertContainsKV(t, env, "no_proxy", "localhost,127.0.0.1")
	assertContainsKV(t, env, "SSL_CERT_FILE", "/run/q15-proxy/ca.crt")
	assertContainsKV(t, env, "CURL_CA_BUNDLE", "/run/q15-proxy/ca.crt")
	assertContainsKV(t, env, "GIT_SSL_CAINFO", "/run/q15-proxy/ca.crt")
}

func TestProxyExtraMountsAddsReadOnlyCAMount(t *testing.T) {
	hostPath := filepath.Join(t.TempDir(), "ca.crt")
	cfg := Settings{
		Proxy: &ProxySettings{
			Enabled:             true,
			CACertHostPath:      hostPath,
			CACertContainerPath: "/run/q15-proxy/ca.crt",
		},
	}

	mounts := proxyExtraMounts(cfg)
	if len(mounts) != 1 {
		t.Fatalf("expected 1 mount, got %#v", mounts)
	}
	m := mounts[0]
	if m.Source != hostPath {
		t.Fatalf("unexpected source mount path: %q", m.Source)
	}
	if m.Destination != "/run/q15-proxy/ca.crt" {
		t.Fatalf("unexpected destination mount path: %q", m.Destination)
	}
	if len(m.Options) == 0 {
		t.Fatalf("expected mount options")
	}
	foundRO := false
	for _, opt := range m.Options {
		if opt == "ro" {
			foundRO = true
			break
		}
	}
	if !foundRO {
		t.Fatalf("expected read-only CA mount, options=%#v", m.Options)
	}
}

func assertContainsKV(t *testing.T, env []string, key, wantValue string) {
	t.Helper()
	want := key + "=" + wantValue
	for _, got := range env {
		if got == want {
			return
		}
	}
	t.Fatalf("missing env %q in %#v", want, env)
}
