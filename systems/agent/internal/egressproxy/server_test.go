package egressproxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
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
