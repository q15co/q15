package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/command"
)

func TestMemoryUsageAndFlags(t *testing.T) {
	env, stdout, _ := newEnv(&fakeExec{})
	if err := Run(context.Background(), env, []string{"memory"}); err != nil {
		t.Fatal(err)
	}
	for _, verb := range []string{"q15 memory import", "q15 memory backfill", "q15 memory status"} {
		if !strings.Contains(stdout.String(), verb) {
			t.Fatalf("missing %s", verb)
		}
	}
	for _, args := range [][]string{{"memory", "bogus"}, {"memory", "import"}, {"memory", "status", "--verify"}, {"memory", "backfill", "--batches", "-1"}, {"memory", "backfill", "--summary-workers", "0"}, {"memory", "backfill", "--summary-workers", "9"}, {"memory", "status", "extra"}, {"memory", "status", "--memory-root", "legacy"}} {
		err := Run(context.Background(), env, args)
		if err == nil || command.ExitCode(err) != 2 {
			t.Fatalf("%v: expected usage error, got %v", args, err)
		}
	}
}

func TestMemoryDatabaseSecretRequired(t *testing.T) {
	t.Setenv("Q15_DATABASE_URL", "")
	t.Setenv("Q15_DATABASE_URL_FILE", "")
	env, _, _ := newEnv(&fakeExec{})
	err := Run(context.Background(), env, []string{"memory", "status"})
	if err == nil || !strings.Contains(err.Error(), "Q15_DATABASE_URL") {
		t.Fatalf("expected missing database connection: %v", err)
	}
}
