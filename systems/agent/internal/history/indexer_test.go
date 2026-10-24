package history

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/pgvector/pgvector-go"

	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/embed"
	"github.com/q15co/q15/systems/agent/internal/history/historydb"
)

type testEmbedder struct{ unavailable bool }

func (e *testEmbedder) EmbedDocuments(
	_ context.Context,
	requests []embed.EmbeddingRequest,
) ([][]float32, error) {
	if e.unavailable {
		return nil, errors.New("embedding service unavailable")
	}
	vectors := make([][]float32, len(requests))
	for n, request := range requests {
		switch {
		case strings.Contains(request.Text, "The approved memory cutover uses PostgreSQL transcripts."):
			vectors[n] = []float32{1, 0.001, 0}
		case strings.Contains(request.Text, "PostgreSQL"):
			vectors[n] = []float32{0.9, 0.2, 0}
		default:
			vectors[n] = []float32{0, 1, 0}
		}
	}
	return vectors, nil
}

func (e *testEmbedder) EmbedQuery(context.Context, string) ([]float32, error) {
	if e.unavailable {
		return nil, errors.New("embedding service unavailable")
	}
	return []float32{1, 0, 0}, nil
}

func appendTestTurn(t *testing.T, store *Store, text string) int64 {
	t.Helper()
	id, err := store.ReserveTurnSeq(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendTurnAtSeq(t.Context(), id, []conversation.Message{conversation.UserMessage(text), conversation.AssistantMessage(conversation.Text("Recorded.", conversation.TextDispositionFinal))}); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestIndexRetrievalAndDeletionProvenance(t *testing.T) {
	store := testStore(t)
	first := appendTestTurn(t, store, "We selected PostgreSQL for the memory cutover.")
	second := appendTestTurn(t, store, "The weather is sunny today.")
	embedder := &testEmbedder{}
	generated := 0
	indexer, err := NewIndexer(
		t.Context(),
		store,
		EmbeddingSpace{Provider: "test-local", Model: "fixture", Dimensions: 3},
		embedder,
		DigestFunc(func(_ context.Context, source string) (DigestResult, error) {
			generated++
			target := source[strings.LastIndex(source, "\nTARGET turn "):]
			body := "The weather discussion was recorded."
			if strings.Contains(target, "PostgreSQL") {
				body = "The approved memory cutover uses PostgreSQL transcripts."
			}
			return DigestResult{
				Body:         body,
				Provider:     "ollama-cloud",
				Model:        "fixture-summary",
				InputTokens:  100,
				OutputTokens: 20,
			}, nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := indexer.Chunk(t.Context(), 128); err != nil {
		t.Fatal(err)
	}
	if n, err := indexer.Digest(t.Context(), 128); err != nil || n != 2 {
		t.Fatalf("digest = %d, %v", n, err)
	}
	if _, err := indexer.Embed(t.Context(), 128); err != nil {
		t.Fatal(err)
	}
	if n, err := indexer.Digest(t.Context(), 128); err != nil || n != 0 || generated != 2 {
		t.Fatalf("completed digests repeated: %d, %d, %v", n, generated, err)
	}
	result, err := indexer.Search(
		t.Context(),
		"memory cutover",
		SearchOptions{Mode: "dense", Limit: 2},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Hits) == 0 || result.Hits[0].TurnID != first || result.Hits[0].Kind != "digest" {
		t.Fatalf("planted near-duplicate digest was not first: %+v", result)
	}
	result, err = indexer.Search(
		t.Context(),
		"memory cutover",
		SearchOptions{Mode: "dense", Limit: 2, ExcludeTurnIDs: []int64{first}},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, hit := range result.Hits {
		if hit.TurnID == first {
			t.Fatal("excluded turn was retrieved")
		}
	}
	embedder.unavailable = true
	result, err = indexer.Search(t.Context(), "PostgreSQL", SearchOptions{Limit: 2})
	if err != nil || !result.LexicalFallback || len(result.Hits) != 1 ||
		result.Hits[0].TurnID != first {
		t.Fatalf("lexical fallback: %+v, %v", result, err)
	}
	if err := store.DeleteTurn(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	status, err := indexer.Status(t.Context())
	if err != nil || status.Turns != 1 || status.Digests != 1 || status.EmbeddedDigests != 1 {
		t.Fatalf("delete cascade status: %+v, %v", status, err)
	}
	var contextID *int64
	if err := store.pool.QueryRow(t.Context(), "SELECT context_turn_id FROM memory.digest_context WHERE digest_turn_id=$1", second).Scan(&contextID); err != nil ||
		contextID != nil {
		t.Fatalf("deleted dependency was not nulled: %v, %v", contextID, err)
	}
	var chunks int
	if err := store.pool.QueryRow(t.Context(), "SELECT count(*) FROM memory.chunks c JOIN memory.chunk_runs r ON r.id=c.run_id JOIN memory.parts p ON p.id=r.part_id JOIN memory.messages m ON m.id=p.message_id WHERE m.turn_id=$1", first).Scan(&chunks); err != nil ||
		chunks != 0 {
		t.Fatalf("source chunks survived deletion: %d, %v", chunks, err)
	}
}

func TestDigestFailureDoesNotAdvancePastAnotherTurn(t *testing.T) {
	store := testStore(t)
	first := appendTestTurn(t, store, "failing summary")
	second := appendTestTurn(t, store, "independent summary")
	indexer, err := NewIndexer(
		t.Context(),
		store,
		EmbeddingSpace{Provider: "test", Model: "fixture", Dimensions: 3},
		&testEmbedder{},
		DigestFunc(func(_ context.Context, source string) (DigestResult, error) {
			target := source[strings.LastIndex(source, "\nTARGET turn "):]
			if strings.Contains(target, "failing summary") {
				return DigestResult{}, errors.New("injected generation failure")
			}
			return DigestResult{
				Body:     "Independent turn processed.",
				Provider: "ollama-cloud",
				Model:    "fixture",
			}, nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := indexer.Digest(t.Context(), 16); err == nil || n != 1 {
		t.Fatalf("digest failure result: %d, %v", n, err)
	}
	q := historydb.New(store.pool)
	if _, err := q.ReadDigest(t.Context(), first); err == nil {
		t.Fatal("failed turn received a digest")
	}
	if _, err := q.ReadDigest(t.Context(), second); err != nil {
		t.Fatal("another turn was blocked by the failure", err)
	}
}

func TestChunkTextKeepsUTF8AndBoundedSource(t *testing.T) {
	body := strings.Repeat("Unicode café 日本語🙂\n", 1000)
	for _, chunk := range chunkText(body, 256, 32) {
		if len(chunk) > 256 || strings.Contains(chunk, "�") {
			t.Fatalf("invalid or oversized chunk: %d bytes", len(chunk))
		}
	}
	for _, limit := range []int{0, 8, 64, 1024} {
		if got := boundedText(body, limit); len(got) > limit {
			t.Fatalf("excerpt exceeded %d-byte bound: %d", limit, len(got))
		}
	}
}

type wideEmbedder struct{ dimensions int }

func (e wideEmbedder) EmbedQuery(context.Context, string) ([]float32, error) {
	v := make([]float32, e.dimensions)
	v[0] = 1
	return v, nil
}

func (e wideEmbedder) EmbedDocuments(
	ctx context.Context,
	input []embed.EmbeddingRequest,
) ([][]float32, error) {
	vectors := make([][]float32, len(input))
	for n := range vectors {
		vectors[n], _ = e.EmbedQuery(ctx, input[n].Text)
	}
	return vectors, nil
}

func TestIndexedVectorDimensionsUsePartialHNSWIndexes(t *testing.T) {
	for _, dimensions := range []int{768, 1024} {
		t.Run(fmt.Sprint(dimensions), func(t *testing.T) {
			store := testStore(t)
			id := appendTestTurn(t, store, "PostgreSQL history source")
			indexer, err := NewIndexer(
				t.Context(),
				store,
				EmbeddingSpace{
					Provider:   "fixture-wide",
					Model:      fmt.Sprint(dimensions),
					Dimensions: dimensions,
				},
				wideEmbedder{dimensions: dimensions},
				DigestFunc(func(context.Context, string) (DigestResult, error) {
					return DigestResult{
						Body:     "PostgreSQL history digest",
						Provider: "fixture",
						Model:    "summary",
					}, nil
				}),
			)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := indexer.RunBatch(t.Context(), 32); err != nil {
				t.Fatal(err)
			}
			found, err := indexer.Search(
				t.Context(),
				"history",
				SearchOptions{Mode: "dense", Limit: 1, MinSimilarity: 0.9},
			)
			if err != nil || len(found.Hits) != 1 || found.Hits[0].TurnID != id {
				t.Fatalf("typed dense search: %+v %v", found, err)
			}
			conn, err := store.pool.Acquire(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Release()
			tx, err := conn.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(t.Context()) }()
			if _, err := tx.Exec(t.Context(), "SET LOCAL enable_seqscan=off"); err != nil {
				t.Fatal(err)
			}
			vector, _ := wideEmbedder{dimensions: dimensions}.EmbedQuery(t.Context(), "")
			query := fmt.Sprintf(
				"EXPLAIN SELECT turn_id FROM memory.digest_embeddings WHERE dimensions=%d AND model_id=$1 ORDER BY embedding::vector(%d) <=> $2::vector(%d) LIMIT 1",
				dimensions,
				dimensions,
				dimensions,
			)
			rows, err := tx.Query(
				t.Context(),
				query,
				indexer.modelID,
				pgvector.NewVector(vector).String(),
			)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			var plan strings.Builder
			for rows.Next() {
				var line string
				if err := rows.Scan(&line); err != nil {
					t.Fatal(err)
				}
				plan.WriteString(line)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(plan.String(), fmt.Sprintf("digest_embeddings_%d", dimensions)) {
				t.Fatalf("HNSW expression index was not usable: %s", plan.String())
			}
		})
	}
}
