package embed

import (
	"context"
	"fmt"
	"math"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/qdrant/go-client/qdrant"
)

// QdrantStore is the concrete vector store used by the embedding service.
type QdrantStore struct {
	client     *qdrant.Client
	dimensions int
}

// NewQdrantStore constructs a Qdrant-backed vector store.
func NewQdrantStore(rawURL string, dimensions int) (*QdrantStore, error) {
	cfg, err := qdrantConfigFromURL(rawURL)
	if err != nil {
		return nil, err
	}
	cfg.PoolSize = 1
	client, err := qdrant.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("create Qdrant client: %w", err)
	}
	return &QdrantStore{
		client:     client,
		dimensions: normalizeDimensions(dimensions),
	}, nil
}

// EnsureCollection creates a named dense+sparse vector collection when missing.
func (s *QdrantStore) EnsureCollection(
	ctx context.Context,
	collection string,
	dimensions int,
) error {
	if s == nil || s.client == nil {
		return fmt.Errorf("qdrant client is not configured")
	}
	if err := validateCollection(collection); err != nil {
		return err
	}
	dimensions = normalizeDimensions(dimensions)
	exists, err := s.client.CollectionExists(ctx, collection)
	if err != nil {
		return err
	}
	if exists {
		return s.ensureSparseVectorConfig(ctx, collection)
	}
	return s.client.CreateCollection(ctx, &qdrant.CreateCollection{
		CollectionName: collection,
		VectorsConfig: qdrant.NewVectorsConfigMap(map[string]*qdrant.VectorParams{
			VectorNameDense: {
				Size:     uint64(dimensions),
				Distance: qdrant.Distance_Cosine,
			},
		}),
		SparseVectorsConfig: sparseVectorsConfig(),
	})
}

// Upsert writes embedded points to one Qdrant collection.
func (s *QdrantStore) Upsert(ctx context.Context, collection string, points []Point) error {
	if len(points) == 0 {
		return nil
	}
	wait := true
	qpoints := make([]*qdrant.PointStruct, 0, len(points))
	for _, point := range points {
		if len(point.Vector) == 0 {
			return fmt.Errorf("point %q has empty vector", point.ID)
		}
		payload, err := qdrant.TryValueMap(point.Payload)
		if err != nil {
			return fmt.Errorf("convert point %q payload: %w", point.ID, err)
		}
		qpoints = append(qpoints, &qdrant.PointStruct{
			Id:      qdrant.NewID(point.ID),
			Vectors: vectorMapForPoint(point),
			Payload: payload,
		})
	}
	_, err := s.client.Upsert(ctx, &qdrant.UpsertPoints{
		CollectionName: collection,
		Wait:           &wait,
		Points:         qpoints,
	})
	return err
}

// UpdatePayload overwrites one point payload without changing its vector.
func (s *QdrantStore) UpdatePayload(
	ctx context.Context,
	collection string,
	pointID string,
	payload map[string]any,
) error {
	wait := true
	qpayload, err := qdrant.TryValueMap(payload)
	if err != nil {
		return fmt.Errorf("convert point %q payload: %w", pointID, err)
	}
	_, err = s.client.OverwritePayload(ctx, &qdrant.SetPayloadPoints{
		CollectionName: collection,
		Wait:           &wait,
		Payload:        qpayload,
		PointsSelector: qdrant.NewPointsSelector(qdrant.NewID(pointID)),
	})
	return err
}

// Delete removes points from one Qdrant collection.
func (s *QdrantStore) Delete(
	ctx context.Context,
	collection string,
	pointIDs []string,
) error {
	if len(pointIDs) == 0 {
		return nil
	}
	wait := true
	ids := make([]*qdrant.PointId, 0, len(pointIDs))
	for _, pointID := range pointIDs {
		ids = append(ids, qdrant.NewID(pointID))
	}
	_, err := s.client.Delete(ctx, &qdrant.DeletePoints{
		CollectionName: collection,
		Wait:           &wait,
		Points:         qdrant.NewPointsSelector(ids...),
	})
	return err
}

// Search runs a dense, sparse, or hybrid vector query against one collection.
func (s *QdrantStore) Search(
	ctx context.Context,
	collection string,
	req SearchRequest,
) ([]SearchResult, error) {
	if s == nil || s.client == nil {
		return nil, fmt.Errorf("qdrant client is not configured")
	}
	if err := validateCollection(collection); err != nil {
		return nil, err
	}
	mode, err := normalizeSearchMode(req.Mode)
	if err != nil {
		return nil, err
	}
	if req.Limit <= 0 {
		req.Limit = 10
	}
	if req.Limit > 50 {
		req.Limit = 50
	}
	qfilter, err := filterFromPayload(req.Filter)
	if err != nil {
		return nil, err
	}
	query, err := queryPointsForSearch(collection, req, qfilter, mode)
	if err != nil {
		return nil, err
	}
	points, err := s.client.Query(ctx, query)
	if err != nil && mode == SearchModeHybrid && isSparseVectorUnavailableError(err) {
		query, err = queryPointsForSearch(collection, req, qfilter, SearchModeDense)
		if err != nil {
			return nil, err
		}
		points, err = s.client.Query(ctx, query)
	}
	if err != nil {
		return nil, err
	}
	return searchResultsFromPoints(collection, points), nil
}

func (s *QdrantStore) ensureSparseVectorConfig(ctx context.Context, collection string) error {
	info, err := s.client.GetCollectionInfo(ctx, collection)
	if err != nil {
		return err
	}
	sparseVectors := info.GetConfig().GetParams().GetSparseVectorsConfig().GetMap()
	if _, ok := sparseVectors[VectorNameSparse]; ok {
		return nil
	}
	merged := make(map[string]*qdrant.SparseVectorParams, len(sparseVectors)+1)
	for name, params := range sparseVectors {
		merged[name] = params
	}
	merged[VectorNameSparse] = lexicalSparseVectorParams()
	err = s.client.UpdateCollection(ctx, &qdrant.UpdateCollection{
		CollectionName:      collection,
		SparseVectorsConfig: qdrant.NewSparseVectorsConfig(merged),
	})
	if err != nil {
		return fmt.Errorf(
			"add sparse vector %q to Qdrant collection %q: %w",
			VectorNameSparse,
			collection,
			err,
		)
	}
	return nil
}

func sparseVectorsConfig() *qdrant.SparseVectorConfig {
	return qdrant.NewSparseVectorsConfig(map[string]*qdrant.SparseVectorParams{
		VectorNameSparse: lexicalSparseVectorParams(),
	})
}

func lexicalSparseVectorParams() *qdrant.SparseVectorParams {
	return &qdrant.SparseVectorParams{
		Modifier: qdrant.Modifier_Idf.Enum(),
	}
}

func vectorMapForPoint(point Point) *qdrant.Vectors {
	vectors := map[string]*qdrant.Vector{
		VectorNameDense: qdrant.NewVectorDense(point.Vector),
	}
	if len(point.Sparse.Indices) > 0 {
		vectors[VectorNameSparse] = qdrant.NewVectorSparse(
			point.Sparse.Indices,
			point.Sparse.Values,
		)
	}
	return qdrant.NewVectorsMap(vectors)
}

func queryPointsForSearch(
	collection string,
	req SearchRequest,
	qfilter *qdrant.Filter,
	mode string,
) (*qdrant.QueryPoints, error) {
	qlimit := uint64(req.Limit)
	switch mode {
	case SearchModeDense:
		if len(req.Vector) == 0 {
			return nil, fmt.Errorf("dense search requires a dense query vector")
		}
		using := VectorNameDense
		return &qdrant.QueryPoints{
			CollectionName: collection,
			Query:          qdrant.NewQueryDense(req.Vector),
			Using:          &using,
			Filter:         qfilter,
			Limit:          &qlimit,
			WithPayload:    qdrant.NewWithPayload(true),
			WithVectors:    qdrant.NewWithVectors(false),
		}, nil
	case SearchModeSparse:
		if err := validateSparseVector(req.Sparse); err != nil {
			return nil, err
		}
		using := VectorNameSparse
		return &qdrant.QueryPoints{
			CollectionName: collection,
			Query:          qdrant.NewQuerySparse(req.Sparse.Indices, req.Sparse.Values),
			Using:          &using,
			Filter:         qfilter,
			Limit:          &qlimit,
			WithPayload:    qdrant.NewWithPayload(true),
			WithVectors:    qdrant.NewWithVectors(false),
		}, nil
	case SearchModeHybrid:
		if len(req.Vector) == 0 {
			return nil, fmt.Errorf("hybrid search requires a dense query vector")
		}
		if len(req.Sparse.Indices) == 0 {
			return queryPointsForSearch(collection, req, qfilter, SearchModeDense)
		}
		if err := validateSparseVector(req.Sparse); err != nil {
			return nil, err
		}
		denseUsing := VectorNameDense
		sparseUsing := VectorNameSparse
		prefetchLimit := uint64(max(req.Limit*4, 20))
		if prefetchLimit > 200 {
			prefetchLimit = 200
		}
		return &qdrant.QueryPoints{
			CollectionName: collection,
			Prefetch: []*qdrant.PrefetchQuery{
				{
					Query:  qdrant.NewQueryDense(req.Vector),
					Using:  &denseUsing,
					Filter: qfilter,
					Limit:  &prefetchLimit,
				},
				{
					Query:  qdrant.NewQuerySparse(req.Sparse.Indices, req.Sparse.Values),
					Using:  &sparseUsing,
					Filter: qfilter,
					Limit:  &prefetchLimit,
				},
			},
			Query: qdrant.NewQueryRRF(
				&qdrant.Rrf{K: qdrant.PtrOf(uint32(60)), Weights: []float32{0.7, 0.3}},
			),
			Limit:       &qlimit,
			WithPayload: qdrant.NewWithPayload(true),
			WithVectors: qdrant.NewWithVectors(false),
		}, nil
	default:
		return nil, fmt.Errorf(
			"search mode %q is not supported; use dense, sparse, or hybrid",
			mode,
		)
	}
}

func validateSparseVector(vector SparseVector) error {
	if len(vector.Indices) == 0 {
		return fmt.Errorf("sparse search requires at least one sparse query token")
	}
	if len(vector.Indices) != len(vector.Values) {
		return fmt.Errorf(
			"sparse query has %d indices for %d values",
			len(vector.Indices),
			len(vector.Values),
		)
	}
	return nil
}

func isSparseVectorUnavailableError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, VectorNameSparse) ||
		strings.Contains(message, "sparse vector") ||
		(strings.Contains(message, "vector") && strings.Contains(message, "not found"))
}

func searchResultsFromPoints(collection string, points []*qdrant.ScoredPoint) []SearchResult {
	results := make([]SearchResult, 0, len(points))
	for _, point := range points {
		results = append(results, SearchResult{
			Collection: collection,
			ID:         pointIDString(point.GetId()),
			Score:      point.GetScore(),
			Payload:    payloadFromValues(point.GetPayload()),
		})
	}
	return results
}

// Status returns existence and point-count health for one collection.
func (s *QdrantStore) Status(ctx context.Context, collection string) (CollectionStatus, error) {
	if s == nil || s.client == nil {
		return CollectionStatus{}, fmt.Errorf("qdrant client is not configured")
	}
	if err := validateCollection(collection); err != nil {
		return CollectionStatus{}, err
	}
	exists, err := s.client.CollectionExists(ctx, collection)
	if err != nil {
		return CollectionStatus{}, err
	}
	status := CollectionStatus{
		Collection: collection,
		Exists:     exists,
		Dimensions: s.dimensions,
	}
	if !exists {
		return status, nil
	}
	count, err := s.client.Count(ctx, &qdrant.CountPoints{
		CollectionName: collection,
		Exact:          qdrant.PtrOf(true),
	})
	if err != nil {
		return CollectionStatus{}, err
	}
	status.Points = count
	return status, nil
}

// Close closes the underlying Qdrant client.
func (s *QdrantStore) Close() error {
	if s == nil || s.client == nil {
		return nil
	}
	return s.client.Close()
}

func qdrantConfigFromURL(raw string) (*qdrant.Config, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("qdrant URL is required")
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse Qdrant URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("qdrant URL scheme %q is not supported", parsed.Scheme)
	}
	host := parsed.Hostname()
	if host == "" {
		return nil, fmt.Errorf("qdrant URL host is required")
	}
	port := 6334
	if parsed.Port() != "" {
		parsedPort, err := strconv.Atoi(parsed.Port())
		if err != nil || parsedPort <= 0 {
			return nil, fmt.Errorf("qdrant URL port %q is invalid", parsed.Port())
		}
		port = parsedPort
	}
	if strings.Contains(host, ":") {
		host = net.JoinHostPort(host, "")
		host = strings.TrimSuffix(host, ":")
	}
	return &qdrant.Config{
		Host:   host,
		Port:   port,
		UseTLS: parsed.Scheme == "https",
	}, nil
}

func filterFromPayload(filter map[string]any) (*qdrant.Filter, error) {
	if len(filter) == 0 {
		return nil, nil
	}
	conditions := make([]*qdrant.Condition, 0, len(filter))
	for key, value := range filter {
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, fmt.Errorf("filter keys must not be empty")
		}
		condition, err := conditionForValue(key, value)
		if err != nil {
			return nil, err
		}
		conditions = append(conditions, condition)
	}
	return &qdrant.Filter{Must: conditions}, nil
}

func conditionForValue(key string, value any) (*qdrant.Condition, error) {
	switch v := value.(type) {
	case string:
		return qdrant.NewMatchKeyword(key, v), nil
	case bool:
		return qdrant.NewMatchBool(key, v), nil
	case int:
		return qdrant.NewMatchInt(key, int64(v)), nil
	case int64:
		return qdrant.NewMatchInt(key, v), nil
	case float64:
		if math.Trunc(v) != v {
			return nil, fmt.Errorf("filter %q uses non-integer number %v; dense search filters only support exact keyword, bool, and integer matches", key, v)
		}
		return qdrant.NewMatchInt(key, int64(v)), nil
	case []any:
		return listConditionForValue(key, v)
	case []string:
		return qdrant.NewMatchKeywords(key, v...), nil
	case []int64:
		return qdrant.NewMatchInts(key, v...), nil
	default:
		return nil, fmt.Errorf("filter %q has unsupported value type %T", key, value)
	}
}

func listConditionForValue(key string, values []any) (*qdrant.Condition, error) {
	if len(values) == 0 {
		return nil, fmt.Errorf("filter %q array must not be empty", key)
	}
	stringsOnly := make([]string, 0, len(values))
	intsOnly := make([]int64, 0, len(values))
	for _, value := range values {
		switch v := value.(type) {
		case string:
			stringsOnly = append(stringsOnly, v)
		case int:
			intsOnly = append(intsOnly, int64(v))
		case int64:
			intsOnly = append(intsOnly, v)
		case float64:
			if math.Trunc(v) != v {
				return nil, fmt.Errorf("filter %q array contains non-integer number %v", key, v)
			}
			intsOnly = append(intsOnly, int64(v))
		default:
			return nil, fmt.Errorf("filter %q array contains unsupported value type %T", key, value)
		}
	}
	if len(stringsOnly) > 0 && len(intsOnly) > 0 {
		return nil, fmt.Errorf("filter %q array cannot mix string and integer values", key)
	}
	if len(stringsOnly) > 0 {
		return qdrant.NewMatchKeywords(key, stringsOnly...), nil
	}
	return qdrant.NewMatchInts(key, intsOnly...), nil
}

func pointIDString(id *qdrant.PointId) string {
	if id == nil {
		return ""
	}
	if uuid := id.GetUuid(); uuid != "" {
		return uuid
	}
	return strconv.FormatUint(id.GetNum(), 10)
}

func payloadFromValues(values map[string]*qdrant.Value) map[string]any {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = payloadValue(value)
	}
	return out
}

func payloadValue(value *qdrant.Value) any {
	if value == nil {
		return nil
	}
	switch kind := value.GetKind().(type) {
	case *qdrant.Value_NullValue:
		return nil
	case *qdrant.Value_DoubleValue:
		return kind.DoubleValue
	case *qdrant.Value_IntegerValue:
		return kind.IntegerValue
	case *qdrant.Value_StringValue:
		return kind.StringValue
	case *qdrant.Value_BoolValue:
		return kind.BoolValue
	case *qdrant.Value_StructValue:
		fields := kind.StructValue.GetFields()
		return payloadFromValues(fields)
	case *qdrant.Value_ListValue:
		values := kind.ListValue.GetValues()
		out := make([]any, 0, len(values))
		for _, item := range values {
			out = append(out, payloadValue(item))
		}
		return out
	default:
		return nil
	}
}
