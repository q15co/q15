// Package embed owns q15's typed ingestion source registry, document scanning,
// embedding sync state, and vector-store orchestration.
package embed

import (
	"context"
	"time"
)

// Default embedding storage paths and vector settings.
const (
	DefaultRegistryRelativePath = ".q15/embed/sources.json"
	DefaultStateRelativePath    = ".q15/embed/state.jsonl"

	DefaultEmbeddingModel      = "gemini-embedding-2"
	DefaultEmbeddingDimensions = 768

	VectorNameDense  = "dense"
	VectorNameSparse = "sparse"
	SparseModelBM25  = "qdrant/bm25"
)

// Supported vector search modes.
const (
	SearchModeDense  = "dense"
	SearchModeSparse = "sparse"
	SearchModeHybrid = "hybrid"
)

// Supported q15 vector collection names.
const (
	CollectionLibrary      = "library"
	CollectionZettelkasten = "zettelkasten"
	CollectionSemantic     = "semantic"
	CollectionCore         = "core"
)

// Supported typed source scanner names.
const (
	SourceTypeMarkdownTree        = "markdown_tree"
	SourceTypeMarkdownFile        = "markdown_file"
	SourceTypeChunkedMarkdownTree = "chunked_markdown_tree"
)

// Settings describes the runtime-local paths and external embedding/vector
// dependencies used by the embedding service.
type Settings struct {
	WorkspaceLocalDir string
	MemoryLocalDir    string
	SkillsLocalDir    string

	RegistryPath string
	StatePath    string

	QdrantURL    string
	GeminiAPIKey string
	Model        string
	Dimensions   int
}

// Source defines one typed ingestion input. Collection chooses where points are
// stored; SourceType chooses how files under Path are scanned and parsed.
type Source struct {
	ID           string   `json:"id"`
	Collection   string   `json:"collection"`
	SourceType   string   `json:"source_type"`
	Path         string   `json:"path"`
	IncludeGlobs []string `json:"include_globs,omitempty"`
	ExcludeGlobs []string `json:"exclude_globs,omitempty"`
	MetadataPath string   `json:"metadata_path,omitempty"`
	Enabled      bool     `json:"enabled"`
}

// RegistryFile is the persisted source registry shape.
type RegistryFile struct {
	Version int      `json:"version"`
	Sources []Source `json:"sources"`
}

// Document is one embedding unit produced by a typed scanner.
type Document struct {
	Collection  string
	SourceID    string
	SourceType  string
	Path        string
	Identity    string
	Text        string
	ContentHash string
	Payload     map[string]any
}

// Point is the vector-store representation of one embedded document.
type Point struct {
	ID         string
	Vector     []float32
	SparseText string
	Payload    map[string]any
}

// SearchResult is a vector-store search hit with normalized payload.
type SearchResult struct {
	Collection string         `json:"collection"`
	ID         string         `json:"id"`
	Score      float32        `json:"score"`
	Payload    map[string]any `json:"payload,omitempty"`
}

// CollectionStatus summarizes one vector collection.
type CollectionStatus struct {
	Collection string `json:"collection"`
	Exists     bool   `json:"exists"`
	Points     uint64 `json:"points"`
	Dimensions int    `json:"dimensions"`
}

// CollectionDeleteResult summarizes a deliberate collection reset. The next
// embed_sync recreates deleted collections from configured sources.
type CollectionDeleteResult struct {
	Collection    string `json:"collection"`
	Deleted       bool   `json:"deleted"`
	StatePoints   int    `json:"state_points"`
	StateSyncRuns int    `json:"state_sync_runs"`
}

// CollectionEnsureResult reports whether EnsureCollection had to create or
// recreate storage. Recreated collections require state invalidation.
type CollectionEnsureResult struct {
	Created   bool
	Recreated bool
}

// SyncResult summarizes one embed_sync run.
type SyncResult struct {
	Scanned   int `json:"scanned"`
	Embedded  int `json:"embedded"`
	Upserted  int `json:"upserted"`
	Pruned    int `json:"pruned"`
	Unchanged int `json:"unchanged"`
}

// SourceStatus describes persisted state for one configured source.
type SourceStatus struct {
	SourceID   string    `json:"source_id"`
	Collection string    `json:"collection"`
	Points     int       `json:"points"`
	LastSynced time.Time `json:"last_synced,omitempty"`
}

// Embedder turns text into embedding vectors.
type Embedder interface {
	EmbedDocuments(ctx context.Context, reqs []EmbeddingRequest) ([][]float32, error)
	EmbedQuery(ctx context.Context, text string) ([]float32, error)
}

// EmbeddingRequest is one document embedding input.
type EmbeddingRequest struct {
	Text  string
	Title string
}

// SearchRequest carries dense vectors and/or sparse query text into the vector
// store. Qdrant generates BM25 sparse vectors from SparseText.
type SearchRequest struct {
	Vector     []float32
	SparseText string
	Filter     map[string]any
	Mode       string
	Limit      int
}

// VectorStore hides the concrete Qdrant client behind the sync/search behavior
// the service needs, keeping source scanning independent from storage.
type VectorStore interface {
	EnsureCollection(
		ctx context.Context,
		collection string,
		dimensions int,
	) (CollectionEnsureResult, error)
	DeleteCollection(ctx context.Context, collection string) (bool, error)
	Upsert(ctx context.Context, collection string, points []Point) error
	UpdatePayload(
		ctx context.Context,
		collection string,
		pointID string,
		payload map[string]any,
	) error
	Delete(ctx context.Context, collection string, pointIDs []string) error
	Search(
		ctx context.Context,
		collection string,
		req SearchRequest,
	) ([]SearchResult, error)
	Status(ctx context.Context, collection string) (CollectionStatus, error)
	Close() error
}
