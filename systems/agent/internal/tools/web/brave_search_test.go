package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBraveWebSearchDefinition(t *testing.T) {
	tool, err := NewBraveWebSearch("test-key")
	if err != nil {
		t.Fatalf("NewBraveWebSearch() error = %v", err)
	}

	def := tool.Definition()
	if def.Name != "web_search" {
		t.Fatalf("Definition().Name = %q, want %q", def.Name, "web_search")
	}
	properties, ok := def.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf(
			"Definition().Parameters[properties] = %#v, want object",
			def.Parameters["properties"],
		)
	}
	query, ok := properties["query"].(map[string]any)
	if !ok {
		t.Fatalf(
			"Definition().Parameters.properties[query] = %#v, want object",
			properties["query"],
		)
	}
	if got := query["maxLength"]; got != maxBraveSearchQueryChars {
		t.Fatalf("query maxLength = %#v, want %d", got, maxBraveSearchQueryChars)
	}
}

func TestBraveWebSearchRunErrorsOnInvalidJSON(t *testing.T) {
	tool, _ := NewBraveWebSearch("test-key")

	_, err := tool.Run(context.Background(), "{")
	if err == nil || !strings.Contains(err.Error(), "invalid arguments JSON") {
		t.Fatalf("Run() error = %v, want invalid arguments JSON", err)
	}
}

func TestBraveWebSearchRunErrorsOnMissingQuery(t *testing.T) {
	tool, _ := NewBraveWebSearch("test-key")

	_, err := tool.Run(context.Background(), `{"query":"   "}`)
	if err == nil || !strings.Contains(err.Error(), "missing required argument: query") {
		t.Fatalf("Run() error = %v, want missing query error", err)
	}
}

func TestBraveWebSearchRunErrorsBeforeRequestOnTooLongQuery(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests++
	}))
	defer server.Close()

	tool, _ := NewBraveWebSearch("test-key")
	tool.baseURL = server.URL
	tool.client = server.Client()

	cases := []struct {
		name  string
		query string
		want  string
	}{
		{
			name:  "characters",
			query: strings.Repeat("a", maxBraveSearchQueryChars+1),
			want:  "401 characters",
		},
		{
			name:  "words",
			query: strings.TrimSpace(strings.Repeat("word ", maxBraveSearchQueryWords+1)),
			want:  "51 words",
		},
	}
	for _, tc := range cases {
		_, err := tool.Run(context.Background(), `{"query":"`+tc.query+`"}`)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s Run() error = %v, want %q", tc.name, err, tc.want)
		}
	}
	if requests != 0 {
		t.Fatalf("requests = %d, want 0", requests)
	}
}

func TestBraveWebSearchRunFormatsResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Subscription-Token") != "test-key" {
			t.Fatalf("missing/incorrect brave token header")
		}
		if got := r.URL.Query().Get("q"); got != "golang news" {
			t.Fatalf("q = %q, want %q", got, "golang news")
		}
		if got := r.URL.Query().Get("count"); got != "2" {
			t.Fatalf("count = %q, want %q", got, "2")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"web": {
				"results": [
					{"title":"Go 1.26 Released","url":"https://go.dev/blog/1.26","description":"Release notes"},
					{"title":"Golang Weekly","url":"https://golangweekly.com","description":"Weekly roundup"}
				]
			}
		}`))
	}))
	defer server.Close()

	tool, err := NewBraveWebSearch("test-key")
	if err != nil {
		t.Fatalf("NewBraveWebSearch() error = %v", err)
	}
	tool.baseURL = server.URL
	tool.client = server.Client()

	got, err := tool.Run(context.Background(), `{"query":"  golang\n news  ","count":2}`)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	wantContains := []string{
		"Results for: golang news",
		"1. Go 1.26 Released",
		"https://go.dev/blog/1.26",
		"Release notes",
		"2. Golang Weekly",
	}
	for _, want := range wantContains {
		if !strings.Contains(got, want) {
			t.Fatalf("Run() output missing %q\noutput:\n%s", want, got)
		}
	}
}

func TestBraveWebSearchRunNoResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"web":{"results":[]}}`))
	}))
	defer server.Close()

	tool, _ := NewBraveWebSearch("test-key")
	tool.baseURL = server.URL
	tool.client = server.Client()

	got, err := tool.Run(context.Background(), `{"query":"nothing"}`)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got != "No results for: nothing" {
		t.Fatalf("Run() = %q, want %q", got, "No results for: nothing")
	}
}

func TestBraveWebSearchRunHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer server.Close()

	tool, _ := NewBraveWebSearch("test-key")
	tool.baseURL = server.URL
	tool.client = server.Client()

	_, err := tool.Run(context.Background(), `{"query":"x"}`)
	if err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("Run() error = %v, want HTTP status error", err)
	}
}

func TestBraveWebSearchRunDefaultAndClampCount(t *testing.T) {
	var counts []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		counts = append(counts, r.URL.Query().Get("count"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"web":{"results":[]}}`))
	}))
	defer server.Close()

	tool, _ := NewBraveWebSearch("test-key")
	tool.baseURL = server.URL
	tool.client = server.Client()

	cases := []string{
		`{"query":"a"}`,
		`{"query":"b","count":0}`,
		`{"query":"c","count":99}`,
	}
	for _, args := range cases {
		if _, err := tool.Run(context.Background(), args); err != nil {
			t.Fatalf("Run(%s) error = %v", args, err)
		}
	}

	want := []string{"5", "1", "10"}
	if len(counts) != len(want) {
		t.Fatalf("counts len = %d, want %d", len(counts), len(want))
	}
	for i := range want {
		if counts[i] != want[i] {
			t.Fatalf("counts[%d] = %q, want %q", i, counts[i], want[i])
		}
	}
}
