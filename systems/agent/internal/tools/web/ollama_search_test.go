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

func TestOllamaWebSearchRenderedExcerptKeepsMatchedPassage(t *testing.T) {
	payload, err := json.Marshal(map[string]any{
		"results": []map[string]string{{
			"title":   "Svelte documentation",
			"url":     "https://svelte.dev/docs/svelte/migration-guide",
			"content": hugeOllamaContent(),
		}},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	tool, err := NewSearch([]Provider{ollamaTestProvider(t, payload)})
	if err != nil {
		t.Fatalf("NewSearch: %v", err)
	}
	for _, args := range []string{
		`{"query":"svelte runes migration guide","count":1}`,
		`{"query":"svelte runes migration guide","count":1,"snippet_chars":150}`,
	} {
		out, err := tool.Run(context.Background(), args)
		if err != nil {
			t.Fatalf("Run(%s): %v", args, err)
		}
		if !strings.Contains(out, "explicit runes") {
			t.Errorf("Run(%s) lost the matched passage:\n%s", args, out)
		}
	}
}

func TestOllamaWebSearchPrefersProseToDocumentScaffolding(t *testing.T) {
	cases := []struct {
		name  string
		query string
		body  string
		want  string
		avoid []string
	}{
		{
			name:  "query dense navigation and code",
			query: "svelte runes migration guide",
			body: "### On this page\n\n" + strings.Repeat("- Svelte runes migration guide\n", 80) +
				"\n```ts\n" + strings.Repeat("export interface SvelteRunes { migration: Guide; }\n", 80) +
				"```\n\nThe Svelte runes migration guide explains how to replace implicit reactivity with explicit signals.\n",
			want:  "explicit signals",
			avoid: []string{"On this page", "export interface", "```"},
		},
		{
			name:  "Japanese navigation tables",
			query: "確定申告 所得税 手引き",
			body: "# 確定申告の手引き\n\n" + strings.Repeat("| 所得税 | 確定申告 | 手引き |\n", 80) +
				"\nこの手引きでは、所得税の確定申告に必要な手続きを説明しています。収入と控除の資料を準備してください。\n",
			want:  "収入と控除の資料を準備してください。",
			avoid: []string{"|", "#"},
		},
		{
			name:  "PDF form glyphs around readable Japanese",
			query: "確定申告 手引き",
			body: "確定申告書の記載手順\n\n" + strings.Repeat("⛯ົ⨫㛗 ௧࿴ ᖺ ᭶ ᪥ 䜃 ⛯ 䛾 ⏦࿌\n\n", 80) +
				"確定申告の手引きには、申告書の記載方法を説明しています。マイナンバーを記入し、本人確認書類を用意してください。\n\n" +
				strings.Repeat("ᡤ ᚟⯆ ᚓ ≉ ⛯ ูᡤ ཬ ᚓ\n\n", 80),
			want:  "マイナンバーを記入し、本人確認書類を用意してください。",
			avoid: []string{"⛯ົ", "᚟⯆"},
		},
		{
			name:  "navigation only page keeps its source without a body dump",
			query: "確定申告 手引き",
			body:  "Reference\n\n# 確定申告の手引き\n\n- 令和７年分の手引き\n- 令和６年分の手引き\n",
			avoid: []string{"令和７年", "令和６年", "#"},
		},
		{
			name:  "matching fences exclude code comments with sentences",
			query: "migration runes",
			body: "````ts\n// Runes migration.\n```\n// Still inside the migration code.\n````\n\n" +
				"~~~js\n// More runes migration.\n~~~\n\nThe migration uses runes for explicit reactivity.\n",
			want:  "explicit reactivity",
			avoid: []string{"//", "```", "~~~"},
		},
		{
			name:  "Japanese prose with spaces between glyphs is preserved",
			query: "確定申告",
			body:  "確 定 申 告 の 資 料 を 準 備 し て く だ さ い 。\n",
			want:  "確 定 申 告 の 資 料 を 準 備 し て く だ さ い 。",
		},
		{
			name:  "substantive bullet instructions",
			query: "migration runes",
			body:  "# Migration\n\n- Replace implicit reactivity with runes.\n- Keep legacy components until they can be migrated.\n",
			want:  "Replace implicit reactivity with runes.",
		},
		{
			name:  "plain summary without punctuation",
			query: "migration runes",
			body:  "Replace implicit reactivity with runes",
			want:  "Replace implicit reactivity with runes",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := json.Marshal(map[string]any{"results": []map[string]string{{
				"title": "Reference", "url": "https://example.com/reference", "content": tc.body,
			}}})
			if err != nil {
				t.Fatal(err)
			}
			results, err := ollamaTestProvider(
				t,
				payload,
			).Search(context.Background(), SearchRequest{Query: tc.query, Count: 1})
			if err != nil {
				t.Fatal(err)
			}
			if len(results) != 1 || results[0].URL != "https://example.com/reference" {
				t.Fatalf("lost source: %+v", results)
			}
			snippet := results[0].Snippet
			if tc.want == "" && snippet != "" {
				t.Errorf("navigation-only page has an excerpt: %q", snippet)
			}
			if !strings.Contains(snippet, tc.want) {
				t.Errorf("excerpt lost %q: %q", tc.want, snippet)
			}
			for _, avoid := range tc.avoid {
				if strings.Contains(snippet, avoid) {
					t.Errorf("excerpt contains scaffolding %q: %q", avoid, snippet)
				}
			}
		})
	}
}
