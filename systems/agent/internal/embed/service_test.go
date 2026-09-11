package embed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
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

func TestServiceSyncRefreshesLegacyVectorVersion(t *testing.T) {
	ctx := context.Background()
	settings := testSettings(t)
	sourceDir := filepath.Join(settings.WorkspaceLocalDir, "docs")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatalf("create docs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "note.md"), []byte("body\n"), 0o644); err != nil {
		t.Fatalf("write note: %v", err)
	}
	source := Source{
		ID:         "docs",
		Collection: CollectionSemantic,
		SourceType: SourceTypeMarkdownTree,
		Path:       "/workspace/docs",
		Enabled:    true,
	}
	docs, err := ScanSource(ctx, settings, source)
	if err != nil {
		t.Fatalf("ScanSource() error = %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("ScanSource() docs = %d, want 1", len(docs))
	}
	state, err := OpenState(ctx, settings)
	if err != nil {
		t.Fatalf("OpenState() error = %v", err)
	}
	defer state.Close()
	if err := state.upsertPoint(ctx, stateRecord{
		PointID:     deterministicPointID(docs[0].Collection, docs[0].SourceID, docs[0].Identity),
		SourceID:    docs[0].SourceID,
		Collection:  docs[0].Collection,
		Path:        docs[0].Path,
		Identity:    docs[0].Identity,
		ContentHash: docs[0].ContentHash,
	}); err != nil {
		t.Fatalf("upsert legacy point state: %v", err)
	}
	vectors := newFakeVectorStore()
	vectors.points[CollectionSemantic] = map[string]Point{
		deterministicPointID(docs[0].Collection, docs[0].SourceID, docs[0].Identity): {},
	}
	service := NewService(settings, state, vectors, fakeEmbedder{})
	if _, err := service.AddSource(ctx, source); err != nil {
		t.Fatalf("AddSource() error = %v", err)
	}

	result, err := service.Sync(ctx, SyncOptions{SourceID: "docs"})
	if err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if want := (SyncResult{Scanned: 1, Embedded: 1, Upserted: 1}); !reflect.DeepEqual(
		result,
		want,
	) {
		t.Fatalf("legacy Sync() = %#v, want %#v", result, want)
	}
	records, err := state.recordsForSource(ctx, "docs")
	if err != nil {
		t.Fatalf("recordsForSource() error = %v", err)
	}
	if len(records) != 1 || records[0].VectorVersion != currentVectorVersion(settings) {
		t.Fatalf("records = %#v, want refreshed vector version", records)
	}

	result, err = service.Sync(ctx, SyncOptions{SourceID: "docs"})
	if err != nil {
		t.Fatalf("second Sync() error = %v", err)
	}
	if want := (SyncResult{Scanned: 1, Unchanged: 1}); !reflect.DeepEqual(result, want) {
		t.Fatalf("second Sync() = %#v, want %#v", result, want)
	}
}

func TestServiceSyncReindexesWhenCollectionWasRecreated(t *testing.T) {
	ctx := context.Background()
	settings := testSettings(t)
	sourceDir := filepath.Join(settings.WorkspaceLocalDir, "docs")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatalf("create docs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "note.md"), []byte("body\n"), 0o644); err != nil {
		t.Fatalf("write note: %v", err)
	}
	source := Source{
		ID:         "docs",
		Collection: CollectionSemantic,
		SourceType: SourceTypeMarkdownTree,
		Path:       "/workspace/docs",
		Enabled:    true,
	}
	docs, err := ScanSource(ctx, settings, source)
	if err != nil {
		t.Fatalf("ScanSource() error = %v", err)
	}
	state, err := OpenState(ctx, settings)
	if err != nil {
		t.Fatalf("OpenState() error = %v", err)
	}
	defer state.Close()
	if err := state.upsertPoint(ctx, stateRecord{
		PointID:       deterministicPointID(docs[0].Collection, docs[0].SourceID, docs[0].Identity),
		SourceID:      docs[0].SourceID,
		Collection:    docs[0].Collection,
		Path:          docs[0].Path,
		Identity:      docs[0].Identity,
		ContentHash:   docs[0].ContentHash,
		VectorVersion: currentVectorVersion(settings),
	}); err != nil {
		t.Fatalf("upsert current point state: %v", err)
	}
	vectors := newFakeVectorStore()
	vectors.ensureResults[CollectionSemantic] = CollectionEnsureResult{Recreated: true}
	service := NewService(settings, state, vectors, fakeEmbedder{})
	if _, err := service.AddSource(ctx, source); err != nil {
		t.Fatalf("AddSource() error = %v", err)
	}

	result, err := service.Sync(ctx, SyncOptions{SourceID: "docs"})
	if err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if want := (SyncResult{Scanned: 1, Embedded: 1, Upserted: 1, Pruned: 1}); !reflect.DeepEqual(
		result,
		want,
	) {
		t.Fatalf("recreated Sync() = %#v, want %#v", result, want)
	}
	if len(vectors.points[CollectionSemantic]) != 1 {
		t.Fatalf(
			"points after recreated sync = %#v, want one reindexed point",
			vectors.points[CollectionSemantic],
		)
	}
}

func TestServiceDeleteCollectionClearsVectorStoreAndState(t *testing.T) {
	ctx := context.Background()
	settings := testSettings(t)
	state, err := OpenState(ctx, settings)
	if err != nil {
		t.Fatalf("OpenState() error = %v", err)
	}
	defer state.Close()
	if err := state.upsertPoint(ctx, stateRecord{
		PointID:       "point-1",
		SourceID:      "docs",
		Collection:    CollectionSemantic,
		Path:          "/memory/semantic/a.md",
		Identity:      "/memory/semantic/a.md",
		ContentHash:   "hash-a",
		VectorVersion: currentVectorVersion(settings),
	}); err != nil {
		t.Fatalf("upsert point state: %v", err)
	}
	if err := state.storeSyncResult(ctx, Source{
		ID:         "docs",
		Collection: CollectionSemantic,
	}, SyncResult{Scanned: 1, Embedded: 1, Upserted: 1}); err != nil {
		t.Fatalf("store sync result: %v", err)
	}
	vectors := newFakeVectorStore()
	vectors.points[CollectionSemantic] = map[string]Point{"point-1": {ID: "point-1"}}
	service := NewService(settings, state, vectors, fakeEmbedder{})

	result, err := service.DeleteCollection(ctx, CollectionSemantic)
	if err != nil {
		t.Fatalf("DeleteCollection() error = %v", err)
	}
	want := CollectionDeleteResult{
		Collection:    CollectionSemantic,
		Deleted:       true,
		StatePoints:   1,
		StateSyncRuns: 1,
	}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("DeleteCollection() = %#v, want %#v", result, want)
	}
	if _, ok := vectors.points[CollectionSemantic]; ok {
		t.Fatalf(
			"vector collection still exists after delete: %#v",
			vectors.points[CollectionSemantic],
		)
	}
	statuses, err := state.sourceStatuses(ctx)
	if err != nil {
		t.Fatalf("sourceStatuses() error = %v", err)
	}
	if len(statuses) != 0 {
		t.Fatalf("sourceStatuses() after collection delete = %#v, want none", statuses)
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

func TestServiceSyncReportsProgressPerSourceAndChunk(t *testing.T) {
	ctx := context.Background()
	settings := testSettings(t)
	settings.SyncBatchSize = 2
	writeMarkdownDocs(
		t,
		filepath.Join(settings.WorkspaceLocalDir, "docs-alpha"),
		"a.md",
		"b.md",
		"c.md",
	)
	writeMarkdownDocs(
		t,
		filepath.Join(settings.WorkspaceLocalDir, "docs-beta"),
		"d.md",
		"e.md",
	)
	state, err := OpenState(ctx, settings)
	if err != nil {
		t.Fatalf("OpenState() error = %v", err)
	}
	defer state.Close()
	service := NewService(settings, state, newFakeVectorStore(), fakeEmbedder{})
	for _, source := range []Source{
		{
			ID:         "alpha",
			Collection: CollectionSemantic,
			SourceType: SourceTypeMarkdownTree,
			Path:       "/workspace/docs-alpha",
			Enabled:    true,
		},
		{
			ID:         "beta",
			Collection: CollectionCore,
			SourceType: SourceTypeMarkdownTree,
			Path:       "/workspace/docs-beta",
			Enabled:    true,
		},
	} {
		if _, err := service.AddSource(ctx, source); err != nil {
			t.Fatalf("AddSource(%q) error = %v", source.ID, err)
		}
	}
	for _, id := range []string{"core-memory", "semantic-memory", "zettelkasten-notes"} {
		if _, err := service.SetSourceEnabled(ctx, id, false); err != nil {
			t.Fatalf("SetSourceEnabled(%q, false) error = %v", id, err)
		}
	}

	var calls []SyncProgress
	result, err := service.Sync(ctx, SyncOptions{
		Progress: func(progress SyncProgress) {
			calls = append(calls, progress)
		},
	})
	if err != nil {
		t.Fatalf("Sync() error = %v", err)
	}

	want := []SyncProgress{
		{SourceID: "alpha", SourcesCompleted: 0, SourcesTotal: 2},
		{
			SourceID:         "alpha",
			SourcesCompleted: 0,
			SourcesTotal:     2,
			Scanned:          3,
			Embedded:         2,
			Upserted:         2,
		},
		{
			SourceID:         "alpha",
			SourcesCompleted: 0,
			SourcesTotal:     2,
			Scanned:          3,
			Embedded:         3,
			Upserted:         3,
		},
		{
			SourceID:         "beta",
			SourcesCompleted: 1,
			SourcesTotal:     2,
			Scanned:          3,
			Embedded:         3,
			Upserted:         3,
		},
		{
			SourceID:         "beta",
			SourcesCompleted: 1,
			SourcesTotal:     2,
			Scanned:          5,
			Embedded:         5,
			Upserted:         5,
		},
		{
			SourcesCompleted: 2,
			SourcesTotal:     2,
			Scanned:          5,
			Embedded:         5,
			Upserted:         5,
		},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("Sync() progress calls = %#v, want %#v", calls, want)
	}
	for i := 1; i < len(calls); i++ {
		prev, current := calls[i-1], calls[i]
		regressed := current.Scanned < prev.Scanned ||
			current.Embedded < prev.Embedded ||
			current.Upserted < prev.Upserted ||
			current.Pruned < prev.Pruned
		if regressed {
			t.Fatalf("progress counters regressed: %#v -> %#v", prev, current)
		}
	}
	wantResult := SyncResult{Scanned: 5, Embedded: 5, Upserted: 5}
	if !reflect.DeepEqual(result, wantResult) {
		t.Fatalf("Sync() = %#v, want %#v", result, wantResult)
	}
	last := calls[len(calls)-1]
	if last.Scanned != result.Scanned ||
		last.Embedded != result.Embedded ||
		last.Upserted != result.Upserted {
		t.Fatalf("final progress = %#v, want counters matching Sync() result %#v", last, result)
	}
}

func TestServiceSyncKeepsCheckpointedBatchesOnEmbedderFailure(t *testing.T) {
	ctx := context.Background()
	settings := testSettings(t)
	settings.SyncBatchSize = 2
	writeMarkdownDocs(
		t,
		filepath.Join(settings.WorkspaceLocalDir, "docs"),
		"a.md",
		"b.md",
		"c.md",
		"d.md",
		"e.md",
	)
	state, err := OpenState(ctx, settings)
	if err != nil {
		t.Fatalf("OpenState() error = %v", err)
	}
	vectors := newFakeVectorStore()
	errBoom := errors.New("embed boom")
	service := NewService(
		settings,
		state,
		vectors,
		&failOnCallEmbedder{failOnCall: 3, err: errBoom},
	)
	addDocsSource(ctx, t, service)

	if _, err := service.Sync(ctx, SyncOptions{SourceID: "docs"}); !errors.Is(err, errBoom) {
		t.Fatalf("Sync() error = %v, want %v", err, errBoom)
	}
	if err := state.Close(); err != nil {
		t.Fatalf("close state: %v", err)
	}
	if got := countStateLines(t, defaultStatePath(settings), stateLinePoint); got != 4 {
		t.Fatalf("state file point records after failure = %d, want 4", got)
	}

	state, err = OpenState(ctx, settings)
	if err != nil {
		t.Fatalf("reopen OpenState() error = %v", err)
	}
	defer state.Close()
	service = NewService(settings, state, vectors, fakeEmbedder{})
	result, err := service.Sync(ctx, SyncOptions{SourceID: "docs"})
	if err != nil {
		t.Fatalf("follow-up Sync() error = %v", err)
	}
	if want := (SyncResult{Scanned: 5, Embedded: 1, Upserted: 1, Unchanged: 4}); !reflect.DeepEqual(
		result,
		want,
	) {
		t.Fatalf(
			"follow-up Sync() = %#v, want %#v (only the remaining docs embedded)",
			result,
			want,
		)
	}
	if got := countStateLines(t, defaultStatePath(settings), stateLinePoint); got != 5 {
		t.Fatalf("state file point records after follow-up sync = %d, want 5", got)
	}
}

func TestServiceSyncKeepsCheckpointedBatchesOnCancellation(t *testing.T) {
	settings := testSettings(t)
	settings.SyncBatchSize = 2
	writeMarkdownDocs(
		t,
		filepath.Join(settings.WorkspaceLocalDir, "docs"),
		"a.md",
		"b.md",
		"c.md",
		"d.md",
		"e.md",
	)
	ctx := context.Background()
	state, err := OpenState(ctx, settings)
	if err != nil {
		t.Fatalf("OpenState() error = %v", err)
	}
	vectors := newFakeVectorStore()
	service := NewService(settings, state, vectors, &blockOnCallEmbedder{blockOnCall: 3})
	addDocsSource(ctx, t, service)

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	watchdog := time.AfterFunc(10*time.Second, cancel)
	defer watchdog.Stop()
	sawCheckpoint := false
	_, err = service.Sync(runCtx, SyncOptions{
		SourceID: "docs",
		Progress: func(progress SyncProgress) {
			if progress.SourceID == "docs" && progress.Embedded >= 4 {
				sawCheckpoint = true
				cancel()
			}
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Sync() error = %v, want context.Canceled", err)
	}
	if !sawCheckpoint {
		t.Fatal("progress callback never observed completed checkpoint batches")
	}
	if err := state.Close(); err != nil {
		t.Fatalf("close state: %v", err)
	}
	if got := countStateLines(t, defaultStatePath(settings), stateLinePoint); got != 4 {
		t.Fatalf("state file point records after cancel = %d, want 4", got)
	}

	state, err = OpenState(ctx, settings)
	if err != nil {
		t.Fatalf("reopen OpenState() error = %v", err)
	}
	defer state.Close()
	service = NewService(settings, state, vectors, fakeEmbedder{})
	result, err := service.Sync(ctx, SyncOptions{SourceID: "docs"})
	if err != nil {
		t.Fatalf("follow-up Sync() error = %v", err)
	}
	if want := (SyncResult{Scanned: 5, Embedded: 1, Upserted: 1, Unchanged: 4}); !reflect.DeepEqual(
		result,
		want,
	) {
		t.Fatalf(
			"follow-up Sync() = %#v, want %#v (only the remaining docs embedded)",
			result,
			want,
		)
	}
}

func addDocsSource(ctx context.Context, t *testing.T, service *Service) {
	t.Helper()
	if _, err := service.AddSource(ctx, Source{
		ID:         "docs",
		Collection: CollectionSemantic,
		SourceType: SourceTypeMarkdownTree,
		Path:       "/workspace/docs",
		Enabled:    true,
	}); err != nil {
		t.Fatalf("AddSource() error = %v", err)
	}
}

func countStateLines(t *testing.T, path string, lineType string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read state file %s: %v", path, err)
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return 0
	}
	count := 0
	for _, line := range strings.Split(trimmed, "\n") {
		var entry struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("decode state line %q: %v", line, err)
		}
		if entry.Type == lineType {
			count++
		}
	}
	return count
}

type failOnCallEmbedder struct {
	failOnCall int
	err        error
	calls      int
}

func (f *failOnCallEmbedder) EmbedDocuments(
	ctx context.Context,
	reqs []EmbeddingRequest,
) ([][]float32, error) {
	_ = ctx
	f.calls++
	if f.calls == f.failOnCall {
		return nil, f.err
	}
	return fakeDocVectors(reqs), nil
}

func (f *failOnCallEmbedder) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	_ = ctx
	return []float32{float32(len(text)), 1}, nil
}

type blockOnCallEmbedder struct {
	blockOnCall int
	calls       int
}

func (f *blockOnCallEmbedder) EmbedDocuments(
	ctx context.Context,
	reqs []EmbeddingRequest,
) ([][]float32, error) {
	f.calls++
	if f.calls < f.blockOnCall {
		return fakeDocVectors(reqs), nil
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (f *blockOnCallEmbedder) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	_ = ctx
	return []float32{float32(len(text)), 1}, nil
}

func fakeDocVectors(reqs []EmbeddingRequest) [][]float32 {
	out := make([][]float32, 0, len(reqs))
	for _, req := range reqs {
		out = append(out, []float32{float32(len(req.Text)), 1})
	}
	return out
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
	ensureResults  map[string]CollectionEnsureResult
	searchErrs     map[string]error
	searchRequests map[string][]SearchRequest
	searchResults  map[string][]SearchResult
}

func newFakeVectorStore() *fakeVectorStore {
	return &fakeVectorStore{
		points:         make(map[string]map[string]Point),
		ensureResults:  make(map[string]CollectionEnsureResult),
		searchErrs:     make(map[string]error),
		searchRequests: make(map[string][]SearchRequest),
		searchResults:  make(map[string][]SearchResult),
	}
}

func (f *fakeVectorStore) EnsureCollection(
	ctx context.Context,
	collection string,
	dimensions int,
) (CollectionEnsureResult, error) {
	_, _ = ctx, dimensions
	result := f.ensureResults[collection]
	if result.Recreated {
		delete(f.points, collection)
	}
	if f.points[collection] == nil {
		f.points[collection] = make(map[string]Point)
		if !result.Recreated {
			result.Created = true
		}
	}
	return result, nil
}

func (f *fakeVectorStore) DeleteCollection(
	ctx context.Context,
	collection string,
) (bool, error) {
	_ = ctx
	_, existed := f.points[collection]
	delete(f.points, collection)
	return existed, nil
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

func writeMarkdownDocs(t *testing.T, dir string, names ...string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create %s: %v", dir, err)
	}
	for i, name := range names {
		body := fmt.Sprintf("body %d for %s\n", i, name)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
}
