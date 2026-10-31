package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// braveTestProvider wires a Brave provider at a test server serving payload.
func braveTestProvider(t *testing.T, payload []byte) *BraveWebSearch {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Subscription-Token") != "test-key" {
			t.Errorf("missing/incorrect brave token header")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	}))
	t.Cleanup(server.Close)

	tool, err := NewBraveWebSearch("test-key")
	if err != nil {
		t.Fatalf("NewBraveWebSearch() error = %v", err)
	}
	tool.baseURL = server.URL
	tool.client = server.Client()
	return tool
}

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

func TestBraveWebSearchSearchMapsGoldenPayload(t *testing.T) {
	payload, err := os.ReadFile("testdata/brave_search.json")
	if err != nil {
		t.Fatalf("read golden payload: %v", err)
	}
	tool := braveTestProvider(t, payload)

	results, err := tool.Search(
		context.Background(),
		SearchRequest{Query: "go 1.26 release", Count: 2},
	)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("Search() returned %d results, want 2", len(results))
	}

	want := []Result{
		{
			Title:     "Go 1.26 Released & Available",
			URL:       "https://go.dev/blog/go1.26?utm_source=newsletter&utm_medium=email#top",
			Snippet:   "Go 1.26 adds generic type aliases and a faster toolchain.",
			Published: time.Date(2026, 2, 10, 15, 4, 5, 0, time.UTC),
		},
		{
			Title:   "Go 1.26 release notes",
			URL:     "https://go.dev/doc/go1.26/",
			Snippet: "The release notes for Go 1.26.",
		},
	}
	for i, w := range want {
		got := results[i]
		if got.Title != w.Title {
			t.Fatalf("results[%d].Title = %q, want %q", i, got.Title, w.Title)
		}
		if got.URL != w.URL {
			t.Fatalf("results[%d].URL = %q, want %q", i, got.URL, w.URL)
		}
		if got.Snippet != w.Snippet {
			t.Fatalf("results[%d].Snippet = %q, want %q", i, got.Snippet, w.Snippet)
		}
		if !got.Published.Equal(w.Published) {
			t.Fatalf("results[%d].Published = %v, want %v", i, got.Published, w.Published)
		}
	}
}

func TestBraveWebSearchOmitsNavigationSummaries(t *testing.T) {
	cases := []struct {
		name        string
		description string
		want        string
	}{
		{
			name:        "year index",
			description: "令和4年分の確定申告に関する手引き等 · 令和3年分の確定申告に関する手引き等 · 所得税（確定申告書等作成コーナー） ·",
		},
		{
			name:        "breadcrumb with trailing ellipsis",
			description: "ホーム &middot; 税の情報・手続・用紙 &middot; 確定申告に関する手引き等 &middot; ...",
		},
		{
			name:        "incomplete PDF bullet",
			description: "• 確定申告書、付表、計算書等は国税庁ホームページからダウンロードで",
		},
		{
			name:        "prose after breadcrumbs",
			description: "ホーム · 所得税 · 申告書の提出方法を説明しています。必要書類を準備してください。",
			want:        "申告書の提出方法を説明しています。必要書類を準備してください。",
		},
		{
			name:        "English breadcrumb separators",
			description: "Home › Guides › Learn how to migrate components with explicit runes.",
			want:        "Learn how to migrate components with explicit runes.",
		},
		{
			name:        "plain unpunctuated summary",
			description: "Replace implicit reactivity with runes",
			want:        "Replace implicit reactivity with runes",
		},
		{
			name:        "complete Japanese bullet",
			description: "• 申告書は国税庁ホームページからダウンロードできます。",
			want:        "申告書は国税庁ホームページからダウンロードできます。",
		},
		{
			name:        "download link title containing a question mark",
			description: "申告忘れていませんか？（令和7年12月）（PDF/729KB） · 計算書の使い方（PDF/387KB）",
		},
		{
			name:        "prose with a download size after breadcrumbs",
			description: "ホーム · この手引きでは申告書の記載方法を説明しています。（PDF/729KB）",
			want:        "この手引きでは申告書の記載方法を説明しています。（PDF/729KB）",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := json.Marshal(
				map[string]any{"web": map[string]any{"results": []map[string]string{{
					"title": "Guide", "url": "https://example.com/guide", "description": tc.description,
				}}}},
			)
			if err != nil {
				t.Fatal(err)
			}
			results, err := braveTestProvider(
				t,
				payload,
			).Search(context.Background(), SearchRequest{Query: "確定申告 runes", Count: 1})
			if err != nil {
				t.Fatal(err)
			}
			if len(results) != 1 || results[0].URL != "https://example.com/guide" {
				t.Fatalf("lost source: %+v", results)
			}
			if results[0].Snippet != tc.want {
				t.Errorf("Snippet = %q, want %q", results[0].Snippet, tc.want)
			}
		})
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
		if got := r.URL.Query().Get("text_decorations"); got != "false" {
			t.Fatalf("text_decorations = %q, want %q", got, "false")
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
		"brave (rotation) · 2 results",
		"1. Go 1.26 Released",
		"   https://go.dev/blog/1.26",
		"   Release notes",
		"2. Golang Weekly",
	}
	for _, want := range wantContains {
		if !strings.Contains(got, want) {
			t.Fatalf("Run() output missing %q\noutput:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Results for:") {
		t.Fatalf("Run() still echoes the query\noutput:\n%s", got)
	}
}

func TestBraveWebSearchRunNoResults(t *testing.T) {
	tool := braveTestProvider(t, []byte(`{"web":{"results":[]}}`))

	got, err := tool.Run(context.Background(), `{"query":"nothing"}`)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(got, "brave (rotation) · 0 results") {
		t.Fatalf("Run() = %q, want an attributed zero-result header", got)
	}
	if strings.Contains(got, "No results for:") {
		t.Fatalf("Run() = %q, want the empty shape, not a query echo", got)
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
