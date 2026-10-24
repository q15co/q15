package history

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pgvector/pgvector-go"
	"github.com/q15co/q15/systems/agent/internal/history/historydb"
)

// SearchOptions bounds retrieval and excludes turns already present in the prompt.
type SearchOptions struct {
	Limit          int
	ExcludeTurnIDs []int64
	MinSimilarity  float32
	Mode           string
}

// SearchHit identifies whether the returned text is a digest or a source excerpt.
type SearchHit struct {
	TurnID     int64     `json:"turn_id"`
	UnitID     int64     `json:"unit_id"`
	Kind       string    `json:"kind"`
	CreatedAt  time.Time `json:"created_at"`
	Text       string    `json:"text"`
	RankScore  float64   `json:"rank_score"`
	Similarity *float32  `json:"cosine_similarity,omitempty"`
}

// SearchResult reports embedding degradation alongside the available evidence.
type SearchResult struct {
	Hits            []SearchHit `json:"hits"`
	LexicalFallback bool        `json:"lexical_fallback"`
}

// Search ranks summaries and original-message chunks in the configured vector space.
func (i *Indexer) Search(
	ctx context.Context,
	query string,
	options SearchOptions,
) (SearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return SearchResult{}, errors.New("history search query is required")
	}
	if options.Limit <= 0 {
		options.Limit = 6
	}
	options.Limit = min(options.Limit, 32)
	if options.Mode == "" {
		options.Mode = "hybrid"
	}
	if options.Mode != "hybrid" && options.Mode != "dense" && options.Mode != "lexical" {
		return SearchResult{}, errors.New("history search mode must be hybrid, dense or lexical")
	}
	if options.ExcludeTurnIDs == nil {
		options.ExcludeTurnIDs = []int64{}
	}
	var embedding []float32
	var embedErr error
	if options.Mode != "lexical" {
		embedCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		embedding, embedErr = i.embedder.EmbedQuery(embedCtx, i.queryText(query))
		cancel()
		if embedErr == nil {
			embedErr = validateVector(embedding, i.space.Dimensions)
		}
		if ctx.Err() != nil {
			return SearchResult{}, ctx.Err()
		}
		if embedErr != nil && options.Mode == "dense" {
			return SearchResult{}, errors.New("history query embedding is unavailable")
		}
	}
	tx, err := i.store.pool.BeginTx(
		ctx,
		pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
	)
	if err != nil {
		return SearchResult{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := historydb.New(tx)
	var dense, lexical []historydb.SearchDenseRow
	if embedErr == nil && options.Mode != "lexical" {
		if _, err := tx.Exec(ctx, "SET LOCAL hnsw.iterative_scan = 'strict_order'"); err != nil {
			return SearchResult{}, err
		}
		dense, err = i.denseSearch(ctx, q, embedding, options)
		if err != nil {
			return SearchResult{}, err
		}
	}
	if options.Mode != "dense" {
		rows, err := q.SearchLexical(
			ctx,
			historydb.SearchLexicalParams{
				Query:          query,
				AgentID:        i.store.agentID,
				ExcludeTurnIds: options.ExcludeTurnIDs,
				ResultLimit:    int32(options.Limit * 4),
			},
		)
		if err != nil {
			return SearchResult{}, err
		}
		for _, row := range rows {
			lexical = append(lexical, historydb.SearchDenseRow(row))
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return SearchResult{}, err
	}
	return SearchResult{
		Hits:            fuseHits(dense, lexical, options),
		LexicalFallback: embedErr != nil,
	}, nil
}

func (i *Indexer) denseSearch(
	ctx context.Context,
	q *historydb.Queries,
	embedding []float32,
	options SearchOptions,
) ([]historydb.SearchDenseRow, error) {
	params := historydb.SearchDenseParams{
		QueryEmbedding: pgvector.NewVector(embedding).String(),
		AgentID:        i.store.agentID,
		ModelID:        i.modelID,
		ExcludeTurnIds: options.ExcludeTurnIDs,
		ResultLimit:    int32(options.Limit * 4),
	}
	var results []historydb.SearchDenseRow
	switch i.space.Dimensions {
	case 768:
		rows, err := q.SearchDense768(ctx, historydb.SearchDense768Params(params))
		for _, row := range rows {
			results = append(results, historydb.SearchDenseRow(row))
		}
		return results, err
	case 1024:
		rows, err := q.SearchDense1024(ctx, historydb.SearchDense1024Params(params))
		for _, row := range rows {
			results = append(results, historydb.SearchDenseRow(row))
		}
		return results, err
	default:
		return q.SearchDense(ctx, params)
	}
}

func fuseHits(dense, lexical []historydb.SearchDenseRow, options SearchOptions) []SearchHit {
	units := make(map[string]*SearchHit)
	add := func(row historydb.SearchDenseRow, rank int, semantic bool) {
		if semantic && row.Score < options.MinSimilarity {
			return
		}
		key := fmt.Sprintf("%s:%d", row.Kind, row.UnitID)
		hit := units[key]
		if hit == nil {
			hit = &SearchHit{
				TurnID:    row.TurnID,
				UnitID:    row.UnitID,
				Kind:      row.Kind,
				CreatedAt: row.CreatedAt.Time,
				Text:      row.Body,
			}
			units[key] = hit
		}
		hit.RankScore += 1 / float64(60+rank+1)
		if semantic {
			score := row.Score
			hit.Similarity = &score
		}
	}
	for rank, row := range dense {
		add(row, rank, true)
	}
	for rank, row := range lexical {
		add(row, rank, false)
	}
	ordered := make([]SearchHit, 0, len(units))
	for _, hit := range units {
		ordered = append(ordered, *hit)
	}
	slices.SortFunc(ordered, func(a, b SearchHit) int {
		if a.RankScore > b.RankScore {
			return -1
		}
		if a.RankScore < b.RankScore {
			return 1
		}
		if a.TurnID > b.TurnID {
			return -1
		}
		if a.TurnID < b.TurnID {
			return 1
		}
		if kind := strings.Compare(a.Kind, b.Kind); kind != 0 {
			return kind
		}
		if a.UnitID < b.UnitID {
			return -1
		}
		if a.UnitID > b.UnitID {
			return 1
		}
		return 0
	})
	seen := make(map[int64]bool)
	hits := make([]SearchHit, 0, options.Limit)
	for _, hit := range ordered {
		if seen[hit.TurnID] {
			continue
		}
		seen[hit.TurnID] = true
		hits = append(hits, hit)
		if len(hits) >= options.Limit {
			break
		}
	}
	return hits
}

// IndexStatus reports source coverage in the active embedding space.
type IndexStatus struct {
	Turns           int64 `json:"turns"`
	Digests         int64 `json:"digests"`
	Chunks          int64 `json:"chunks"`
	EmbeddedDigests int64 `json:"embedded_digests"`
	EmbeddedChunks  int64 `json:"embedded_chunks"`
}

// Status returns coverage counts without reading private source content.
func (i *Indexer) Status(ctx context.Context) (IndexStatus, error) {
	row, err := historydb.New(i.store.pool).
		ReadIndexStatus(ctx, historydb.ReadIndexStatusParams{AgentID: i.store.agentID, ModelID: i.modelID})
	return IndexStatus{
		Turns:           row.Turns,
		Digests:         row.Digests,
		Chunks:          row.Chunks,
		EmbeddedDigests: row.EmbeddedDigests,
		EmbeddedChunks:  row.EmbeddedChunks,
	}, err
}
