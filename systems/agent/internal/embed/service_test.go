package embed

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestServiceSyncTracksChangedUnchangedAndDeletedDocuments(t *testing.T) {
	ctx := context.Background()
	settings := testSettings(t)
	sourceDir := filepath.Join(settings.WorkspaceLocalDir, "docs")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatalf("create docs: %v", err)
	}
	notePath := filepath.Join(sourceDir, "note.md")
	if err := os.WriteFile(notePath, []byte("first body\n"), 0o644); err != nil {
		t.Fatalf("write note: %v", err)
	}
	state, err := OpenState(ctx, settings)
	if err != nil {
		t.Fatalf("OpenState() error = %v", err)
	}
	defer state.Close()
	vectors := newFakeVectorStore()
	service := NewService(settings, state, vectors, fakeEmbedder{})
	if _, err := service.AddSource(ctx, Source{
		ID:         "docs",
		Collection: CollectionSemantic,
		SourceType: SourceTypeMarkdownTree,
		Path:       "/workspace/docs",
		Enabled:    true,
	}); err != nil {
		t.Fatalf("AddSource() error = %v", err)
	}

	result, err := service.Sync(ctx, SyncOptions{SourceID: "docs"})
	if err != nil {
		t.Fatalf("first Sync() error = %v", err)
	}
	if want := (SyncResult{Scanned: 1, Embedded: 1, Upserted: 1}); !reflect.DeepEqual(
		result,
		want,
	) {
		t.Fatalf("first Sync() = %#v, want %#v", result, want)
	}

	result, err = service.Sync(ctx, SyncOptions{SourceID: "docs"})
	if err != nil {
		t.Fatalf("second Sync() error = %v", err)
	}
	if want := (SyncResult{Scanned: 1, Unchanged: 1}); !reflect.DeepEqual(result, want) {
		t.Fatalf("second Sync() = %#v, want %#v", result, want)
	}

	if err := os.WriteFile(notePath, []byte("changed body\n"), 0o644); err != nil {
		t.Fatalf("rewrite note: %v", err)
	}
	result, err = service.Sync(ctx, SyncOptions{SourceID: "docs"})
	if err != nil {
		t.Fatalf("changed Sync() error = %v", err)
	}
	if want := (SyncResult{Scanned: 1, Embedded: 1, Upserted: 1}); !reflect.DeepEqual(
		result,
		want,
	) {
		t.Fatalf("changed Sync() = %#v, want %#v", result, want)
	}

	if err := os.Remove(notePath); err != nil {
		t.Fatalf("remove note: %v", err)
	}
	result, err = service.Sync(ctx, SyncOptions{SourceID: "docs"})
	if err != nil {
		t.Fatalf("deleted Sync() error = %v", err)
	}
	if want := (SyncResult{Scanned: 0, Pruned: 1}); !reflect.DeepEqual(result, want) {
		t.Fatalf("deleted Sync() = %#v, want %#v", result, want)
	}
	if len(vectors.points[CollectionSemantic]) != 0 {
		t.Fatalf("vector points after prune = %#v, want none", vectors.points[CollectionSemantic])
	}
	status, err := service.Status(ctx, CollectionSemantic)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if len(status.SourceState) != 1 {
		t.Fatalf("SourceState len = %d, want 1", len(status.SourceState))
	}
	if status.SourceState[0].Points != 0 || status.SourceState[0].LastSynced.IsZero() {
		t.Fatalf(
			"SourceState after prune = %#v, want zero points with last sync",
			status.SourceState[0],
		)
	}
}

func TestServiceSyncPrunesRemovedSources(t *testing.T) {
	ctx := context.Background()
	settings := testSettings(t)
	sourceDir := filepath.Join(settings.WorkspaceLocalDir, "docs")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatalf("create docs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "note.md"), []byte("body\n"), 0o644); err != nil {
		t.Fatalf("write note: %v", err)
	}
	state, err := OpenState(ctx, settings)
	if err != nil {
		t.Fatalf("OpenState() error = %v", err)
	}
	defer state.Close()
	vectors := newFakeVectorStore()
	service := NewService(settings, state, vectors, fakeEmbedder{})
	if _, err := service.AddSource(ctx, Source{
		ID:         "docs",
		Collection: CollectionSemantic,
		SourceType: SourceTypeMarkdownTree,
		Path:       "/workspace/docs",
		Enabled:    true,
	}); err != nil {
		t.Fatalf("AddSource() error = %v", err)
	}
	if _, err := service.Sync(ctx, SyncOptions{SourceID: "docs"}); err != nil {
		t.Fatalf("initial Sync() error = %v", err)
	}
	if _, err := service.RemoveSource(ctx, "docs"); err != nil {
		t.Fatalf("RemoveSource() error = %v", err)
	}

	result, err := service.Sync(ctx, SyncOptions{})
	if err != nil {
		t.Fatalf("Sync() after remove error = %v", err)
	}
	if result.Pruned != 1 {
		t.Fatalf("Pruned = %d, want 1", result.Pruned)
	}
	if len(vectors.points[CollectionSemantic]) != 0 {
		t.Fatalf(
			"vector points after source remove = %#v, want none",
			vectors.points[CollectionSemantic],
		)
	}
}

func TestServiceSearchRejectsUnsupportedModes(t *testing.T) {
	service := NewService(testSettings(t), nil, newFakeVectorStore(), fakeEmbedder{})
	_, err := service.Search(context.Background(), SearchOptions{
		Query: "hello",
		Mode:  "rerank",
	})
	if err == nil ||
		err.Error() != `search mode "rerank" is not supported; use dense, sparse, or hybrid` {
		t.Fatalf("Search() error = %v, want unsupported mode error", err)
	}
}

func TestServiceSearchSkipsMissingCollectionsWhenUnscoped(t *testing.T) {
	vectors := newFakeVectorStore()
	vectors.points[CollectionCore] = map[string]Point{}
	vectors.points[CollectionSemantic] = map[string]Point{}
	vectors.searchResults[CollectionCore] = []SearchResult{
		{Collection: CollectionCore, ID: "core-low", Score: 0.4},
	}
	vectors.searchResults[CollectionSemantic] = []SearchResult{
		{Collection: CollectionSemantic, ID: "semantic-high", Score: 0.9},
		{Collection: CollectionSemantic, ID: "semantic-low", Score: 0.2},
	}
	service := NewService(testSettings(t), nil, vectors, fakeEmbedder{})

	results, err := service.Search(context.Background(), SearchOptions{
		Query: "hello",
		Limit: 2,
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if got, want := len(results), 2; got != want {
		t.Fatalf("results len = %d, want %d", got, want)
	}
	if results[0].ID != "semantic-high" || results[1].ID != "core-low" {
		t.Fatalf("results = %#v, want globally sorted top two hits", results)
	}
	if _, searched := vectors.searchRequests[CollectionLibrary]; searched {
		t.Fatal("library collection was searched even though it does not exist")
	}
}

func TestServiceSearchReturnsExplicitMissingCollectionError(t *testing.T) {
	vectors := newFakeVectorStore()
	vectors.searchErrs[CollectionLibrary] = errors.New("collection library doesn't exist")
	service := NewService(testSettings(t), nil, vectors, fakeEmbedder{})

	_, err := service.Search(context.Background(), SearchOptions{
		Query:      "hello",
		Collection: CollectionLibrary,
	})
	if err == nil || err.Error() != "collection library doesn't exist" {
		t.Fatalf("Search() error = %v, want explicit missing collection error", err)
	}
}

type fakeEmbedder struct{}

func (fakeEmbedder) EmbedDocuments(
	ctx context.Context,
	reqs []EmbeddingRequest,
) ([][]float32, error) {
	_ = ctx
	out := make([][]float32, 0, len(reqs))
	for _, req := range reqs {
		out = append(out, []float32{float32(len(req.Text)), 1})
	}
	return out, nil
}

func (fakeEmbedder) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	_ = ctx
	return []float32{float32(len(text)), 1}, nil
}

type fakeVectorStore struct {
	points         map[string]map[string]Point
	searchErrs     map[string]error
	searchRequests map[string][]SearchRequest
	searchResults  map[string][]SearchResult
}

func newFakeVectorStore() *fakeVectorStore {
	return &fakeVectorStore{
		points:         make(map[string]map[string]Point),
		searchErrs:     make(map[string]error),
		searchRequests: make(map[string][]SearchRequest),
		searchResults:  make(map[string][]SearchResult),
	}
}

func (f *fakeVectorStore) EnsureCollection(
	ctx context.Context,
	collection string,
	dimensions int,
) error {
	_, _ = ctx, dimensions
	if f.points[collection] == nil {
		f.points[collection] = make(map[string]Point)
	}
	return nil
}

func (f *fakeVectorStore) Upsert(ctx context.Context, collection string, points []Point) error {
	_ = ctx
	if f.points[collection] == nil {
		f.points[collection] = make(map[string]Point)
	}
	for _, point := range points {
		f.points[collection][point.ID] = point
	}
	return nil
}

func (f *fakeVectorStore) UpdatePayload(
	ctx context.Context,
	collection string,
	pointID string,
	payload map[string]any,
) error {
	_ = ctx
	point := f.points[collection][pointID]
	point.Payload = payload
	f.points[collection][pointID] = point
	return nil
}

func (f *fakeVectorStore) Delete(
	ctx context.Context,
	collection string,
	pointIDs []string,
) error {
	_ = ctx
	for _, pointID := range pointIDs {
		delete(f.points[collection], pointID)
	}
	return nil
}

func (f *fakeVectorStore) Search(
	ctx context.Context,
	collection string,
	req SearchRequest,
) ([]SearchResult, error) {
	_ = ctx
	f.searchRequests[collection] = append(f.searchRequests[collection], req)
	if err := f.searchErrs[collection]; err != nil {
		return nil, err
	}
	results := append([]SearchResult(nil), f.searchResults[collection]...)
	if req.Limit > 0 && len(results) > req.Limit {
		results = results[:req.Limit]
	}
	return results, nil
}

func (f *fakeVectorStore) Status(
	ctx context.Context,
	collection string,
) (CollectionStatus, error) {
	_ = ctx
	return CollectionStatus{
		Collection: collection,
		Exists:     f.points[collection] != nil,
		Points:     uint64(len(f.points[collection])),
		Dimensions: 2,
	}, nil
}

func (f *fakeVectorStore) Close() error {
	return nil
}
