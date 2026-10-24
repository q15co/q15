package history

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/pgvector/pgvector-go"
	"github.com/q15co/q15/systems/agent/internal/embed"
	"github.com/q15co/q15/systems/agent/internal/history/historydb"
)

const chunkerVersion = "utf8-4096-overlap256-v1"

// EmbeddingSpace identifies a vector space, including its input formatting.
type EmbeddingSpace struct {
	Provider      string
	Model         string
	Dimensions    int
	PrefixVersion string
}

// Indexer maintains rebuildable search data separately from immutable history.
type Indexer struct {
	store         *Store
	embedder      embed.Embedder
	space         EmbeddingSpace
	modelID       int64
	digester      DigestGenerator
	digestWorkers int
}

// NewIndexer binds an explicit embedding space and optional digest generator.
func NewIndexer(
	ctx context.Context,
	store *Store,
	space EmbeddingSpace,
	embedder embed.Embedder,
	digester DigestGenerator,
) (*Indexer, error) {
	if store == nil || embedder == nil || space.Provider == "" || space.Model == "" ||
		space.Dimensions <= 0 ||
		space.Dimensions > 2000 {
		return nil, errors.New(
			"history indexing requires a store, embedder and explicit model space",
		)
	}
	if space.PrefixVersion == "" {
		space.PrefixVersion = "plain-v1"
		if strings.HasPrefix(space.Model, "embeddinggemma-2") {
			space.PrefixVersion = "embeddinggemma-2-search-v1"
		}
	}
	if space.PrefixVersion != "plain-v1" && space.PrefixVersion != "embeddinggemma-2-search-v1" {
		return nil, errors.New("unsupported history embedding prefix version")
	}
	id, err := historydb.New(store.pool).
		EnsureEmbeddingModel(ctx, historydb.EnsureEmbeddingModelParams{Provider: space.Provider, Model: space.Model, Dimensions: int32(space.Dimensions), PrefixVersion: space.PrefixVersion})
	if err != nil {
		return nil, err
	}
	return &Indexer{
		store:         store,
		space:         space,
		modelID:       id,
		embedder:      embedder,
		digester:      digester,
		digestWorkers: 1,
	}, nil
}

// SetDigestConcurrency bounds independent summary requests before indexing starts.
func (i *Indexer) SetDigestConcurrency(workers int) error {
	if workers < 1 || workers > 8 {
		return errors.New("summary concurrency must be between 1 and 8")
	}
	i.digestWorkers = workers
	return nil
}

// Chunk indexes a bounded batch of source parts for lexical search.
func (i *Indexer) Chunk(ctx context.Context, limit int) (int, error) {
	parts, err := historydb.New(i.store.pool).
		PendingChunkParts(ctx, historydb.PendingChunkPartsParams{AgentID: i.store.agentID, ChunkerVersion: chunkerVersion, BatchLimit: int32(batchLimit(limit))})
	if err != nil {
		return 0, err
	}
	for n, part := range parts {
		if err := i.chunkPart(ctx, part); err != nil {
			return n, err
		}
	}
	return len(parts), nil
}

func (i *Indexer) chunkPart(ctx context.Context, part historydb.PendingChunkPartsRow) error {
	body := joinText(part.TextBody, part.RawTextBody)
	if part.Kind == historydb.MemoryPartKindToolResult {
		body = joinText(part.ResultBody, part.RawResultBody)
	}
	if part.Kind == historydb.MemoryPartKindToolCall {
		arguments := part.Arguments
		if part.RawArguments != nil {
			arguments = part.RawArguments
		}
		body = value(part.ToolName) + "\n" + string(arguments)
	}
	tx, err := i.store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := historydb.New(tx)
	locked, err := q.LockChunkPart(ctx, part.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if locked.ChunkerVersion == chunkerVersion {
		return nil
	}
	if err := q.DeleteChunkRun(ctx, part.ID); err != nil {
		return err
	}
	runID, err := q.InsertChunkRun(
		ctx,
		historydb.InsertChunkRunParams{PartID: part.ID, ChunkerVersion: chunkerVersion},
	)
	if err != nil {
		return err
	}
	for position, chunk := range chunkText(body, 4096, 256) {
		if _, err := q.InsertChunk(ctx, historydb.InsertChunkParams{RunID: runID, Position: int32(position), Body: chunk}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

type embeddingUnit struct {
	id     int64
	body   string
	digest bool
}

// Embed fills missing vectors without repeating already completed model/unit pairs.
func (i *Indexer) Embed(ctx context.Context, limit int) (int, error) {
	q := historydb.New(i.store.pool)
	limit = batchLimit(limit)
	digests, err := q.PendingDigestEmbeddings(
		ctx,
		historydb.PendingDigestEmbeddingsParams{
			AgentID:    i.store.agentID,
			ModelID:    i.modelID,
			BatchLimit: int32(limit),
		},
	)
	if err != nil {
		return 0, err
	}
	chunks, err := q.PendingChunkEmbeddings(
		ctx,
		historydb.PendingChunkEmbeddingsParams{
			AgentID:    i.store.agentID,
			ModelID:    i.modelID,
			BatchLimit: int32(limit - len(digests)),
		},
	)
	if err != nil {
		return 0, err
	}
	units := make([]embeddingUnit, 0, len(digests)+len(chunks))
	for _, digest := range digests {
		units = append(units, embeddingUnit{id: digest.TurnID, body: digest.Body, digest: true})
	}
	for _, chunk := range chunks {
		units = append(units, embeddingUnit{id: chunk.ID, body: chunk.Body})
	}
	if len(units) == 0 {
		return 0, nil
	}
	requests := make([]embed.EmbeddingRequest, len(units))
	for n, unit := range units {
		requests[n].Text = i.documentText(unit.body)
	}
	vectors, err := i.embedder.EmbedDocuments(ctx, requests)
	if err != nil {
		return 0, fmt.Errorf("history document embeddings failed: %w", err)
	}
	if len(vectors) != len(units) {
		return 0, errors.New("history embedder returned the wrong number of vectors")
	}
	return i.saveEmbeddings(ctx, units, vectors)
}

func (i *Indexer) saveEmbeddings(
	ctx context.Context,
	units []embeddingUnit,
	vectors [][]float32,
) (int, error) {
	tx, err := i.store.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := historydb.New(tx)
	for n, unit := range units {
		if err := validateVector(vectors[n], i.space.Dimensions); err != nil {
			return 0, err
		}
		vector := pgvector.NewVector(vectors[n]).String()
		if unit.digest {
			err = q.InsertDigestEmbedding(
				ctx,
				historydb.InsertDigestEmbeddingParams{
					TurnID:    unit.id,
					ModelID:   i.modelID,
					Embedding: vector,
				},
			)
		} else {
			err = q.InsertChunkEmbedding(ctx, historydb.InsertChunkEmbeddingParams{ChunkID: unit.id, ModelID: i.modelID, Embedding: vector})
		}
		if err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(units), nil
}

func (i *Indexer) documentText(body string) string {
	if i.space.PrefixVersion == "embeddinggemma-2-search-v1" {
		return "title: conversation history | text: " + body
	}
	return body
}

func (i *Indexer) queryText(body string) string {
	if i.space.PrefixVersion == "embeddinggemma-2-search-v1" {
		return "task: search result | query: " + body
	}
	return body
}

func validateVector(vector []float32, dimensions int) error {
	if len(vector) != dimensions {
		return fmt.Errorf(
			"history embedding has %d dimensions; expected %d",
			len(vector),
			dimensions,
		)
	}
	var norm float64
	for _, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return errors.New("history embedding contains a non-finite value")
		}
		norm += float64(value) * float64(value)
	}
	if norm == 0 {
		return errors.New("history embedding is a zero vector")
	}
	return nil
}

func batchLimit(limit int) int {
	if limit <= 0 {
		return 16
	}
	return min(limit, 128)
}

func chunkText(body string, limit, overlap int) []string {
	body = strings.ToValidUTF8(strings.ReplaceAll(body, "\x00", " "), "�")
	var chunks []string
	for start := 0; start < len(body); {
		end := min(start+limit, len(body))
		for end < len(body) && !utf8.RuneStart(body[end]) {
			end--
		}
		if end < len(body) {
			if newline := strings.LastIndexByte(body[start+limit/2:end], '\n'); newline >= 0 {
				end = start + limit/2 + newline + 1
			}
		}
		if chunk := strings.TrimSpace(body[start:end]); chunk != "" {
			chunks = append(chunks, chunk)
		}
		if end == len(body) {
			break
		}
		start = max(start+1, end-overlap)
		for start < end && !utf8.RuneStart(body[start]) {
			start++
		}
	}
	return chunks
}
