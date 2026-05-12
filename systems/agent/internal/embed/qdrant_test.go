package embed

import "testing"

func TestQueryPointsForHybridSearchUsesPrefetchFusion(t *testing.T) {
	query, err := queryPointsForSearch(CollectionCore, SearchRequest{
		Vector: []float32{0.1, 0.2},
		Sparse: SparseVector{
			Indices: []uint32{1, 2},
			Values:  []float32{0.5, 0.5},
		},
		Mode:  SearchModeHybrid,
		Limit: 3,
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
	if query.GetQuery().GetRrf() == nil {
		t.Fatalf("query = %#v, want RRF fusion", query.GetQuery())
	}
}

func TestQueryPointsForSparseSearchRejectsEmptySparseVector(t *testing.T) {
	_, err := queryPointsForSearch(CollectionCore, SearchRequest{
		Mode:  SearchModeSparse,
		Limit: 10,
	}, nil, SearchModeSparse)
	if err == nil || err.Error() != "sparse search requires at least one sparse query token" {
		t.Fatalf("queryPointsForSearch() error = %v, want sparse-token error", err)
	}
}

func TestSparseVectorsConfigUsesLexicalSparseName(t *testing.T) {
	cfg := sparseVectorsConfig()
	if _, ok := cfg.GetMap()[VectorNameSparse]; !ok {
		t.Fatalf("sparse config map = %#v, want %q", cfg.GetMap(), VectorNameSparse)
	}
}
