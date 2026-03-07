package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestWebFetchDefinition(t *testing.T) {
	tool := NewWebFetch()

	def := tool.Definition()
	if def.Name != "web_fetch" {
		t.Fatalf("Definition().Name = %q, want %q", def.Name, "web_fetch")
	}
}

func TestWebFetchRunErrorsOnInvalidJSON(t *testing.T) {
	tool := NewWebFetch()

	_, err := tool.Run(context.Background(), "{")
	if err == nil || !strings.Contains(err.Error(), "invalid arguments JSON") {
		t.Fatalf("Run() error = %v, want invalid arguments JSON", err)
	}
}

func TestWebFetchRunErrorsOnMissingURL(t *testing.T) {
	tool := NewWebFetch()

	_, err := tool.Run(context.Background(), `{"url":"   "}`)
	if err == nil || !strings.Contains(err.Error(), "missing required argument: url") {
		t.Fatalf("Run() error = %v, want missing URL error", err)
	}
}

func TestWebFetchRunErrorsOnInvalidMode(t *testing.T) {
	tool := NewWebFetch()

	_, err := tool.Run(context.Background(), `{"url":"https://example.com","mode":"weird"}`)
	if err == nil || !strings.Contains(err.Error(), "invalid mode") {
		t.Fatalf("Run() error = %v, want invalid mode error", err)
	}
}

func TestWebFetchRunErrorsOnNegativeOffset(t *testing.T) {
	tool := NewWebFetch()

	_, err := tool.Run(context.Background(), `{"url":"https://example.com","offset":-1}`)
	if err == nil || !strings.Contains(err.Error(), "offset must be >= 0") {
		t.Fatalf("Run() error = %v, want negative offset error", err)
	}
}

func TestWebFetchRunArticleModeFormatsMetadata(t *testing.T) {
	paragraphA := strings.Repeat(
		"This is a long article paragraph with enough text to satisfy readability extraction. ",
		8,
	)
	paragraphB := strings.Repeat(
		"This second paragraph adds more article content so the article extractor has plenty of text. ",
		8,
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html>
<html>
  <head>
    <title>Example Article</title>
    <meta property="og:site_name" content="Example News">
    <meta name="author" content="Casey Writer">
    <meta property="article:published_time" content="2026-03-01T10:00:00Z">
  </head>
  <body>
    <article>
      <h1>Example Article</h1>
      <p>` + paragraphA + `</p>
      <p>` + paragraphB + `<a href="/story">Source link</a>.</p>
    </article>
  </body>
</html>`))
	}))
	defer server.Close()

	tool := NewWebFetch()
	tool.client = server.Client()

	got, err := tool.Run(context.Background(), `{"url":"`+server.URL+`","mode":"article"}`)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	wantContains := []string{
		"URL: " + server.URL,
		"Title: Example Article",
		"Source: article",
		"Site: Example News",
		"Byline: Casey Writer",
		"Published: 2026-03-01T10:00:00Z",
		"--- CONTENT ---",
		server.URL + "/story",
	}
	for _, want := range wantContains {
		if !strings.Contains(got, want) {
			t.Fatalf("Run() output missing %q\noutput:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Requested-URL:") {
		t.Fatalf("Run() should omit Requested-URL when unchanged\noutput:\n%s", got)
	}
}

func TestWebFetchRunAutoFallsBackToPageAndCleansContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept"); got != "text/html,application/xhtml+xml" {
			t.Fatalf("Accept = %q, want %q", got, "text/html,application/xhtml+xml")
		}
		if got := r.Header.Get("User-Agent"); strings.TrimSpace(got) == "" {
			t.Fatalf("User-Agent should not be empty")
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html>
<html>
  <head>
    <title>Docs Page</title>
    <script>window.bad = true</script>
  </head>
  <body>
    <nav>Navigation</nav>
    <main>
      <h1>Docs</h1>
      <p>Read <a href="/guide">the guide</a>.</p>
      <div hidden>Hidden secret</div>
      <table>
        <tr><td>Name</td><td>Value</td></tr>
        <tr><td>Mode</td><td>auto</td></tr>
        <tr><td> </td><td> </td></tr>
      </table>
    </main>
    <footer>Footer text</footer>
  </body>
</html>`))
	}))
	defer server.Close()

	tool := NewWebFetch()
	tool.client = server.Client()

	got, err := tool.Run(context.Background(), `{"url":"`+server.URL+`","mode":"auto"}`)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	for _, want := range []string{
		"Title: Docs Page",
		"Source: page",
		"--- CONTENT ---",
		server.URL + "/guide",
		"| Name | Value |",
		"|---|---|",
		"| Mode | auto |",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("Run() output missing %q\noutput:\n%s", want, got)
		}
	}
	for _, notWanted := range []string{
		"Navigation",
		"Hidden secret",
		"Footer text",
		"window.bad",
	} {
		if strings.Contains(got, notWanted) {
			t.Fatalf("Run() output should not contain %q\noutput:\n%s", notWanted, got)
		}
	}
}

func TestWebFetchRunIncludesRequestedURLOnRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			http.Redirect(w, r, "/final", http.StatusFound)
		case "/final":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(
				[]byte(
					`<!doctype html><html><head><title>Redirected</title></head><body><p>Redirect target.</p></body></html>`,
				),
			)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	tool := NewWebFetch()
	tool.client = server.Client()

	got, err := tool.Run(context.Background(), `{"url":"`+server.URL+`/start","mode":"page"}`)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if !strings.Contains(got, "URL: "+server.URL+"/final") {
		t.Fatalf("Run() output missing final URL\noutput:\n%s", got)
	}
	if !strings.Contains(got, "Requested-URL: "+server.URL+"/start") {
		t.Fatalf("Run() output missing requested URL\noutput:\n%s", got)
	}
}

func TestWebFetchRunDecodesCharset(t *testing.T) {
	body := []byte(
		"<!doctype html><html><head><title>Caf\xe9</title></head><body><p>Caf\xe9 details.</p></body></html>",
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=windows-1252")
		_, _ = w.Write(body)
	}))
	defer server.Close()

	tool := NewWebFetch()
	tool.client = server.Client()

	got, err := tool.Run(context.Background(), `{"url":"`+server.URL+`","mode":"page"}`)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(got, "Title: Café") || !strings.Contains(got, "Café details.") {
		t.Fatalf("Run() output should contain decoded charset content\noutput:\n%s", got)
	}
}

func TestWebFetchRunRejectsNonHTML(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write([]byte("%PDF-1.4"))
	}))
	defer server.Close()

	tool := NewWebFetch()
	tool.client = server.Client()

	_, err := tool.Run(context.Background(), `{"url":"`+server.URL+`"}`)
	if err == nil || !strings.Contains(err.Error(), "not an HTML document") {
		t.Fatalf("Run() error = %v, want non-HTML error", err)
	}
}

func TestWebFetchRunHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad gateway", http.StatusBadGateway)
	}))
	defer server.Close()

	tool := NewWebFetch()
	tool.client = server.Client()

	_, err := tool.Run(context.Background(), `{"url":"`+server.URL+`"}`)
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("Run() error = %v, want HTTP status error", err)
	}
}

func TestWebFetchRunTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><html><body><p>Slow page.</p></body></html>`))
	}))
	defer server.Close()

	tool := NewWebFetch()
	client := server.Client()
	client.Timeout = 50 * time.Millisecond
	tool.client = client

	_, err := tool.Run(context.Background(), `{"url":"`+server.URL+`"}`)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("Run() error = %v, want timeout error", err)
	}
}

func TestWebFetchRunClampsMaxCharsAndCachesSlices(t *testing.T) {
	var hits atomic.Int32
	longText := strings.Repeat("x", 1600)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(
			[]byte(
				`<!doctype html><html><head><title>Long Page</title></head><body><p>` + longText + `</p></body></html>`,
			),
		)
	}))
	defer server.Close()

	tool := NewWebFetch()
	tool.client = server.Client()

	first, err := tool.Run(
		context.Background(),
		`{"url":"`+server.URL+`","mode":"page","max_chars":10}`,
	)
	if err != nil {
		t.Fatalf("Run() first error = %v", err)
	}
	second, err := tool.Run(
		context.Background(),
		`{"url":"`+server.URL+`","mode":"page","offset":1000,"max_chars":10}`,
	)
	if err != nil {
		t.Fatalf("Run() second error = %v", err)
	}

	for _, want := range []string{
		"Slice: chars 1-1000 of 1600",
		"More: true",
		"Next-Offset: 1000",
		"--- CONTENT ---",
	} {
		if !strings.Contains(first, want) {
			t.Fatalf("first slice missing %q\noutput:\n%s", want, first)
		}
	}
	for _, want := range []string{
		"Slice: chars 1001-1600 of 1600",
		"More: false",
		"Next-Offset: none",
	} {
		if !strings.Contains(second, want) {
			t.Fatalf("second slice missing %q\noutput:\n%s", want, second)
		}
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("server hits = %d, want 1 (cache should serve second slice)", got)
	}
}

func TestWebFetchRunErrorsOnOffsetPastEnd(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(
			[]byte(
				`<!doctype html><html><head><title>Short</title></head><body><p>short page</p></body></html>`,
			),
		)
	}))
	defer server.Close()

	tool := NewWebFetch()
	tool.client = server.Client()

	_, err := tool.Run(context.Background(), `{"url":"`+server.URL+`","mode":"page","offset":20}`)
	if err == nil || !strings.Contains(err.Error(), "past end of content") {
		t.Fatalf("Run() error = %v, want offset past end error", err)
	}
}

func TestWebFetchRunEnforcesBodyLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(
			[]byte(`<!doctype html><html><body>` + strings.Repeat("a", 2048) + `</body></html>`),
		)
	}))
	defer server.Close()

	tool := NewWebFetch()
	tool.client = server.Client()
	tool.maxResponseBytes = 1024

	_, err := tool.Run(context.Background(), `{"url":"`+server.URL+`"}`)
	if err == nil || !strings.Contains(err.Error(), "exceeded 1024 bytes") {
		t.Fatalf("Run() error = %v, want response size error", err)
	}
}

func TestWebFetchRunArticleModeErrorsWhenNoReadableArticleExists(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(
			[]byte(
				`<!doctype html><html><head><title>Short</title></head><body><p>tiny page</p></body></html>`,
			),
		)
	}))
	defer server.Close()

	tool := NewWebFetch()
	tool.client = server.Client()

	_, err := tool.Run(context.Background(), `{"url":"`+server.URL+`","mode":"article"}`)
	if err == nil || !strings.Contains(err.Error(), "no readable article content found") {
		t.Fatalf("Run() error = %v, want no article error", err)
	}
}
