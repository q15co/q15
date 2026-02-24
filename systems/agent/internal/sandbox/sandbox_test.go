package sandbox

import (
	"context"
	"reflect"
	"testing"
)

func TestHelperCommandInvokesHelperDirectly(t *testing.T) {
	t.Parallel()

	cmd := helperCommand(context.Background(), "/tmp/q15-sandbox-helper", "prepare")

	if got, want := cmd.Path, "/tmp/q15-sandbox-helper"; got != want {
		t.Fatalf("cmd.Path = %q, want %q", got, want)
	}
	if got, want := cmd.Args, []string{"/tmp/q15-sandbox-helper", "prepare"}; !reflect.DeepEqual(
		got,
		want,
	) {
		t.Fatalf("cmd.Args = %v, want %v", got, want)
	}
}
