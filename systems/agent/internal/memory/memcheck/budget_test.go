package memcheck

import "testing"

// TestOverLimit pins the comparison every sized-file judge shares: exactly at
// the limit is within it, only strictly past it is over, whichever side the
// caller calls a budget or a cap.
func TestOverLimit(t *testing.T) {
	tests := []struct {
		name     string
		size     int64
		limit    int64
		wantOver bool
	}{
		{name: "at a positive limit is within", size: 8192, limit: 8192},
		{name: "one byte past the limit is over", size: 8193, limit: 8192, wantOver: true},
		{name: "under the limit is within", size: 4096, limit: 8192},
		{name: "zero size is within", size: 0, limit: 8192},
		{name: "zero limit and zero size is within", size: 0, limit: 0},
		{name: "negative size is within a zero limit", size: -1, limit: 0},
		{name: "negative size past a negative limit is over", size: -3, limit: -4, wantOver: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := OverLimit(test.size, test.limit); got != test.wantOver {
				t.Fatalf(
					"OverLimit(%d, %d) = %v, want %v",
					test.size,
					test.limit,
					got,
					test.wantOver,
				)
			}
		})
	}
}
