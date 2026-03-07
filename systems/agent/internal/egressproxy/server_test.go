package egressproxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestStart_ProxiesHTTPRequests(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ping" {
			t.Fatalf("unexpected path: %q", r.URL.Path)
		}
		_, _ = io.WriteString(w, "pong")
	}))
	defer upstream.Close()

	proxy, err := Start(ctx, Config{})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	caPath := proxy.CACertHostPath()
	if caPath == "" {
		t.Fatalf("expected CA cert host path to be populated")
	}
	caBytes, err := os.ReadFile(caPath)
	if err != nil {
		t.Fatalf("os.ReadFile(CA cert) error = %v", err)
	}
	if !strings.Contains(string(caBytes), "BEGIN CERTIFICATE") {
		t.Fatalf("CA cert file does not look like PEM certificate: %q", string(caBytes))
	}

	proxyURL, err := proxy.ProxyURLForContainerHost("127.0.0.1")
	if err != nil {
		t.Fatalf("ProxyURLForContainerHost() error = %v", err)
	}
	parsedProxyURL, err := url.Parse(proxyURL)
	if err != nil {
		t.Fatalf("url.Parse(proxyURL) error = %v", err)
	}

	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			Proxy: http.ProxyURL(parsedProxyURL),
		},
	}

	resp, err := client.Get(upstream.URL + "/ping")
	if err != nil {
		t.Fatalf("client.Get() through proxy error = %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("io.ReadAll() error = %v", err)
	}
	if got := strings.TrimSpace(string(body)); got != "pong" {
		t.Fatalf("unexpected body: %q", got)
	}
}

func TestStart_ExportsAndCleansUpCACert(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	proxy, err := Start(ctx, Config{})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	caPath := proxy.CACertHostPath()
	if caPath == "" {
		t.Fatalf("expected CA cert host path")
	}
	if _, err := os.Stat(caPath); err != nil {
		t.Fatalf("os.Stat(CA cert before stop) error = %v", err)
	}

	cancel()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(caPath); err != nil {
			if os.IsNotExist(err) {
				return
			}
			t.Fatalf("os.Stat(CA cert after stop) unexpected error = %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected CA cert file to be removed after proxy stop: %s", caPath)
}

func TestProxyURLForContainerHostRejectsEmptyHost(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	proxy, err := Start(ctx, Config{})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	if _, err := proxy.ProxyURLForContainerHost(""); err == nil {
		t.Fatalf("expected error for empty container host")
	}
}

func TestStart_AppliesSetHeaderAndStripsSensitiveResponseHeaders(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var sawAuthHeader atomic.Value
	sawAuthHeader.Store("")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuthHeader.Store(r.Header.Get("Authorization"))
		w.Header().Set("Authorization", "upstream-secret")
		_, _ = io.WriteString(w, "ok")
	}))
	defer upstream.Close()

	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("url.Parse(upstream.URL) error = %v", err)
	}

	proxy, err := Start(ctx, Config{
		SecretValues: map[string]string{"gh_token": "test-token"},
		Rules: []Rule{
			{
				Name:              "inject-auth",
				MatchHosts:        []string{upstreamURL.Host},
				MatchPathPrefixes: []string{"/"},
				SetHeader: map[string]string{
					"Authorization": "token {{secret.gh_token}}",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	proxyURL, err := proxy.ProxyURLForContainerHost("127.0.0.1")
	if err != nil {
		t.Fatalf("ProxyURLForContainerHost() error = %v", err)
	}
	parsedProxyURL, err := url.Parse(proxyURL)
	if err != nil {
		t.Fatalf("url.Parse(proxyURL) error = %v", err)
	}

	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			Proxy: http.ProxyURL(parsedProxyURL),
		},
	}

	resp, err := client.Get(upstream.URL + "/any")
	if err != nil {
		t.Fatalf("client.Get() error = %v", err)
	}
	defer resp.Body.Close()
	if got := sawAuthHeader.Load().(string); got != "token test-token" {
		t.Fatalf("upstream Authorization header = %q, want %q", got, "token test-token")
	}
	if got := resp.Header.Get("Authorization"); got != "" {
		t.Fatalf("expected response Authorization header to be stripped, got %q", got)
	}
}

func TestStart_ReplacesConfiguredPlaceholdersInHeaderQueryAndPath(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var sawAuthHeader atomic.Value
	var sawPath atomic.Value
	var sawQueryToken atomic.Value
	sawAuthHeader.Store("")
	sawPath.Store("")
	sawQueryToken.Store("")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuthHeader.Store(r.Header.Get("Authorization"))
		sawPath.Store(r.URL.Path)
		sawQueryToken.Store(r.URL.Query().Get("access_token"))
		_, _ = io.WriteString(w, "ok")
	}))
	defer upstream.Close()

	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("url.Parse(upstream.URL) error = %v", err)
	}

	proxy, err := Start(ctx, Config{
		SecretValues: map[string]string{"gh_token": "test-token"},
		Rules: []Rule{
			{
				Name:              "replace-gh-placeholder",
				MatchHosts:        []string{upstreamURL.Host},
				MatchPathPrefixes: []string{"/"},
				ReplacePlaceholder: []PlaceholderReplacement{
					{
						Placeholder: "__Q15_PROXY_ENV_test__",
						Secret:      "gh_token",
						In:          []string{"header", "query", "path"},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	client := newProxyClient(t, proxy)

	req, err := http.NewRequest(
		http.MethodGet,
		upstream.URL+"/user/__Q15_PROXY_ENV_test__/repos?access_token=__Q15_PROXY_ENV_test__",
		nil,
	)
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Authorization", "Bearer __Q15_PROXY_ENV_test__")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do() error = %v", err)
	}
	resp.Body.Close()

	if got := sawAuthHeader.Load().(string); got != "Bearer test-token" {
		t.Fatalf("upstream Authorization header = %q, want %q", got, "Bearer test-token")
	}
	if got := sawPath.Load().(string); got != "/user/test-token/repos" {
		t.Fatalf("upstream path = %q, want %q", got, "/user/test-token/repos")
	}
	if got := sawQueryToken.Load().(string); got != "test-token" {
		t.Fatalf("upstream query token = %q, want %q", got, "test-token")
	}
}

func TestStart_DoesNotReplacePlaceholdersForUnmatchedRules(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var matchedAuth atomic.Value
	var unmatchedAuth atomic.Value
	matchedAuth.Store("")
	unmatchedAuth.Store("")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/match") {
			matchedAuth.Store(r.Header.Get("Authorization"))
		} else {
			unmatchedAuth.Store(r.Header.Get("Authorization"))
		}
		_, _ = io.WriteString(w, "ok")
	}))
	defer upstream.Close()

	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("url.Parse(upstream.URL) error = %v", err)
	}

	proxy, err := Start(ctx, Config{
		SecretValues: map[string]string{"gh_token": "test-token"},
		Rules: []Rule{
			{
				Name:              "replace-gh-placeholder",
				MatchHosts:        []string{upstreamURL.Host},
				MatchPathPrefixes: []string{"/match"},
				ReplacePlaceholder: []PlaceholderReplacement{
					{
						Placeholder: "__Q15_PROXY_ENV_test__",
						Secret:      "gh_token",
						In:          []string{"header"},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	client := newProxyClient(t, proxy)

	req, err := http.NewRequest(http.MethodGet, upstream.URL+"/match/ok", nil)
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Authorization", "Bearer __Q15_PROXY_ENV_test__")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do(matched) error = %v", err)
	}
	resp.Body.Close()

	req, err = http.NewRequest(http.MethodGet, upstream.URL+"/other/ok", nil)
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Authorization", "Bearer __Q15_PROXY_ENV_test__")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("client.Do(unmatched) error = %v", err)
	}
	resp.Body.Close()

	if got := matchedAuth.Load().(string); got != "Bearer test-token" {
		t.Fatalf("matched upstream Authorization header = %q, want %q", got, "Bearer test-token")
	}
	if got := unmatchedAuth.Load().(string); got != "Bearer __Q15_PROXY_ENV_test__" {
		t.Fatalf(
			"unmatched upstream Authorization header = %q, want %q",
			got,
			"Bearer __Q15_PROXY_ENV_test__",
		)
	}
}

func TestStart_KeepsProxyReplacementStateIsolatedPerServer(t *testing.T) {
	ctxA, cancelA := context.WithCancel(context.Background())
	defer cancelA()
	ctxB, cancelB := context.WithCancel(context.Background())
	defer cancelB()

	var sawAuth atomic.Value
	sawAuth.Store("")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth.Store(r.Header.Get("Authorization"))
		_, _ = io.WriteString(w, "ok")
	}))
	defer upstream.Close()

	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("url.Parse(upstream.URL) error = %v", err)
	}

	newRule := func(placeholder string) []Rule {
		return []Rule{
			{
				Name:              "replace-gh-placeholder",
				MatchHosts:        []string{upstreamURL.Host},
				MatchPathPrefixes: []string{"/"},
				ReplacePlaceholder: []PlaceholderReplacement{
					{
						Placeholder: placeholder,
						Secret:      "gh_token",
						In:          []string{"header"},
					},
				},
			},
		}
	}

	proxyA, err := Start(ctxA, Config{
		SecretValues: map[string]string{"gh_token": "token-a"},
		Rules:        newRule("__Q15_PROXY_ENV_agent_a__"),
	})
	if err != nil {
		t.Fatalf("Start(proxyA) error = %v", err)
	}
	proxyB, err := Start(ctxB, Config{
		SecretValues: map[string]string{"gh_token": "token-b"},
		Rules:        newRule("__Q15_PROXY_ENV_agent_b__"),
	})
	if err != nil {
		t.Fatalf("Start(proxyB) error = %v", err)
	}

	clientA := newProxyClient(t, proxyA)
	clientB := newProxyClient(t, proxyB)

	req, err := http.NewRequest(http.MethodGet, upstream.URL+"/repos", nil)
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Authorization", "Bearer __Q15_PROXY_ENV_agent_a__")
	resp, err := clientA.Do(req)
	if err != nil {
		t.Fatalf("clientA.Do() error = %v", err)
	}
	resp.Body.Close()
	if got := sawAuth.Load().(string); got != "Bearer token-a" {
		t.Fatalf("proxyA Authorization header = %q, want %q", got, "Bearer token-a")
	}

	req, err = http.NewRequest(http.MethodGet, upstream.URL+"/repos", nil)
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Authorization", "Bearer __Q15_PROXY_ENV_agent_b__")
	resp, err = clientB.Do(req)
	if err != nil {
		t.Fatalf("clientB.Do() error = %v", err)
	}
	resp.Body.Close()
	if got := sawAuth.Load().(string); got != "Bearer token-b" {
		t.Fatalf("proxyB Authorization header = %q, want %q", got, "Bearer token-b")
	}

	req, err = http.NewRequest(http.MethodGet, upstream.URL+"/repos", nil)
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Authorization", "Bearer __Q15_PROXY_ENV_agent_b__")
	resp, err = clientA.Do(req)
	if err != nil {
		t.Fatalf("clientA.Do(unmatched placeholder) error = %v", err)
	}
	resp.Body.Close()
	if got := sawAuth.Load().(string); got != "Bearer __Q15_PROXY_ENV_agent_b__" {
		t.Fatalf(
			"proxyA should not rewrite proxyB placeholder, got %q",
			got,
		)
	}
}

func TestServer_ShouldMITMHostForMatchedRule(t *testing.T) {
	s := &Server{
		compiledRules: []compiledRule{
			{
				matchHosts: map[string]struct{}{
					"api.github.com": {},
				},
			},
		},
	}
	if !s.shouldMITMHost("api.github.com:443") {
		t.Fatalf("expected matched host to require MITM")
	}
	if s.shouldMITMHost("example.com:443") {
		t.Fatalf("expected unmatched host to stay passthrough")
	}
}

func newProxyClient(t *testing.T, proxy *Server) *http.Client {
	t.Helper()

	proxyURL, err := proxy.ProxyURLForContainerHost("127.0.0.1")
	if err != nil {
		t.Fatalf("ProxyURLForContainerHost() error = %v", err)
	}
	parsedProxyURL, err := url.Parse(proxyURL)
	if err != nil {
		t.Fatalf("url.Parse(proxyURL) error = %v", err)
	}

	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			Proxy: http.ProxyURL(parsedProxyURL),
		},
	}
}
