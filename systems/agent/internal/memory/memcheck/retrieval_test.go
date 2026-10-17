package memcheck_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/embed"
	"github.com/q15co/q15/systems/agent/internal/memory/memcheck"
)

// fixtureSearcher is a fake SemanticSearcher that pretends the embed scanner
// chunked the corpus into one section chunk per file: any search returns each
// file's chunk, named with the runtime-style path and holding that file's
// claim IDs, so every sampled claim should be covered.
type fixtureSearcher struct{}

func (fixtureSearcher) SearchSemantic(
	_ context.Context,
	_ string,
	_ int,
) ([]memcheck.SemanticHit, error) {
	return []memcheck.SemanticHit{
		{
			ID:       "semantic-facts",
			FilePath: "/fixture/semantic/facts.md",
			Text:     "## Facts\n- [c-2026-0101] The river ferry...\n- [c-2026-0102] The river ferry...",
		},
		{
			ID:       "semantic-preferences",
			FilePath: "/fixture/semantic/preferences.md",
			Text:     "## Preferences\n- [c-2026-0201] The garden gate...\n- [c-2026-0202] The garden gate...",
		},
		{
			ID:       "semantic-projects",
			FilePath: "/fixture/semantic/projects.md",
			Text: "## Projects\n- [c-2026-0301] The bakery...\n- [c-2026-0302] The umbrella...\n" +
				"- [c-2026-0303] The chess club...\n- [c-2026-0304] The kettle...\n" +
				"- [c-2026-0305] Stargazers...\n- [c-2026-0306] The freight train...\n" +
				"- [c-2026-0307] The puppet workshop...\n- [c-2026-0308] Volunteers...",
		},
	}, nil
}

// corpusClaimIDs lists the claim IDs the testdata parses to, in document
// order, so tests can predict the sample without re-parsing.
var corpusClaimIDs = []string{
	"c-2026-0101", "c-2026-0102",
	"c-2026-0201", "c-2026-0202",
	"c-2026-0301", "c-2026-0302", "c-2026-0303",
	"c-2026-0304", "c-2026-0305", "c-2026-0306",
	"c-2026-0307", "c-2026-0308",
}

// TestCheckRetrievalSelfHitCovered runs the §7.5 shape offline: with a
// searcher that returns each claim's own chunk, every sampled claim is in the
// top three and the report is clean.
func TestCheckRetrievalSelfHitCovered(t *testing.T) {
	report, err := memcheck.CheckRetrieval(
		context.Background(),
		"testdata",
		fixtureSearcher{},
		memcheck.DefaultRetrievalOptions(),
	)
	if err != nil {
		t.Fatalf("CheckRetrieval error = %v", err)
	}
	if report.Sampled != len(corpusClaimIDs) {
		t.Fatalf("sampled = %d, want all %d corpus claims", report.Sampled, len(corpusClaimIDs))
	}
	if report.TopK != 3 {
		t.Fatalf("topK = %d, want the design's 3", report.TopK)
	}
	if !report.OK() {
		t.Fatalf("self hits missed: %+v", report.Misses)
	}
}

// searchingFakes assembles fake searchers that return one or more hits for
// every query, optionally ignoring the requested topK.
type searchingFake struct {
	hits      []memcheck.SemanticHit
	honorTopK bool
	err       error
}

func (f searchingFake) SearchSemantic(
	_ context.Context,
	_ string,
	topK int,
) ([]memcheck.SemanticHit, error) {
	if f.err != nil {
		return nil, f.err
	}
	if !f.honorTopK {
		return f.hits, nil
	}
	if topK > len(f.hits) {
		topK = len(f.hits)
	}
	if topK < 0 {
		topK = 0
	}
	return f.hits[:topK], nil
}

// TestCheckRetrievalMissNamesTheClaim pins the failure shape: a corpus whose
// chunks never come back is reported miss by miss, with the claim, its file,
// its text and what the search returned instead.
func TestCheckRetrievalMissNamesTheClaim(t *testing.T) {
	junk := memcheck.SemanticHit{
		ID:       "unrelated-1",
		FilePath: "/fixture/semantic/unrelated.md",
		Text:     "a chunk that mentions no claim at all",
	}
	report, err := memcheck.CheckRetrieval(
		context.Background(),
		"testdata",
		searchingFake{hits: []memcheck.SemanticHit{junk}},
		memcheck.DefaultRetrievalOptions(),
	)
	if err != nil {
		t.Fatalf("CheckRetrieval error = %v", err)
	}
	if report.OK() {
		t.Fatal("every claim missed, want misses")
	}
	if report.Sampled != len(corpusClaimIDs) || len(report.Misses) != len(corpusClaimIDs) {
		t.Fatalf("sampled %d misses %d, want one miss per sampled claim",
			report.Sampled, len(report.Misses))
	}
	for i, miss := range report.Misses {
		if miss.ClaimID != corpusClaimIDs[i] {
			t.Fatalf("miss %d claim = %s, want %s", i, miss.ClaimID, corpusClaimIDs[i])
		}
		if miss.File == "" || miss.Text == "" || len(miss.Hits) != 1 {
			t.Fatalf("miss %d is not fully named: %+v", i, miss)
		}
	}
}

func TestCheckRetrievalRequiresExactClaimID(t *testing.T) {
	t.Parallel()
	for _, text := range []string{
		"- [c-2026-01010] A different claim with the same ID prefix.",
		"- [prefix-c-2026-0101] A different identifier containing the ID.",
		"- [c-2026-0101_suffix] A different identifier containing the ID.",
	} {
		t.Run(text, func(t *testing.T) {
			t.Parallel()
			report, err := memcheck.CheckRetrieval(
				context.Background(), "testdata", searchingFake{hits: []memcheck.SemanticHit{{
					ID: "unrelated", FilePath: "semantic/facts.md", Text: text,
				}}}, memcheck.DefaultRetrievalOptions(),
			)
			if err != nil {
				t.Fatal(err)
			}
			if !missesInclude(report.Misses, "c-2026-0101") {
				t.Fatal("a different identifier falsely covered c-2026-0101")
			}
		})
	}
}

// TestCheckRetrievalTopKHonored pins that the check hands its TopK to the
// searcher and judges only that prefix: a self-hit at position four is inside
// a top-four search but outside the design's top three.
func TestCheckRetrievalTopKHonored(t *testing.T) {
	late := memcheck.SemanticHit{
		ID:       "semantic-facts",
		FilePath: "semantic/facts.md",
		Text:     "the chunk holding c-2026-0101 in bare-relative form",
	}
	filler := func(id string) memcheck.SemanticHit {
		return memcheck.SemanticHit{
			ID:       id,
			FilePath: "/fixture/semantic/unrelated.md",
			Text:     "nothing",
		}
	}
	hits := []memcheck.SemanticHit{filler("a"), filler("b"), filler("c"), late}

	strict, err := memcheck.CheckRetrieval(
		context.Background(),
		"testdata",
		searchingFake{hits: hits, honorTopK: true},
		memcheck.RetrievalOptions{Sample: 12, TopK: 3},
	)
	if err != nil {
		t.Fatalf("CheckRetrieval top 3 error = %v", err)
	}
	if !missesInclude(strict.Misses, "c-2026-0101") {
		t.Fatal("a fourth-ranked self hit must miss the top 3")
	}

	loose, err := memcheck.CheckRetrieval(
		context.Background(),
		"testdata",
		searchingFake{hits: hits, honorTopK: true},
		memcheck.RetrievalOptions{Sample: 12, TopK: 4},
	)
	if err != nil {
		t.Fatalf("CheckRetrieval top 4 error = %v", err)
	}
	if missesInclude(loose.Misses, "c-2026-0101") {
		t.Fatal("the same hit ranks inside the top 4 and must be covered")
	}
}

// TestCheckRetrievalSampleStride pins the deterministic sample: from more
// claims than the cap allows, exactly the cap's worth of evenly strided
// claims are checked, and the same run always checks the same claims.
func TestCheckRetrievalSampleStride(t *testing.T) {
	options := memcheck.RetrievalOptions{Sample: 5, TopK: 3}
	first, err := runSampleSearch("testdata", options)
	if err != nil {
		t.Fatalf("first run error = %v", err)
	}
	second, err := runSampleSearch("testdata", options)
	if err != nil {
		t.Fatalf("second run error = %v", err)
	}
	if first.sampled != 5 {
		t.Fatalf("sampled = %d, want the cap of 5", first.sampled)
	}
	wantIDs := []string{"c-2026-0101", "c-2026-0201", "c-2026-0301", "c-2026-0303", "c-2026-0305"}
	if len(first.misses) != len(wantIDs) || len(second.misses) != len(wantIDs) {
		t.Fatalf(
			"first/second sample sizes=%d/%d; want %d",
			len(first.misses),
			len(second.misses),
			len(wantIDs),
		)
	}
	for i, id := range wantIDs {
		if first.misses[i].ClaimID != id {
			t.Fatalf("sample %d = %s, want stride pick %s", i, first.misses[i].ClaimID, id)
		}
	}
	for i := range first.misses {
		if first.misses[i].ClaimID != second.misses[i].ClaimID {
			t.Fatalf("rerun sampled %s where the first run sampled %s",
				second.misses[i].ClaimID, first.misses[i].ClaimID)
		}
	}
}

// runSampleSearch returns the report plus the claimed misses, which double as
// the ordered sample listing in the all-junk fake.
func runSampleSearch(
	root string,
	options memcheck.RetrievalOptions,
) (sampleResult, error) {
	junk := memcheck.SemanticHit{
		ID:       "junk",
		FilePath: "/fixture/semantic/none.md",
		Text:     "no claims",
	}
	report, err := memcheck.CheckRetrieval(
		context.Background(),
		root,
		searchingFake{hits: []memcheck.SemanticHit{junk}},
		options,
	)
	if err != nil {
		return sampleResult{}, err
	}
	return sampleResult{sampled: report.Sampled, misses: report.Misses}, nil
}

type sampleResult struct {
	sampled int
	misses  []memcheck.RetrievalMiss
}

func missesInclude(misses []memcheck.RetrievalMiss, claimID string) bool {
	for _, miss := range misses {
		if miss.ClaimID == claimID {
			return true
		}
	}
	return false
}

// TestCheckRetrievalEmptyRoot: no claims is an empty sample, not an error.
func TestCheckRetrievalEmptyRoot(t *testing.T) {
	report, err := memcheck.CheckRetrieval(
		context.Background(),
		t.TempDir(),
		fixtureSearcher{},
		memcheck.DefaultRetrievalOptions(),
	)
	if err != nil {
		t.Fatalf("CheckRetrieval empty root error = %v", err)
	}
	if report.Sampled != 0 || !report.OK() {
		t.Fatalf("empty root report = %+v, want an empty clean sample", report)
	}
}

// TestCheckRetrievalErrors pins the two named refusals: a nil searcher and a
// searcher that fails mid-check.
func TestCheckRetrievalErrors(t *testing.T) {
	if _, err := memcheck.CheckRetrieval(
		context.Background(), "testdata", nil, memcheck.DefaultRetrievalOptions(),
	); err == nil {
		t.Fatal("nil searcher must be refused")
	}
	searchErr := errors.New("collection down")
	_, err := memcheck.CheckRetrieval(
		context.Background(),
		"testdata",
		searchingFake{err: searchErr},
		memcheck.RetrievalOptions{Sample: 1, TopK: 3},
	)
	if err == nil || !strings.Contains(err.Error(), "search semantic collection for claim") {
		t.Fatalf("search failure must be wrapped with claim: %v", err)
	}
	if !errors.Is(err, searchErr) {
		t.Fatalf("wrapped error must keep the cause: %v", err)
	}
}

// TestCheckRetrievalMissingRootErrors refuses to measure a path that is not a
// memory root.
func TestCheckRetrievalMissingRootErrors(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-a-root")
	_, err := memcheck.CheckRetrieval(
		context.Background(), missing, fixtureSearcher{}, memcheck.DefaultRetrievalOptions(),
	)
	if err == nil {
		t.Fatal("CheckRetrieval on a missing root succeeded, want error")
	}
}

// TestSearchFuncNilGuard pins the adapter's own refusal.
func TestSearchFuncNilGuard(t *testing.T) {
	var fn memcheck.SearchFunc
	if _, err := fn.SearchSemantic(context.Background(), "text", 3); err == nil {
		t.Fatal("nil SearchFunc must be refused")
	}
}

// TestQdrantSemanticRetrievalIntegration is the live §7.5 run. It follows the
// repository's integration convention, the same shape as provider/ollama's
// TestOllamaLocalIntegration: an Integration-named test that skips until its
// environment is set, rather than a build tag (the repository defines none)
// and rather than a Makefile switch (the Makefile has no integration target).
//
// Required environment:
//   - Q15_QDRANT_INTEGRATION_URL: the Qdrant gRPC endpoint, e.g.
//     localhost:6334
//   - Q15_QDRANT_INTEGRATION_MEMORY_ROOT: the memory root whose semantic
//     files are indexed in the semantic collection
//
// Optional embedding settings, all defaulting to the embed package's defaults:
//   - Q15_QDRANT_INTEGRATION_EMBED_PROVIDER: gemini (default) or openai
//   - Q15_QDRANT_INTEGRATION_EMBED_MODEL: the embedding model name
//   - Q15_QDRANT_INTEGRATION_EMBED_DIMENSIONS: the vector dimensions
//   - Q15_QDRANT_INTEGRATION_EMBED_API_KEY: the provider key
//   - Q15_QDRANT_INTEGRATION_EMBED_BASE_URL: root for an OpenAI-compatible
//     endpoint such as a local TEI or Ollama /v1
func TestQdrantSemanticRetrievalIntegration(t *testing.T) {
	url := strings.TrimSpace(os.Getenv("Q15_QDRANT_INTEGRATION_URL"))
	root := os.Getenv("Q15_QDRANT_INTEGRATION_MEMORY_ROOT")
	if url == "" || root == "" {
		t.Skip("set Q15_QDRANT_INTEGRATION_URL and Q15_QDRANT_INTEGRATION_MEMORY_ROOT " +
			"to run the live semantic-retrieval integration")
	}

	dimensions := embed.DefaultEmbeddingDimensions
	if raw := strings.TrimSpace(
		os.Getenv("Q15_QDRANT_INTEGRATION_EMBED_DIMENSIONS"),
	); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			t.Fatalf("Q15_QDRANT_INTEGRATION_EMBED_DIMENSIONS = %q: %v", raw, err)
		}
		dimensions = parsed
	}
	settings := embed.Settings{
		QdrantURL:    url,
		Model:        os.Getenv("Q15_QDRANT_INTEGRATION_EMBED_MODEL"),
		Dimensions:   dimensions,
		GeminiAPIKey: os.Getenv("Q15_QDRANT_INTEGRATION_EMBED_API_KEY"),
		APIKey:       os.Getenv("Q15_QDRANT_INTEGRATION_EMBED_API_KEY"),
		BaseURL:      os.Getenv("Q15_QDRANT_INTEGRATION_EMBED_BASE_URL"),
	}
	if provider := strings.TrimSpace(
		os.Getenv("Q15_QDRANT_INTEGRATION_EMBED_PROVIDER"),
	); provider != "" {
		settings.Provider = provider
	}

	ctx := context.Background()
	embedder, err := embed.NewEmbedder(ctx, settings)
	if err != nil {
		t.Fatalf("build embedder: %v", err)
	}
	vectors, err := embed.NewQdrantStore(settings.QdrantURL, "", settings.Dimensions)
	if err != nil {
		t.Fatalf("build Qdrant store: %v", err)
	}
	defer vectors.Close()

	searcher := memcheck.SearchFunc(func(
		ctx context.Context,
		query string,
		topK int,
	) ([]memcheck.SemanticHit, error) {
		vector, err := embedder.EmbedQuery(ctx, query)
		if err != nil {
			return nil, fmt.Errorf("embed query: %w", err)
		}
		found, err := vectors.Search(ctx, embed.CollectionSemantic, embed.SearchRequest{
			Vector: vector,
			Mode:   embed.SearchModeDense,
			Limit:  topK,
		})
		if err != nil {
			return nil, fmt.Errorf("search: %w", err)
		}
		hits := make([]memcheck.SemanticHit, 0, len(found))
		for _, result := range found {
			hits = append(hits, memcheck.SemanticHit{
				ID:       result.ID,
				Score:    float64(result.Score),
				FilePath: payloadString(result.Payload, "file_path"),
				Text:     payloadString(result.Payload, "text"),
			})
		}
		return hits, nil
	})

	report, err := memcheck.CheckRetrieval(ctx, root, searcher, memcheck.DefaultRetrievalOptions())
	if err != nil {
		t.Fatalf("CheckRetrieval error = %v", err)
	}
	if !report.OK() {
		var named []string
		for _, miss := range report.Misses {
			named = append(named, fmt.Sprintf("%s in %s (hits: %s)",
				miss.ClaimID, miss.File, hitIDs(miss.Hits)))
		}
		t.Fatalf("%d of %d sampled claims missed the top %d:\n%s",
			len(report.Misses), report.Sampled, report.TopK, strings.Join(named, "\n"))
	}
}

func payloadString(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return value
}

func hitIDs(hits []memcheck.SemanticHit) string {
	ids := make([]string, 0, len(hits))
	for _, hit := range hits {
		ids = append(ids, hit.ID)
	}
	return strings.Join(ids, ", ")
}
