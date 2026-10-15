package cognition

import (
	"context"
	"strings"
	"testing"
)

// TestSemanticMigrationKeepsALineItDidNotLedger covers the drain at the level
// the ledger invariant lives at: one chunk holds two byte-identical lines and
// the run ledgers only three of its four bullets, so the uncited copy of the
// duplicated line is never named. It must keep its bytes anyway, because only
// the offsets a ledger line cites may leave the source file.
func TestSemanticMigrationKeepsALineItDidNotLedger(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	duplicate := "- identical legacy bullet about the deploy target of project X\n"
	content := "# Semantic Facts\n\n## Confirmed Facts\n\n" +
		"- legacy bullet 001 about the deploy target of project X\n" +
		duplicate +
		"- legacy bullet 003 about the deploy target of project X\n" +
		duplicate
	loader := &migrationJobLoader{
		files: map[string]string{
			semanticFactsRelativePath: content,
			semanticPreferencesRelativePath: "# Semantic Preferences\n" +
				"\n## User Preferences\n\n- None\n",
			semanticProjectsRelativePath: "# Semantic Projects\n\n## Active Projects\n\n- None\n",
		},
		topics: map[string]string{},
	}
	migrationCursorFor(t, loader, semanticFactsRelativePath, semanticPreferencesRelativePath)
	job := NewSemanticMigrationRegistration().NewJob()

	spec, err := job.Build(ctx, loader)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got := len(migrationChunkBullets(t, spec)); got != 4 {
		t.Fatalf("chunk bullets = %d, want 4", got)
	}
	output := migrationFakeModelOutput(t, loader, spec)

	// The ledger is cut back to the three lines the run actually recorded, so
	// the second copy of the duplicated line is never cited.
	appended := strings.Split(strings.TrimSpace(loader.ledger), "\n")
	if len(appended) != 4 {
		t.Fatalf("scripted ledger lines = %d, want 4", len(appended))
	}
	loader.ledger = strings.Join(appended[:3], "\n")

	if _, err := job.ApplyResult(ctx, loader, output); err != nil {
		t.Fatalf("ApplyResult() error = %v", err)
	}

	facts := loader.files[semanticFactsRelativePath]
	if got := strings.Count(facts, duplicate); got != 1 {
		t.Fatalf("uncited duplicate survives %d times, want exactly 1: %q", got, facts)
	}
	// The uncited copy is the last bullet left in the file, and the cursor
	// stops in front of it instead of reporting the source drained.
	survivor := int64(strings.Index(facts, duplicate))
	cursor, err := LoadSemanticMigrationCursor(ctx, loader)
	if err != nil {
		t.Fatalf("LoadSemanticMigrationCursor() error = %v", err)
	}
	entry, ok := SemanticMigrationFileCursorFor(cursor, semanticFactsRelativePath)
	if !ok {
		t.Fatalf("cursor lost the facts entry: %+v", cursor)
	}
	if got, want := entry.Offset, survivor; got != want {
		t.Fatalf("cursor offset = %d, want %d (the surviving bullet)", got, want)
	}
	if entry.Finished {
		t.Fatalf("cursor reports the source finished with a bullet still in it")
	}
}
