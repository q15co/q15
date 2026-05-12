package embed

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Service coordinates source registry operations, scanning, embedding, vector
// storage, and dirty-tracking state.
type Service struct {
	settings Settings
	registry *Registry
	state    *StateStore
	vectors  VectorStore
	embedder Embedder
}

// NewService constructs the embedding service from already-initialized storage
// dependencies. Tests pass fakes here; app wiring passes Qdrant and Gemini.
func NewService(
	settings Settings,
	state *StateStore,
	vectors VectorStore,
	embedder Embedder,
) *Service {
	settings.Model = normalizeModel(settings.Model)
	settings.Dimensions = normalizeDimensions(settings.Dimensions)
	return &Service{
		settings: settings,
		registry: NewRegistry(settings),
		state:    state,
		vectors:  vectors,
		embedder: embedder,
	}
}

// Close releases embedding service storage clients.
func (s *Service) Close() error {
	var err error
	if s.state != nil {
		err = s.state.Close()
	}
	if s.vectors != nil {
		if closeErr := s.vectors.Close(); err == nil {
			err = closeErr
		}
	}
	return err
}

// ListSources returns configured typed ingestion sources.
func (s *Service) ListSources(ctx context.Context) ([]Source, error) {
	return s.registry.List(ctx)
}

// AddSource validates and stores one typed ingestion source.
func (s *Service) AddSource(ctx context.Context, source Source) (Source, error) {
	return s.registry.Add(ctx, source)
}

// RemoveSource removes one source from the registry.
func (s *Service) RemoveSource(ctx context.Context, id string) (Source, error) {
	return s.registry.Remove(ctx, id)
}

// SetSourceEnabled toggles one source without removing it.
func (s *Service) SetSourceEnabled(ctx context.Context, id string, enabled bool) (Source, error) {
	return s.registry.SetEnabled(ctx, id, enabled)
}

// DeleteCollection drops one collection and clears matching dirty-tracking
// state. The next sync recreates the collection from configured sources.
func (s *Service) DeleteCollection(
	ctx context.Context,
	collection string,
) (CollectionDeleteResult, error) {
	if s.state == nil {
		return CollectionDeleteResult{}, fmt.Errorf("embed state store is not configured")
	}
	if s.vectors == nil {
		return CollectionDeleteResult{}, fmt.Errorf("vector store is not configured")
	}
	if err := validateCollection(collection); err != nil {
		return CollectionDeleteResult{}, err
	}
	deleted, err := s.vectors.DeleteCollection(ctx, collection)
	if err != nil {
		return CollectionDeleteResult{}, err
	}
	stateResult, err := s.state.deleteCollection(ctx, collection)
	if err != nil {
		return CollectionDeleteResult{}, err
	}
	return CollectionDeleteResult{
		Collection:    collection,
		Deleted:       deleted,
		StatePoints:   stateResult.Points,
		StateSyncRuns: stateResult.SyncRuns,
	}, nil
}

// SyncOptions narrows or expands one embedding sync run.
type SyncOptions struct {
	Collection string
	SourceID   string
	Full       bool
}

// Sync scans enabled sources, embeds changed documents, and prunes stale points.
func (s *Service) Sync(ctx context.Context, opts SyncOptions) (SyncResult, error) {
	if s.state == nil {
		return SyncResult{}, fmt.Errorf("embed state store is not configured")
	}
	if s.vectors == nil {
		return SyncResult{}, fmt.Errorf("vector store is not configured")
	}
	if s.embedder == nil {
		return SyncResult{}, fmt.Errorf("embedder is not configured")
	}
	if strings.TrimSpace(opts.Collection) != "" {
		if err := validateCollection(opts.Collection); err != nil {
			return SyncResult{}, err
		}
	}

	sources, err := s.registry.List(ctx)
	if err != nil {
		return SyncResult{}, err
	}
	activeSourceIDs := make(map[string]struct{}, len(sources))
	for _, source := range sources {
		activeSourceIDs[source.ID] = struct{}{}
	}
	result, err := s.pruneRemovedSources(ctx, activeSourceIDs)
	if err != nil {
		return SyncResult{}, err
	}

	sourceIDFound := opts.SourceID == ""
	for _, source := range sources {
		if opts.Collection != "" && source.Collection != opts.Collection {
			continue
		}
		if opts.SourceID != "" {
			if source.ID != opts.SourceID {
				continue
			}
			sourceIDFound = true
		}
		if !source.Enabled {
			continue
		}
		sourceResult, err := s.syncSource(ctx, source, opts.Full)
		if err != nil {
			return SyncResult{}, err
		}
		result = addSyncResults(result, sourceResult)
	}
	if !sourceIDFound {
		return SyncResult{}, fmt.Errorf("source id %q not found", opts.SourceID)
	}
	return result, nil
}

func (s *Service) syncSource(ctx context.Context, source Source, full bool) (SyncResult, error) {
	docs, err := ScanSource(ctx, s.settings, source)
	if err != nil {
		return SyncResult{}, err
	}
	result := SyncResult{Scanned: len(docs)}
	ensureResult, err := s.vectors.EnsureCollection(ctx, source.Collection, s.settings.Dimensions)
	if err != nil {
		return SyncResult{}, err
	}
	if ensureResult.Created || ensureResult.Recreated {
		stateResult, err := s.state.deleteCollection(ctx, source.Collection)
		if err != nil {
			return SyncResult{}, err
		}
		result.Pruned += stateResult.Points
		full = true
	}
	vectorVersion := currentVectorVersion(s.settings)

	seen := make(map[string]struct{}, len(docs))
	var embedReqs []EmbeddingRequest
	var embedDocs []Document
	for _, doc := range docs {
		if doc.Text == "" {
			continue
		}
		seen[doc.Identity] = struct{}{}
		record, ok, err := s.state.findByIdentity(ctx, doc.Collection, doc.SourceID, doc.Identity)
		if err != nil {
			return SyncResult{}, fmt.Errorf("query embed state: %w", err)
		}
		if ok && !full && stateRecordMatchesDocument(record, doc, vectorVersion) {
			result.Unchanged++
			continue
		}
		if !ok && !full {
			moved, movedOK, err := s.state.findByContentHash(
				ctx,
				doc.Collection,
				doc.SourceID,
				doc.ContentHash,
			)
			if err != nil {
				return SyncResult{}, fmt.Errorf("query moved embed state: %w", err)
			}
			if movedOK && stateRecordMatchesDocument(moved, doc, vectorVersion) {
				if err := s.vectors.UpdatePayload(ctx, doc.Collection, moved.PointID, doc.Payload); err != nil {
					return SyncResult{}, err
				}
				if err := s.state.upsertPoint(ctx, stateRecord{
					PointID:       moved.PointID,
					SourceID:      doc.SourceID,
					Collection:    doc.Collection,
					Path:          doc.Path,
					Identity:      doc.Identity,
					ContentHash:   doc.ContentHash,
					VectorVersion: vectorVersion,
				}); err != nil {
					return SyncResult{}, err
				}
				result.Upserted++
				continue
			}
		}
		embedDocs = append(embedDocs, doc)
		embedReqs = append(embedReqs, EmbeddingRequest{
			Text:  doc.Text,
			Title: titleForDocument(doc),
		})
	}

	if len(embedReqs) > 0 {
		vectors, err := s.embedder.EmbedDocuments(ctx, embedReqs)
		if err != nil {
			return SyncResult{}, err
		}
		if len(vectors) != len(embedDocs) {
			return SyncResult{}, fmt.Errorf(
				"embedder returned %d vectors for %d documents",
				len(vectors),
				len(embedDocs),
			)
		}
		points := make([]Point, 0, len(embedDocs))
		records := make([]stateRecord, 0, len(embedDocs))
		for i, doc := range embedDocs {
			pointID := deterministicPointID(doc.Collection, doc.SourceID, doc.Identity)
			points = append(points, Point{
				ID:         pointID,
				Vector:     vectors[i],
				SparseText: doc.Text,
				Payload:    doc.Payload,
			})
			records = append(records, stateRecord{
				PointID:       pointID,
				SourceID:      doc.SourceID,
				Collection:    doc.Collection,
				Path:          doc.Path,
				Identity:      doc.Identity,
				ContentHash:   doc.ContentHash,
				VectorVersion: vectorVersion,
			})
		}
		if err := s.vectors.Upsert(ctx, source.Collection, points); err != nil {
			return SyncResult{}, err
		}
		for _, record := range records {
			if err := s.state.upsertPoint(ctx, record); err != nil {
				return SyncResult{}, err
			}
		}
		result.Embedded += len(embedDocs)
		result.Upserted += len(embedDocs)
	}

	pruned, err := s.pruneMissingDocuments(ctx, source, seen)
	if err != nil {
		return SyncResult{}, err
	}
	result.Pruned += pruned
	if err := s.state.storeSyncResult(ctx, source, result); err != nil {
		return SyncResult{}, err
	}
	return result, nil
}

func (s *Service) pruneMissingDocuments(
	ctx context.Context,
	source Source,
	seen map[string]struct{},
) (int, error) {
	records, err := s.state.recordsForSource(ctx, source.ID)
	if err != nil {
		return 0, err
	}
	var ids []string
	for _, record := range records {
		if _, ok := seen[record.Identity]; ok {
			continue
		}
		ids = append(ids, record.PointID)
	}
	if len(ids) == 0 {
		return 0, nil
	}
	if err := s.vectors.Delete(ctx, source.Collection, ids); err != nil {
		return 0, err
	}
	if err := s.state.deletePointIDs(ctx, ids); err != nil {
		return 0, err
	}
	return len(ids), nil
}

func (s *Service) pruneRemovedSources(
	ctx context.Context,
	active map[string]struct{},
) (SyncResult, error) {
	ids, err := s.state.allSourceIDs(ctx)
	if err != nil {
		return SyncResult{}, err
	}
	var result SyncResult
	for _, id := range ids {
		if _, ok := active[id]; ok {
			continue
		}
		records, err := s.state.recordsForSource(ctx, id)
		if err != nil {
			return SyncResult{}, err
		}
		byCollection := make(map[string][]string)
		for _, record := range records {
			byCollection[record.Collection] = append(
				byCollection[record.Collection],
				record.PointID,
			)
		}
		for collection, pointIDs := range byCollection {
			if err := s.vectors.Delete(ctx, collection, pointIDs); err != nil {
				return SyncResult{}, err
			}
			result.Pruned += len(pointIDs)
		}
		if err := s.state.deleteSource(ctx, id); err != nil {
			return SyncResult{}, err
		}
	}
	return result, nil
}

// SearchOptions configures one embedding search.
type SearchOptions struct {
	Query      string
	Collection string
	Filter     map[string]any
	Mode       string
	Limit      int
}

// Search embeds a query and searches one or all supported collections.
func (s *Service) Search(ctx context.Context, opts SearchOptions) ([]SearchResult, error) {
	if s.vectors == nil {
		return nil, fmt.Errorf("vector store is not configured")
	}
	query := strings.TrimSpace(opts.Query)
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}
	mode, err := normalizeSearchMode(opts.Mode)
	if err != nil {
		return nil, err
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}
	req := SearchRequest{
		SparseText: query,
		Filter:     opts.Filter,
		Mode:       mode,
		Limit:      limit,
	}
	if mode != SearchModeSparse {
		if s.embedder == nil {
			return nil, fmt.Errorf("embedder is not configured")
		}
		vector, err := s.embedder.EmbedQuery(ctx, query)
		if err != nil {
			return nil, err
		}
		req.Vector = vector
	}
	collections := []string{opts.Collection}
	unscoped := strings.TrimSpace(opts.Collection) == ""
	if unscoped {
		collections = SupportedCollections()
	} else if err := validateCollection(opts.Collection); err != nil {
		return nil, err
	}
	var out []SearchResult
	for _, collection := range collections {
		if unscoped {
			status, err := s.vectors.Status(ctx, collection)
			if err != nil {
				return nil, err
			}
			if !status.Exists {
				continue
			}
		}
		results, err := s.vectors.Search(ctx, collection, req)
		if err != nil {
			if unscoped && isMissingCollectionError(err) {
				continue
			}
			return nil, err
		}
		out = append(out, results...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Score > out[j].Score
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func normalizeSearchMode(mode string) (string, error) {
	mode = strings.TrimSpace(mode)
	if mode == "" {
		return SearchModeHybrid, nil
	}
	switch mode {
	case SearchModeDense, SearchModeSparse, SearchModeHybrid:
		return mode, nil
	default:
		return "", fmt.Errorf("search mode %q is not supported; use dense, sparse, or hybrid", mode)
	}
}

func isMissingCollectionError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "collection") &&
		(strings.Contains(message, "not found") ||
			strings.Contains(message, "doesn't exist") ||
			strings.Contains(message, "does not exist"))
}

// Status summarizes configured sources and vector collection health.
type Status struct {
	Sources     []Source           `json:"sources"`
	SourceState []SourceStatus     `json:"source_state"`
	Collections []CollectionStatus `json:"collections"`
}

// Status returns embedding source state and collection health.
func (s *Service) Status(ctx context.Context, collection string) (Status, error) {
	if s.state == nil {
		return Status{}, fmt.Errorf("embed state store is not configured")
	}
	if s.vectors == nil {
		return Status{}, fmt.Errorf("vector store is not configured")
	}
	sources, err := s.registry.List(ctx)
	if err != nil {
		return Status{}, err
	}
	sourceState, err := s.state.sourceStatuses(ctx)
	if err != nil {
		return Status{}, err
	}
	collections := []string{collection}
	if strings.TrimSpace(collection) == "" {
		collections = SupportedCollections()
	} else if err := validateCollection(collection); err != nil {
		return Status{}, err
	}
	var collectionStatuses []CollectionStatus
	for _, name := range collections {
		status, err := s.vectors.Status(ctx, name)
		if err != nil {
			status = CollectionStatus{
				Collection: name,
				Exists:     false,
				Dimensions: s.settings.Dimensions,
			}
		}
		collectionStatuses = append(collectionStatuses, status)
	}
	return Status{
		Sources:     sources,
		SourceState: sourceState,
		Collections: collectionStatuses,
	}, nil
}

func addSyncResults(a SyncResult, b SyncResult) SyncResult {
	return SyncResult{
		Scanned:   a.Scanned + b.Scanned,
		Embedded:  a.Embedded + b.Embedded,
		Upserted:  a.Upserted + b.Upserted,
		Pruned:    a.Pruned + b.Pruned,
		Unchanged: a.Unchanged + b.Unchanged,
	}
}

func titleForDocument(doc Document) string {
	for _, key := range []string{"title", "section", "file_stem"} {
		if value, ok := doc.Payload[key].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return doc.Path
}

func currentVectorVersion(settings Settings) string {
	return fmt.Sprintf(
		"dense:%s:%d;sparse:%s",
		normalizeModel(settings.Model),
		normalizeDimensions(settings.Dimensions),
		SparseModelBM25,
	)
}

func stateRecordMatchesDocument(record stateRecord, doc Document, vectorVersion string) bool {
	return record.ContentHash == doc.ContentHash && record.VectorVersion == vectorVersion
}
