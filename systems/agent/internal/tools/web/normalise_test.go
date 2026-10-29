package web

import (
	"context"
	"strings"
	"testing"
)

func TestCanonicalURL(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "strips campaign parameters",
			in:   "https://example.com/post?utm_source=news&utm_medium=email&fbclid=abc&gclid=d",
			want: "https://example.com/post",
		},
		{
			name: "strips ref parameters",
			in:   "https://example.com/post?ref=hn&ref_src=twsrc",
			want: "https://example.com/post",
		},
		{
			name: "keeps document selecting parameters",
			in:   "https://example.com/watch?v=abc123&id=7",
			want: "https://example.com/watch?id=7&v=abc123",
		},
		{
			name: "drops fragment and trailing slash",
			in:   "https://Example.COM/Path/?utm_source=x#section",
			want: "https://example.com/Path",
		},
		{
			name: "root path keeps a fetchable form",
			in:   "https://example.com/?utm_source=x",
			want: "https://example.com",
		},
		{
			name: "drops default port",
			in:   "https://example.com:443/a",
			want: "https://example.com/a",
		},
		{
			name: "keeps non default port",
			in:   "http://example.com:8080/a",
			want: "http://example.com:8080/a",
		},
		{
			name: "leaves non http urls alone",
			in:   "mailto:someone@example.com",
			want: "mailto:someone@example.com",
		},
		{
			name: "trims whitespace",
			in:   "  https://example.com/a  ",
			want: "https://example.com/a",
		},
	}
	for _, tc := range cases {
		if got := canonicalURL(tc.in); got != tc.want {
			t.Fatalf("%s: canonicalURL(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

func TestNormaliseResultsDeduplicatesBeforeTruncating(t *testing.T) {
	t.Parallel()

	results := []Result{
		{
			Title:   "The Go memory model, explained",
			URL:     "https://example.com/memory?utm_source=twitter",
			Snippet: "the go memory model",
		},
		{
			// Same article, syndicated on another domain: same title, other URL.
			Title:   "The Go memory model, explained",
			URL:     "https://mirror.example.org/2026/memory-model",
			Snippet: "the go memory model",
		},
		{
			// Same document, tracking parameters only.
			Title:   "Something else entirely",
			URL:     "https://example.com/memory?utm_medium=email",
			Snippet: "the go memory model",
		},
		{
			Title:   "Go scheduler internals",
			URL:     "https://example.com/scheduler",
			Snippet: "the go scheduler",
		},
	}

	got := normaliseResults(results, "", 10)
	if len(got) != 2 {
		t.Fatalf("normaliseResults() kept %d results, want 2: %+v", len(got), got)
	}
	if got[0].URL != "https://example.com/memory" {
		t.Fatalf("got[0].URL = %q, want the canonical URL", got[0].URL)
	}
	if got[1].Title != "Go scheduler internals" {
		t.Fatalf("got[1].Title = %q, want the distinct source", got[1].Title)
	}

	// Truncation happens after dedupe, so a count of 2 is filled by two
	// distinct sources rather than one source and its syndicated copy.
	if trunc := normaliseResults(results, "", 2); len(trunc) != 2 {
		t.Fatalf("normaliseResults(count 2) kept %d results, want 2", len(trunc))
	}
}

func TestNormaliseResultsBoundsSnippetsFromAnyProvider(t *testing.T) {
	t.Parallel()

	huge := strings.Repeat("filler prose about nothing in particular. ", 400)
	got := normaliseResults(
		[]Result{{Title: "t", URL: "https://example.com", Snippet: huge}},
		"",
		1,
	)
	if len(got) != 1 {
		t.Fatalf("kept %d results, want 1", len(got))
	}
	if n := len([]rune(got[0].Snippet)); n > maxExcerptRunes {
		t.Fatalf("snippet is %d runes, want <= %d", n, maxExcerptRunes)
	}
}

// TestNormaliseResultsWindowsAnOversizedSnippetByQuery covers the hole a third
// adapter would otherwise reopen: an adapter that returns a document despite
// the excerpt invariant must be re-windowed onto the matched passage, not onto
// its leading text, which is navigation in every real page.
func TestNormaliseResultsWindowsAnOversizedSnippetByQuery(t *testing.T) {
	t.Parallel()

	nav := strings.Repeat("On this page deployment licensing release history. ", 60)
	filler := strings.Repeat("Appendix material repeats the deployment notes. ", 40)
	buried := "The runes migration guide explains explicit reactivity and which compiler flags keep legacy syntax compiling. "
	huge := nav + filler + buried + filler

	got := normaliseResults(
		[]Result{{Title: "Svelte runes", URL: "https://svelte.dev/docs/migration", Snippet: huge}},
		"svelte runes migration guide",
		1,
	)
	if len(got) != 1 {
		t.Fatalf("kept %d results, want 1", len(got))
	}
	snippet := got[0].Snippet
	if n := len([]rune(snippet)); n > maxExcerptRunes {
		t.Fatalf("snippet is %d runes, want <= %d", n, maxExcerptRunes)
	}
	if !strings.Contains(snippet, "explicit reactivity") {
		t.Fatalf("snippet lost the matched passage: %q", snippet)
	}
	if strings.Contains(snippet, "On this page") {
		t.Fatalf("snippet starts from the nav block: %q", snippet)
	}
}

func TestSearchOutputDeduplicatesSyndicatedResults(t *testing.T) {
	t.Parallel()

	syndicated := fakeProvider{
		name: "brave",
		run: func(context.Context, SearchRequest) ([]Result, error) {
			return []Result{
				{Title: "Runes migration guide", URL: "https://a.example/guide", Snippet: "one"},
				{Title: "Runes migration guide", URL: "https://b.example/guide", Snippet: "two"},
				{Title: "Svelte changelog", URL: "https://c.example/changelog", Snippet: "three"},
			}, nil
		},
	}
	tool := recordingTool(t, syndicated)

	out, err := tool.Run(context.Background(), `{"query":"svelte runes","count":2}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if header := firstLine(out); header != "brave (rotation) · 2 results" {
		t.Fatalf("header = %q, want two distinct results", header)
	}
	if strings.Contains(out, "b.example") {
		t.Fatalf("syndicated duplicate survived:\n%s", out)
	}
}
