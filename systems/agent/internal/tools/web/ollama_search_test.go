package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// ollamaTestProvider wires an Ollama provider at a test server serving payload.
func ollamaTestProvider(t *testing.T, payload []byte) *OllamaWebSearch {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	}))
	t.Cleanup(server.Close)

	provider, err := NewOllamaWebSearch("test-key")
	if err != nil {
		t.Fatalf("NewOllamaWebSearch() error = %v", err)
	}
	provider.baseURL = server.URL
	provider.client = server.Client()
	return provider
}

func TestOllamaWebSearchSearchMapsGoldenPayload(t *testing.T) {
	payload, err := os.ReadFile("testdata/ollama_search.json")
	if err != nil {
		t.Fatalf("read golden payload: %v", err)
	}
	provider := ollamaTestProvider(t, payload)

	results, err := provider.Search(
		context.Background(),
		SearchRequest{Query: "svelte 6 runes migration guide", Count: 2},
	)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("Search() returned %d results, want 2", len(results))
	}

	first := results[0]
	if first.Title != "Svelte 6 runes migration guide" {
		t.Fatalf("Title = %q", first.Title)
	}
	if first.URL != "https://svelte.dev/docs/svelte/migration-guide" {
		t.Fatalf("URL = %q", first.URL)
	}
	if !first.Published.IsZero() {
		t.Fatalf("Published = %v, want zero: ollama has no date field", first.Published)
	}
	if got := len([]rune(first.Snippet)); got > maxExcerptRunes {
		t.Fatalf("Snippet is %d runes, want <= %d", got, maxExcerptRunes)
	}
	if !strings.Contains(first.Snippet, "Runes replace the implicit reactivity") {
		t.Fatalf("Snippet lost the matched passage: %q", first.Snippet)
	}
	if strings.Contains(first.Snippet, "On this page") {
		t.Fatalf("Snippet kept the leading nav block: %q", first.Snippet)
	}
	if strings.Contains(first.Snippet, "Svelte 6 runes migration guide") {
		t.Fatalf("Snippet kept the title echo: %q", first.Snippet)
	}
}

func TestOllamaWebSearchSearchEmptyGoldenPayload(t *testing.T) {
	payload, err := os.ReadFile("testdata/ollama_search_empty.json")
	if err != nil {
		t.Fatalf("read golden payload: %v", err)
	}
	provider := ollamaTestProvider(t, payload)

	results, err := provider.Search(context.Background(), SearchRequest{Query: "nothing", Count: 5})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("Search() returned %d results, want 0", len(results))
	}
}

// hugeOllamaContent builds an 80 kB body whose first 4 kB is nav and TOC filler
// and whose only relevant passage is buried far past any leading-text cut.
func hugeOllamaContent() string {
	var b strings.Builder
	b.WriteString("On this page\n")
	for i := 0; b.Len() < 4096; i++ {
		b.WriteString("- Compiler reference: deprecated overload and type signature ")
		b.WriteString(strings.Repeat("x", 40))
		b.WriteString(" section ")
		b.WriteString(string(rune('a' + i%26)))
		b.WriteString("\n")
	}
	for b.Len() < 70000 {
		b.WriteString(
			"Unrelated configuration prose covers deployment, licensing and release history. ",
		)
	}
	b.WriteString("The runes migration guide explains how each component switches from implicit ")
	b.WriteString(
		"reactivity to explicit runes, and which compiler flags keep the legacy syntax compiling.\n",
	)
	for b.Len() < 81920 {
		b.WriteString(
			"Appendix material repeats the deployment notes without mentioning the topic. ",
		)
	}
	return b.String()
}

func TestOllamaWebSearchBoundsHugeContent(t *testing.T) {
	content := hugeOllamaContent()
	if len(content) < 80000 {
		t.Fatalf("fixture is %d bytes, want at least 80000", len(content))
	}

	payload, err := json.Marshal(map[string]any{
		"results": []map[string]string{{
			"title":   "Svelte 6 runes migration guide",
			"url":     "https://svelte.dev/docs/svelte/migration-guide",
			"content": content,
		}},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	provider := ollamaTestProvider(t, payload)

	results, err := provider.Search(
		context.Background(),
		SearchRequest{Query: "svelte runes migration guide", Count: 1},
	)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Search() returned %d results, want 1", len(results))
	}

	snippet := results[0].Snippet
	if got := len([]rune(snippet)); got > maxExcerptRunes {
		t.Fatalf("Snippet is %d runes, want <= %d", got, maxExcerptRunes)
	}
	if !strings.Contains(snippet, "explicit runes") {
		t.Fatalf("Snippet lost the buried passage: %q", snippet)
	}
	if strings.Contains(snippet, "On this page") ||
		strings.Contains(snippet, "deprecated overload") {
		t.Fatalf("Snippet contains the leading nav/TOC block: %q", snippet)
	}
}
