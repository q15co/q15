package embed

import (
	"testing"

	"github.com/qdrant/go-client/qdrant"
)

func TestQueryPointsForHybridSearchUsesPrefetchFusion(t *testing.T) {
	query, err := queryPointsForSearch(CollectionCore, SearchRequest{
		Vector:     []float32{0.1, 0.2},
		SparseText: "alpha beta",
		Mode:       SearchModeHybrid,
		Limit:      3,
	}, nil, SearchModeHybrid)
	if err != nil {
		t.Fatalf("queryPointsForSearch() error = %v", err)
	}
	if got, want := len(query.GetPrefetch()), 2; got != want {
		t.Fatalf("prefetch len = %d, want %d", got, want)
	}
	if query.GetPrefetch()[0].GetUsing() != VectorNameDense ||
		query.GetPrefetch()[1].GetUsing() != VectorNameSparse {
		t.Fatalf("prefetch vector names = %#v, want dense then sparse", query.GetPrefetch())
	}
	sparseDoc := query.GetPrefetch()[1].GetQuery().GetNearest().GetDocument()
	if sparseDoc.GetModel() != SparseModelBM25 || sparseDoc.GetText() != "alpha beta" {
		t.Fatalf("sparse prefetch doc = %#v, want BM25 document", sparseDoc)
	}
	if query.GetQuery().GetRrf() == nil {
		t.Fatalf("query = %#v, want RRF fusion", query.GetQuery())
	}
}

func TestQueryPointsForSparseSearchRejectsEmptyQueryText(t *testing.T) {
	_, err := queryPointsForSearch(CollectionCore, SearchRequest{
		Mode:  SearchModeSparse,
		Limit: 10,
	}, nil, SearchModeSparse)
	if err == nil || err.Error() != "sparse search requires query text" {
		t.Fatalf("queryPointsForSearch() error = %v, want sparse-text error", err)
	}
}

func TestSparseVectorsConfigUsesBM25SparseName(t *testing.T) {
	cfg := sparseVectorsConfig()
	params, ok := cfg.GetMap()[VectorNameSparse]
	if !ok {
		t.Fatalf("sparse config map = %#v, want %q", cfg.GetMap(), VectorNameSparse)
	}
	if params.GetModifier() != qdrant.Modifier_Idf {
		t.Fatalf("sparse modifier = %v, want idf", params.GetModifier())
	}
}

func TestCollectionHasExpectedVectorSchemaRequiresDenseAndSparse(t *testing.T) {
	info := collectionInfoForTest(DefaultEmbeddingDimensions, sparseVectorsConfig())
	if !collectionHasExpectedVectorSchema(info, DefaultEmbeddingDimensions) {
		t.Fatal("collectionHasExpectedVectorSchema() = false, want true")
	}

	missingSparse := collectionInfoForTest(DefaultEmbeddingDimensions, nil)
	if collectionHasExpectedVectorSchema(missingSparse, DefaultEmbeddingDimensions) {
		t.Fatal("collectionHasExpectedVectorSchema() = true without sparse config, want false")
	}

	wrongDimensions := collectionInfoForTest(DefaultEmbeddingDimensions+1, sparseVectorsConfig())
	if collectionHasExpectedVectorSchema(wrongDimensions, DefaultEmbeddingDimensions) {
		t.Fatal("collectionHasExpectedVectorSchema() = true with wrong dimensions, want false")
	}
}

func TestVectorMapForPointUsesBM25DocumentSparseVector(t *testing.T) {
	vectors := vectorMapForPoint(Point{
		Vector:     []float32{0.1, 0.2},
		SparseText: "library chunk",
	})
	doc := vectors.GetVectors().GetVectors()[VectorNameSparse].GetDocument()
	if doc.GetModel() != SparseModelBM25 || doc.GetText() != "library chunk" {
		t.Fatalf("sparse vector doc = %#v, want BM25 document", doc)
	}
}

func collectionInfoForTest(
	dimensions int,
	sparseConfig *qdrant.SparseVectorConfig,
) *qdrant.CollectionInfo {
	return &qdrant.CollectionInfo{
		Config: &qdrant.CollectionConfig{
			Params: &qdrant.CollectionParams{
				VectorsConfig: qdrant.NewVectorsConfigMap(map[string]*qdrant.VectorParams{
					VectorNameDense: {
						Size:     uint64(dimensions),
						Distance: qdrant.Distance_Cosine,
					},
				}),
				SparseVectorsConfig: sparseConfig,
			},
		},
	}
}
