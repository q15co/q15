package sandboxbuildah

import (
	"path/filepath"
	"strings"
	"testing"
)

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
			Env: map[string]string{
				"GH_TOKEN": "__Q15_PROXY_ENV_test__",
			},
		},
	}

	env := proxyRunEnv(cfg)
	assertContainsKV(t, env, "GH_TOKEN", "__Q15_PROXY_ENV_test__")
	assertContainsKV(t, env, "HTTP_PROXY", "http://10.0.2.2:8080")
	assertContainsKV(t, env, "HTTPS_PROXY", "http://10.0.2.2:8080")
	assertContainsKV(t, env, "ALL_PROXY", "http://10.0.2.2:8080")
	assertContainsKV(t, env, "NO_PROXY", "localhost,127.0.0.1")
	assertContainsKV(t, env, "http_proxy", "http://10.0.2.2:8080")
	assertContainsKV(t, env, "https_proxy", "http://10.0.2.2:8080")
	assertContainsKV(t, env, "all_proxy", "http://10.0.2.2:8080")
	assertContainsKV(t, env, "no_proxy", "localhost,127.0.0.1")
	assertContainsKV(t, env, "SSL_CERT_FILE", "/run/q15-proxy/ca.crt")
	assertContainsKV(t, env, "NIX_SSL_CERT_FILE", "/run/q15-proxy/ca.crt")
	assertContainsKV(t, env, "CURL_CA_BUNDLE", "/run/q15-proxy/ca.crt")
	assertContainsKV(t, env, "GIT_SSL_CAINFO", "/run/q15-proxy/ca.crt")
}

func TestRunEnvPrependsNixPaths(t *testing.T) {
	env := runEnv(Settings{})
	assertContainsKV(
		t,
		env,
		"PATH",
		"/root/.nix-profile/bin:/nix/var/nix/profiles/default/bin:/nix/var/nix/profiles/per-user/root/profile/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	)
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

func TestWrapCommandWithProxyCABundle_NoProxyReturnsOriginal(t *testing.T) {
	command := "echo hi"
	got := wrapCommandWithProxyCABundle(Settings{}, command)
	if got != command {
		t.Fatalf("wrapped command = %q, want %q", got, command)
	}
}

func TestWrapCommandWithProxyCABundle_AppendsBundlePrefix(t *testing.T) {
	command := "nix --version"
	cfg := Settings{
		Proxy: &ProxySettings{
			Enabled:             true,
			CACertContainerPath: "/run/q15-proxy/ca.crt",
		},
	}

	got := wrapCommandWithProxyCABundle(cfg, command)
	assertContainsSnippet(t, got, "proxy CA cert is not readable")
	assertContainsSnippet(
		t,
		got,
		"cat /etc/ssl/certs/ca-certificates.crt '/run/q15-proxy/ca.crt' > \"$q15_ca_bundle\"",
	)
	assertContainsSnippet(t, got, "export NIX_SSL_CERT_FILE=\"$q15_ca_bundle\"")
	assertContainsSnippet(t, got, "export CURL_CA_BUNDLE=\"$q15_ca_bundle\"")
	assertContainsSnippet(t, got, command)
}

func assertContainsSnippet(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Fatalf("expected %q in %q", needle, haystack)
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
