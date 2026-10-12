package cli

import (
	"errors"
	"fmt"
	"testing"
)

func TestExitCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "success", err: nil, want: 0},
		{name: "usage", err: Usagef("unknown command %q", "bogus"), want: 2},
		{
			name: "usage behind a wrapper",
			err:  fmt.Errorf("running the verb: %w", Usagef("bad argument")),
			want: 2,
		},
		{
			name: "an inner error marked as usage",
			err:  AsUsage(errors.New("flag provided but not defined: -x")),
			want: 2,
		},
		{name: "runtime failure", err: errors.New("connection refused"), want: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ExitCode(test.err); got != test.want {
				t.Fatalf("ExitCode(%v) = %d, want %d", test.err, got, test.want)
			}
		})
	}
}

func TestUsagefCarriesTheMessage(t *testing.T) {
	err := Usagef("unknown command %q", "bogus")
	if err.Error() != `unknown command "bogus"` {
		t.Fatalf("Error() = %q, want %q", err.Error(), `unknown command "bogus"`)
	}
}

// TestAsUsageUnwraps is what lets a caller keep testing with errors.Is after the
// error has been classified.
func TestAsUsageUnwraps(t *testing.T) {
	inner := errors.New("flag provided but not defined: -x")

	if !errors.Is(AsUsage(inner), inner) {
		t.Fatal("errors.Is(AsUsage(inner), inner) = false, want true")
	}
}
