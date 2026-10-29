package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func TestNewProvidersResolvesConfiguredProviders(t *testing.T) {
	t.Parallel()

	providers, err := NewProviders(SearchConfig{})
	if err != nil {
		t.Fatalf("no keys: unexpected error: %v", err)
	}
	if len(providers) != 0 {
		t.Fatalf("no keys: providers = %v, want none", providers)
	}

	providers, err = NewProviders(SearchConfig{BraveAPIKey: "brave-key"})
	if err != nil {
		t.Fatalf("brave-only: unexpected error: %v", err)
	}
	if len(providers) != 1 || providers[0].Name() != "brave" {
		t.Fatalf("brave-only providers = %v", providers)
	}

	providers, err = NewProviders(SearchConfig{OllamaAPIKey: "ollama-key"})
	if err != nil {
		t.Fatalf("ollama-only: unexpected error: %v", err)
	}
	if len(providers) != 1 || providers[0].Name() != "ollama" {
		t.Fatalf("ollama-only providers = %v", providers)
	}

	providers, err = NewProviders(SearchConfig{BraveAPIKey: "b", OllamaAPIKey: "o"})
	if err != nil {
		t.Fatalf("both: unexpected error: %v", err)
	}
	if len(providers) != 2 || providers[0].Name() != "brave" || providers[1].Name() != "ollama" {
		t.Fatalf("both providers = %v", providers)
	}
}

func TestNewSearchRequiresAProvider(t *testing.T) {
	t.Parallel()

	if tool, err := NewSearch(nil); err == nil || tool != nil {
		t.Fatalf("expected an error and no tool, got %v, %v", tool, err)
	}

	tool, err := NewSearch([]Provider{fakeProvider{name: "a"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := tool.Providers(); len(got) != 1 || got[0] != "a" {
		t.Fatalf("providers = %v", got)
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

	results, err := provider.Search(context.Background(), SearchRequest{Query: "golang testing", Count: 5})
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

	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}
	if results[0].URL != "https://example.com/one" || results[0].Snippet != "snippet one" {
		t.Errorf("first result = %+v", results[0])
	}
	// A blank provider title stays blank in the contract; the renderer is what
	// supplies the placeholder.
	if results[1].Title != "" {
		t.Errorf("blank title became %q, want empty", results[1].Title)
	}
	if out := renderResults("ollama", "rotation", 5, results, defaultSnippetChars); !strings.Contains(out, "(untitled)") {
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

	_, err = provider.Search(context.Background(), SearchRequest{Query: "query", Count: 3})
	if err == nil {
		t.Fatal("expected error on 429 response")
	} else if !strings.Contains(err.Error(), "429") {
		t.Fatalf("error should mention status: %v", err)
	}
}

// fakeResult is one result attributed to a provider, used to trace which
// backend answered without asserting on provider-specific content.
func fakeResult(name string) Result {
	return Result{
		Title:   "result from " + name,
		URL:     "https://example.com/" + name,
		Snippet: "result from " + name,
	}
}

// fakeProvider implements Provider with an injected search function so
// rotation, throttling, and failover can be observed without network access.
type fakeProvider struct {
	name string
	min  time.Duration
	run  func(ctx context.Context, req SearchRequest) ([]Result, error)
}

func (f fakeProvider) Name() string               { return f.name }
func (f fakeProvider) MinInterval() time.Duration { return f.min }

func (f fakeProvider) Search(ctx context.Context, req SearchRequest) ([]Result, error) {
	if f.run == nil {
		return []Result{fakeResult(f.name)}, nil
	}
	return f.run(ctx, req)
}

// recordingTool builds a Search over injected providers.
func recordingTool(t *testing.T, providers ...fakeProvider) *Search {
	t.Helper()
	list := make([]Provider, 0, len(providers))
	for _, p := range providers {
		list = append(list, p)
	}
	tool, err := NewSearch(list)
	if err != nil {
		t.Fatalf("NewSearch() error = %v", err)
	}
	return tool
}

func TestSearchRoundRobinsStartProvider(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var starts []string

	makeProvider := func(name string) fakeProvider {
		return fakeProvider{
			name: name,
			run: func(context.Context, SearchRequest) ([]Result, error) {
				mu.Lock()
				starts = append(starts, name)
				mu.Unlock()
				return []Result{fakeResult(name)}, nil
			},
		}
	}

	tool := recordingTool(t, makeProvider("a"), makeProvider("b"))
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

func TestSearchFallsBackOnProviderError(t *testing.T) {
	t.Parallel()

	failing := fakeProvider{
		name: "failing",
		run: func(context.Context, SearchRequest) ([]Result, error) {
			return nil, context.DeadlineExceeded
		},
	}
	healthy := fakeProvider{
		name: "healthy",
		run: func(context.Context, SearchRequest) ([]Result, error) {
			return []Result{fakeResult("healthy")}, nil
		},
	}

	tool := recordingTool(t, failing, healthy)
	out, err := tool.Run(context.Background(), `{"query":"failover"}`)
	if err != nil {
		t.Fatalf("expected failover to succeed: %v", err)
	}
	if !strings.Contains(out, "result from healthy") {
		t.Fatalf("out = %q, want the healthy provider's result", out)
	}
	if header := firstLine(out); header != "healthy (fallback after failing: failed) · 1 results" {
		t.Fatalf("header = %q, want the failed predecessor named", header)
	}
}

// firstLine returns the attribution header of a rendered response.
func firstLine(out string) string {
	if i := strings.IndexByte(out, '\n'); i >= 0 {
		return out[:i]
	}
	return out
}

func TestSearchAttributesRotation(t *testing.T) {
	t.Parallel()

	tool := recordingTool(t, fakeProvider{name: "a"}, fakeProvider{name: "b"})
	out, err := tool.Run(context.Background(), `{"query":"rotation"}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if header := firstLine(out); header != "a (rotation) · 1 results" {
		t.Fatalf("header = %q, want rotation", header)
	}
}

func TestSearchAttributesIdlePromotion(t *testing.T) {
	t.Parallel()

	tool := recordingTool(
		t,
		fakeProvider{name: "slow", min: time.Hour},
		fakeProvider{name: "idle"},
	)
	// Book the rotation target's slot so it cannot run for an hour, which is
	// what makes order() promote the idle provider.
	if err := tool.providers[0].wait(context.Background()); err != nil {
		t.Fatalf("priming throttle: %v", err)
	}

	out, err := tool.Run(context.Background(), `{"query":"idle"}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if header := firstLine(out); header != "idle (idle) · 1 results" {
		t.Fatalf("header = %q, want idle promotion", header)
	}
}

func TestSearchAttributesFallbackAfterThrottledProvider(t *testing.T) {
	t.Parallel()

	tool := recordingTool(
		t,
		fakeProvider{name: "cooling", min: time.Hour},
		fakeProvider{name: "spaced", min: 5 * time.Millisecond},
	)
	// The rotation target is in failure cooldown, and the only other provider
	// is inside its spacing window, so nothing can be promoted to the front.
	tool.providers[0].mu.Lock()
	tool.providers[0].cooldownUntil = time.Now().Add(time.Hour)
	tool.providers[0].mu.Unlock()
	if err := tool.providers[1].wait(context.Background()); err != nil {
		t.Fatalf("priming throttle: %v", err)
	}

	out, err := tool.Run(context.Background(), `{"query":"fallback"}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if header := firstLine(out); header != "spaced (fallback after cooling: throttled) · 1 results" {
		t.Fatalf("header = %q, want the throttled predecessor named", header)
	}
}

func TestSearchAllProvidersFail(t *testing.T) {
	t.Parallel()

	fail := func(context.Context, SearchRequest) ([]Result, error) {
		return nil, fmt.Errorf("down")
	}
	tool := recordingTool(
		t,
		fakeProvider{name: "a", run: fail},
		fakeProvider{name: "b", run: fail},
	)

	_, err := tool.Run(context.Background(), `{"query":"x"}`)
	if err == nil || !strings.Contains(err.Error(), "all web search providers failed") {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(err.Error(), "a:") || !strings.Contains(err.Error(), "b:") {
		t.Fatalf("error should name both providers: %v", err)
	}
}

func TestSearchThrottlesEachProvider(t *testing.T) {
	t.Parallel()

	spacing := 60 * time.Millisecond
	var mu sync.Mutex
	var stamps []time.Time

	throttled := fakeProvider{
		name: "throttled",
		min:  spacing,
		run: func(context.Context, SearchRequest) ([]Result, error) {
			mu.Lock()
			stamps = append(stamps, time.Now())
			mu.Unlock()
			return []Result{fakeResult("throttled")}, nil
		},
	}

	tool := recordingTool(t, throttled)
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
	if gap := stamps[1].Sub(stamps[0]); gap < spacing {
		t.Fatalf("provider calls not throttled: gap %s < %s", gap, spacing)
	}
}

func TestSearchNilToolIsSafe(t *testing.T) {
	t.Parallel()

	var tool *Search
	if _, err := tool.Run(context.Background(), `{"query":"x"}`); err == nil {
		t.Fatal("expected error from nil tool")
	}
	if got := tool.Providers(); got != nil {
		t.Fatalf("Providers on nil tool = %v", got)
	}
}

func TestSearchPrefersIdleProviderOverThrottledRotation(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var ran []string

	throttled := fakeProvider{
		name: "throttled",
		min:  time.Hour,
		run: func(context.Context, SearchRequest) ([]Result, error) {
			mu.Lock()
			ran = append(ran, "throttled")
			mu.Unlock()
			return []Result{fakeResult("throttled")}, nil
		},
	}
	idle := fakeProvider{
		name: "idle",
		run: func(context.Context, SearchRequest) ([]Result, error) {
			mu.Lock()
			ran = append(ran, "idle")
			mu.Unlock()
			return []Result{fakeResult("idle")}, nil
		},
	}

	tool := recordingTool(t, throttled, idle)
	// Book the throttled provider's slot so the rotation lands on a provider
	// that cannot run for an hour.
	if err := tool.providers[0].wait(context.Background()); err != nil {
		t.Fatalf("priming throttle: %v", err)
	}

	out, err := tool.Run(context.Background(), `{"query":"idle"}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, "result from idle") {
		t.Fatalf("out = %q, want the idle provider's result", out)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(ran) != 1 || ran[0] != "idle" {
		t.Fatalf("providers run = %v, want only idle", ran)
	}
}

func TestSearchCoolsDownAFailingProvider(t *testing.T) {
	t.Parallel()

	calls := 0
	brittle := fakeProvider{
		name: "brittle",
		run: func(context.Context, SearchRequest) ([]Result, error) {
			calls++
			return nil, fmt.Errorf("backend down")
		},
	}
	tool := recordingTool(t, brittle)

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

func TestThrottledProviderWaitSerialisesConcurrentCallers(t *testing.T) {
	t.Parallel()

	spacing := 40 * time.Millisecond
	p := &throttledProvider{min: spacing}

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

func TestThrottledProviderCancellationDoesNotReserveSlots(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		p := &throttledProvider{min: time.Second}
		start := time.Now()
		if err := p.wait(context.Background()); err != nil {
			t.Fatal(err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		if err := p.wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("canceled wait: %v", err)
		}

		ctx, cancel = context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := p.wait(ctx); err != nil {
			t.Fatalf("canceled caller delayed the next request: %v", err)
		}
		if elapsed := time.Since(start); elapsed != time.Second {
			t.Fatalf("next request after %v, want 1s", elapsed)
		}
	})
}

func TestSearchCanceledBeforeRequest(t *testing.T) {
	t.Parallel()

	calls := 0
	tool := recordingTool(t, fakeProvider{
		name: "healthy",
		run: func(context.Context, SearchRequest) ([]Result, error) {
			calls++
			return []Result{fakeResult("healthy")}, nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := tool.Run(ctx, `{"query":"canceled"}`); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context cancellation", err)
	}
	if calls != 0 {
		t.Fatalf("canceled request called provider %d times", calls)
	}
}

func TestSearchCallerCancellationDoesNotCoolDownProvider(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		calls := 0
		tool := recordingTool(t, fakeProvider{
			name: "healthy",
			run: func(ctx context.Context, _ SearchRequest) ([]Result, error) {
				calls++
				if ctx.Done() != nil {
					<-ctx.Done()
					return nil, ctx.Err()
				}
				return []Result{fakeResult("healthy")}, nil
			},
		})
		for i := 0; i < providerFailureThreshold; i++ {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			_, err := tool.Run(ctx, `{"query":"short deadline"}`)
			cancel()
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("Run() error = %v, want caller deadline", err)
			}
		}

		out, err := tool.Run(context.Background(), `{"query":"healthy"}`)
		if err != nil || !strings.Contains(out, "result from healthy") {
			t.Fatalf("healthy provider unavailable after canceled calls: %q, %v", out, err)
		}
		if calls != providerFailureThreshold+1 {
			t.Fatalf("calls = %d, want %d", calls, providerFailureThreshold+1)
		}
	})
}
