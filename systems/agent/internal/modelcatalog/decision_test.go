package modelcatalog

import "testing"

func TestMergePreservesDecisionClassification(t *testing.T) {
	for _, models := range [][2]Model{
		{{ProviderModel: "model", Type: ModelTypeDecision}, {ProviderModel: "model"}},
		{{ProviderModel: "model"}, {ProviderModel: "model", Type: ModelTypeDecision}},
	} {
		merged := Merge([]Model{models[0]}, []Model{models[1]})
		if len(merged) != 1 || !merged[0].IsDecision() {
			t.Fatalf("classification lost: %+v", merged)
		}
	}
}
