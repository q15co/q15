package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNewMultiWebSearchRequiresAnyProvider(t *testing.T) {
	t.Parallel()

	if tool, err := NewMultiWebSearch("", ""); err != nil || tool != nil {
		t.Fatalf("expected nil tool with no keys, got %v, %v", tool, err)
	}

	tool, err := NewMultiWebSearch("brave-key", "")
	if err != nil {
		t.Fatalf("brave-only: unexpected error: %v", err)
	}
	if got := tool.Providers(); len(got) != 1 || got[0] != "brave" {
		t.Fatalf("brave-only providers = %v", got)
	}

	tool, err = NewMultiWebSearch("", "ollama-key")
	if err != nil {
		t.Fatalf("ollama-only: unexpected error: %v", err)
	}
	if got := tool.Providers(); len(got) != 1 || got[0] != "ollama" {
		t.Fatalf("ollama-only providers = %v", got)
	}
}

func TestOllamaWebSearchRequestAndResponse(t *testing.T) {
	t.Parallel()

	var (
		mu       sync.Mutex
		method   string
		auth     string
		received map[string]any
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var decoded map[string]any
		_ = json.Unmarshal(body, &decoded)

		mu.Lock()
		method = r.Method
		auth = r.Header.Get("Authorization")
		received = decoded
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"results":[
			{"title":"First result","url":"https://example.com/one","content":"snippet one"},
			{"title":"  ","url":"https://example.com/two","content":""}
		]}`)
	}))
	defer srv.Close()

	provider, err := NewOllamaWebSearch("secret-key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	provider.baseURL = srv.URL

	out, err := provider.Search(context.Background(), "golang testing", 5)
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if method != http.MethodPost {
		t.Errorf("method = %q, want POST", method)
	}
	if auth != "Bearer secret-key" {
		t.Errorf("authorization = %q", auth)
	}
	if got, _ := received["query"].(string); got != "golang testing" {
		t.Errorf("query = %v", received["query"])
	}
	if got, _ := received["max_results"].(float64); int(got) != 5 {
		t.Errorf("max_results = %v, want 5", received["max_results"])
	}

	if !strings.Contains(out, "Results for: golang testing") {
		t.Errorf("missing header in output:\n%s", out)
	}
	if !strings.Contains(out, "https://example.com/one") || !strings.Contains(out, "snippet one") {
		t.Errorf("missing first result in output:\n%s", out)
	}
	if !strings.Contains(out, "(untitled)") {
		t.Errorf("expected placeholder title for blank title:\n%s", out)
	}
}

func TestOllamaWebSearchErrorStatus(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, "slow down")
	}))
	defer srv.Close()

	provider, err := NewOllamaWebSearch("key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	provider.baseURL = srv.URL

	if _, err := provider.Search(context.Background(), "query", 3); err == nil {
		t.Fatal("expected error on 429 response")
	} else if !strings.Contains(err.Error(), "429") {
		t.Fatalf("error should mention status: %v", err)
	}
}

// recordingTool builds a MultiWebSearch whose providers are driven by injected
// functions, so rotation and failover can be observed without network access.
func recordingTool(providers ...*provider) *MultiWebSearch {
	tool := &MultiWebSearch{maxRes: defaultWebSearchCount}
	tool.providers = make([]provider, len(providers))
	for i, p := range providers {
		tool.providers[i].name = p.name
		tool.providers[i].min = p.min
		tool.providers[i].run = p.run
	}
	return tool
}

func TestMultiWebSearchRoundRobinsStartProvider(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var starts []string

	makeProvider := func(name string) *provider {
		return &provider{
			name: name,
			min:  0,
			run: func(_ context.Context, query string, _ int) (string, error) {
				mu.Lock()
				starts = append(starts, name)
				mu.Unlock()
				return "from " + name, nil
			},
		}
	}

	tool := recordingTool(makeProvider("a"), makeProvider("b"))
	for i := 0; i < 3; i++ {
		if _, err := tool.Run(context.Background(), `{"query":"rotation"}`); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}

	want := []string{"a", "b", "a"}
	if len(starts) != len(want) {
		t.Fatalf("starts = %v, want %v", starts, want)
	}
	for i := range want {
		if starts[i] != want[i] {
			t.Fatalf("starts = %v, want %v", starts, want)
		}
	}
}

func TestMultiWebSearchFallsBackOnProviderError(t *testing.T) {
	t.Parallel()

	failing := &provider{
		name: "failing",
		min:  0,
		run: func(context.Context, string, int) (string, error) {
			return "", context.DeadlineExceeded
		},
	}
	healthy := &provider{
		name: "healthy",
		min:  0,
		run: func(context.Context, string, int) (string, error) {
			return "results from healthy", nil
		},
	}

	tool := recordingTool(failing, healthy)
	out, err := tool.Run(context.Background(), `{"query":"failover"}`)
	if err != nil {
		t.Fatalf("expected failover to succeed: %v", err)
	}
	if out != "results from healthy" {
		t.Fatalf("out = %q", out)
	}
}

func TestMultiWebSearchAllProvidersFail(t *testing.T) {
	t.Parallel()

	tool := recordingTool(
		&provider{name: "one", min: 0, run: func(context.Context, string, int) (string, error) {
			return "", context.DeadlineExceeded
		}},
		&provider{name: "two", min: 0, run: func(context.Context, string, int) (string, error) {
			return "", context.Canceled
		}},
	)

	_, err := tool.Run(context.Background(), `{"query":"all fail"}`)
	if err == nil {
		t.Fatal("expected an error when every provider fails")
	}
	msg := err.Error()
	if !strings.Contains(msg, "one") || !strings.Contains(msg, "two") {
		t.Fatalf("error should name every provider: %v", err)
	}
}

func TestMultiWebSearchThrottlesProviderSpacing(t *testing.T) {
	t.Parallel()

	const min = 60 * time.Millisecond
	var mu sync.Mutex
	var stamps []time.Time

	throttled := &provider{
		name: "throttled",
		min:  min,
		run: func(context.Context, string, int) (string, error) {
			mu.Lock()
			stamps = append(stamps, time.Now())
			mu.Unlock()
			return "ok", nil
		},
	}

	tool := recordingTool(throttled)
	for i := 0; i < 2; i++ {
		if _, err := tool.Run(context.Background(), `{"query":"spacing"}`); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(stamps) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(stamps))
	}
	if gap := stamps[1].Sub(stamps[0]); gap < min {
		t.Fatalf("provider calls not throttled: gap %s < %s", gap, min)
	}
}

func TestMultiWebSearchNilToolIsSafe(t *testing.T) {
	t.Parallel()

	var tool *MultiWebSearch
	if _, err := tool.Run(context.Background(), `{"query":"x"}`); err == nil {
		t.Fatal("expected error from nil tool")
	}
	if got := tool.Providers(); got != nil {
		t.Fatalf("Providers on nil tool = %v", got)
	}
}

func TestMultiWebSearchPrefersIdleProviderOverThrottledRotation(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var ran []string

	throttled := &provider{
		name: "throttled",
		min:  time.Hour,
		run: func(context.Context, string, int) (string, error) {
			mu.Lock()
			ran = append(ran, "throttled")
			mu.Unlock()
			return "from throttled", nil
		},
	}
	idle := &provider{
		name: "idle",
		min:  0,
		run: func(context.Context, string, int) (string, error) {
			mu.Lock()
			ran = append(ran, "idle")
			mu.Unlock()
			return "from idle", nil
		},
	}

	tool := recordingTool(throttled, idle)
	// Book the throttled provider's slot so the rotation lands on a provider
	// that cannot run for an hour.
	if err := tool.providers[0].wait(context.Background()); err != nil {
		t.Fatalf("priming throttle: %v", err)
	}

	out, err := tool.Run(context.Background(), `{"query":"idle"}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if out != "from idle" {
		t.Fatalf("out = %q, want the idle provider's result", out)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(ran) != 1 || ran[0] != "idle" {
		t.Fatalf("providers run = %v, want only idle", ran)
	}
}

func TestMultiWebSearchCoolsDownAFailingProvider(t *testing.T) {
	t.Parallel()

	calls := 0
	brittle := &provider{
		name: "brittle",
		run: func(context.Context, string, int) (string, error) {
			calls++
			return "", fmt.Errorf("backend down")
		},
	}
	tool := recordingTool(brittle)

	for i := 0; i < providerFailureThreshold; i++ {
		if _, err := tool.Run(context.Background(), `{"query":"x"}`); err == nil {
			t.Fatalf("run %d: want an error", i)
		}
	}
	if calls != providerFailureThreshold {
		t.Fatalf("calls = %d, want %d", calls, providerFailureThreshold)
	}

	if _, err := tool.Run(context.Background(), `{"query":"x"}`); err == nil {
		t.Fatal("cooled-down provider: want an error, got nil")
	}
	if calls != providerFailureThreshold {
		t.Fatalf("cooling provider was called: %d calls", calls)
	}

	// Expire the cooldown and confirm the provider is retried.
	tool.providers[0].mu.Lock()
	tool.providers[0].cooldownUntil = time.Now().Add(-time.Second)
	tool.providers[0].mu.Unlock()

	if _, err := tool.Run(context.Background(), `{"query":"x"}`); err == nil {
		t.Fatal("retried provider: want an error, got nil")
	}
	if calls != providerFailureThreshold+1 {
		t.Fatalf("calls = %d after cooldown, want %d", calls, providerFailureThreshold+1)
	}
}

func TestProviderWaitSerialisesConcurrentCallers(t *testing.T) {
	t.Parallel()

	spacing := 40 * time.Millisecond
	p := &provider{name: "shared", min: spacing}

	const callers = 3
	returned := make([]time.Time, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := p.wait(context.Background()); err != nil {
				t.Errorf("wait: %v", err)
			}
			returned[i] = time.Now()
		}(i)
	}
	wg.Wait()

	earliest, latest := returned[0], returned[0]
	for _, ts := range returned[1:] {
		if ts.Before(earliest) {
			earliest = ts
		}
		if ts.After(latest) {
			latest = ts
		}
	}
	want := time.Duration(callers-1) * spacing
	if got := latest.Sub(earliest); got < want-10*time.Millisecond {
		t.Fatalf("concurrent waiters spread over %v, want at least %v", got, want)
	}
}
