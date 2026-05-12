package embed

import "testing"

func TestEncodeSparseTextIsStableAndNormalized(t *testing.T) {
	first := encodeSparseText("Alpha beta beta, the gamma.")
	second := encodeSparseText("alpha beta beta gamma")

	if len(first.Indices) == 0 {
		t.Fatal("encodeSparseText() returned no sparse tokens")
	}
	if len(first.Indices) != len(first.Values) {
		t.Fatalf("indices len = %d, values len = %d", len(first.Indices), len(first.Values))
	}
	if len(first.Indices) != len(second.Indices) {
		t.Fatalf("stable token count = %d, want %d", len(second.Indices), len(first.Indices))
	}
	for i := range first.Indices {
		if first.Indices[i] != second.Indices[i] || first.Values[i] != second.Values[i] {
			t.Fatalf("sparse vector changed: %#v vs %#v", first, second)
		}
		if i > 0 && first.Indices[i-1] >= first.Indices[i] {
			t.Fatalf("indices are not sorted: %#v", first.Indices)
		}
	}
}

func TestEncodeSparseTextDropsOnlyStopWords(t *testing.T) {
	vector := encodeSparseText("the and of")
	if len(vector.Indices) != 0 {
		t.Fatalf("stop-word vector = %#v, want empty", vector)
	}
}
