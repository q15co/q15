package cognition

import (
	"context"
	"strings"
	"testing"
)

func TestSemanticMigrationCursorRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	loader := &migrationJobLoader{
		files: map[string]string{
			semanticFactsRelativePath: "# Facts\n",
		},
	}

	// A missing cursor is not an error: it reports no migration running.
	cursor, err := LoadSemanticMigrationCursor(ctx, loader)
	if err != nil {
		t.Fatalf("LoadSemanticMigrationCursor(missing) error = %v", err)
	}
	if SemanticMigrationCursorHasOpenFile(cursor) {
		t.Fatalf("missing cursor reports an open file")
	}

	cursor = SemanticMigrationCursor{
		Files: map[string]SemanticMigrationFileCursor{
			semanticFactsRelativePath: {Offset: 128, Hash: "aa11"},
		},
	}
	if err := SaveSemanticMigrationCursor(ctx, loader, cursor); err != nil {
		t.Fatalf("SaveSemanticMigrationCursor() error = %v", err)
	}
	if _, ok := loader.artifacts[SemanticMigrationCursorArtifactPath]; !ok {
		t.Fatalf(
			"cursor not stored at %q (artifacts=%v)",
			SemanticMigrationCursorArtifactPath,
			loader.artifacts,
		)
	}
	loaded, err := LoadSemanticMigrationCursor(ctx, loader)
	if err != nil {
		t.Fatalf("LoadSemanticMigrationCursor() error = %v", err)
	}
	entry, ok := SemanticMigrationFileCursorFor(loaded, semanticFactsRelativePath)
	if !ok {
		t.Fatalf("loaded cursor lost the tracked file: %+v", loaded)
	}
	if entry.Offset != 128 || entry.Hash != "aa11" || entry.Finished {
		t.Fatalf("loaded entry = %+v, want offset 128, hash aa11, unfinished", entry)
	}
	if !SemanticMigrationCursorHasOpenFile(loaded) {
		t.Fatalf("loaded cursor reports no open file")
	}

	// A cursor recording only finished files produces no run.
	loaded.Files[semanticFactsRelativePath] = SemanticMigrationFileCursor{
		Offset:   10,
		Hash:     "aa11",
		Finished: true,
	}
	if SemanticMigrationCursorHasOpenFile(loaded) {
		t.Fatalf("fully finished cursor reports an open file")
	}

	if err := ClearSemanticMigrationCursor(ctx, loader); err != nil {
		t.Fatalf("ClearSemanticMigrationCursor() error = %v", err)
	}
	if len(loader.artifacts) != 0 {
		t.Fatalf("clear left artifacts behind: %v", loader.artifacts)
	}
	// Clearing twice is safe, and a cleared cursor again reports nothing.
	if err := ClearSemanticMigrationCursor(ctx, loader); err != nil {
		t.Fatalf("ClearSemanticMigrationCursor(twice) error = %v", err)
	}
	if _, err := LoadSemanticMigrationCursor(ctx, loader); err != nil {
		t.Fatalf("LoadSemanticMigrationCursor(cleared) error = %v", err)
	}
}

func TestSemanticMigrationCursorRejectsUnknownVersions(t *testing.T) {
	t.Parallel()
	loader := &migrationJobLoader{}
	loader.artifacts = map[string]string{
		SemanticMigrationCursorArtifactPath: `{"version":99,"files":{}}`,
	}
	if _, err := LoadSemanticMigrationCursor(context.Background(), loader); err == nil {
		t.Fatalf("LoadSemanticMigrationCursor(version 99) = nil error, want rejection")
	}
}

func TestSemanticLineHashExcludesTheTrailingNewline(t *testing.T) {
	t.Parallel()
	if got := SemanticLineHash("same line\n"); got != SemanticLineHash("same line") {
		t.Fatalf("newline changed the line hash: %q vs %q", got, SemanticLineHash("same line"))
	}
	if SemanticLineHash("a") == SemanticLineHash("b") {
		t.Fatalf("different lines hashed equal")
	}
	if len(SemanticLineHash("a")) != 64 {
		t.Fatalf("line hash is not a 64-hex digest: %q", SemanticLineHash("a"))
	}
}

func TestSemanticMigrationTriggerGatesOnCursor(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	registration := NewSemanticMigrationRegistration()
	if got, want := len(registration.Policy.Schedule), 0; got != want {
		t.Fatalf("schedule rules = %d, want %d: the migration is operator-started", got, want)
	}
	if got, want := len(registration.Policy.State), 1; got != want {
		t.Fatalf("state rules = %d, want %d", got, want)
	}
	evaluate := registration.Policy.State[0].Evaluate
	if got, want := registration.Policy.State[0].ID, "semantic_migration_cursor_present"; got != want {
		t.Fatalf("state rule id = %q, want %q", got, want)
	}
	loader := &migrationJobLoader{
		files: map[string]string{
			semanticFactsRelativePath:       "# Semantic Facts\n\n## Confirmed Facts\n\n- None\n",
			semanticPreferencesRelativePath: "# Semantic Preferences\n",
			semanticProjectsRelativePath:    "# Semantic Projects\n",
		},
	}
	snapshot := Snapshot{Loader: loader}

	fired, _, err := evaluate(ctx, snapshot, JobState{})
	if err != nil {
		t.Fatalf("evaluate(no cursor) error = %v", err)
	}
	if fired {
		t.Fatalf("evaluate(no cursor) fired, want false: no cursor, no run")
	}

	open := SemanticMigrationCursor{
		Files: map[string]SemanticMigrationFileCursor{
			semanticFactsRelativePath: {Offset: 8},
		},
	}
	if err := SaveSemanticMigrationCursor(ctx, loader, open); err != nil {
		t.Fatalf("SaveSemanticMigrationCursor() error = %v", err)
	}
	fired, reason, err := evaluate(ctx, snapshot, JobState{})
	if err != nil {
		t.Fatalf("evaluate(cursor present) error = %v", err)
	}
	if !fired || strings.TrimSpace(reason) == "" {
		t.Fatalf("evaluate(cursor present) = %v %q, want fired with a reason", fired, reason)
	}

	// A cursor recording a finished file plus an unfinished one still runs,
	// and the finished file is skipped for that run.
	mixed := SemanticMigrationCursor{
		Files: map[string]SemanticMigrationFileCursor{
			semanticFactsRelativePath:       {Offset: 8, Finished: true},
			semanticPreferencesRelativePath: {Offset: 0},
		},
	}
	if err := SaveSemanticMigrationCursor(ctx, loader, mixed); err != nil {
		t.Fatalf("SaveSemanticMigrationCursor() error = %v", err)
	}
	fired, _, err = evaluate(ctx, snapshot, JobState{})
	if err != nil || !fired {
		t.Fatalf("evaluate(mixed cursor) = %v %v, want fired", fired, err)
	}
	job := registration.NewJob()
	spec, err := job.Build(ctx, loader)
	if err != nil {
		t.Fatalf("Build(mixed cursor) error = %v", err)
	}
	chunkSection := migrationPromptSection(t, spec, "migration_chunk")
	if got, want := chunkSection.Attributes["path"], semanticPreferencesRelativePath; got != want {
		t.Fatalf("mixed cursor processed %q, want the unfinished file %q", got, want)
	}

	finished := SemanticMigrationCursor{
		Files: map[string]SemanticMigrationFileCursor{
			semanticFactsRelativePath:       {Offset: 8, Finished: true},
			semanticPreferencesRelativePath: {Offset: 4, Finished: true},
		},
	}
	if err := SaveSemanticMigrationCursor(ctx, loader, finished); err != nil {
		t.Fatalf("SaveSemanticMigrationCursor() error = %v", err)
	}
	fired, _, err = evaluate(ctx, snapshot, JobState{})
	if err != nil {
		t.Fatalf("evaluate(finished cursor) error = %v", err)
	}
	if fired {
		t.Fatalf("evaluate(finished cursor) fired, want false")
	}
	if _, err := job.Build(ctx, loader); err == nil {
		t.Fatalf("Build(finished cursor) = nil error, want rejection")
	}

	// Unknown source paths fail loudly in Build rather than being ignored.
	bogus := SemanticMigrationCursor{
		Files: map[string]SemanticMigrationFileCursor{
			"semantic/elsewhere.md": {Offset: 0},
		},
	}
	if err := SaveSemanticMigrationCursor(ctx, loader, bogus); err != nil {
		t.Fatalf("SaveSemanticMigrationCursor() error = %v", err)
	}
	if _, err := job.Build(ctx, loader); err == nil {
		t.Fatalf("Build(unknown source) = nil error, want rejection")
	}
}

func TestSemanticMigrationCursorMissingFailsBuild(t *testing.T) {
	t.Parallel()
	job := NewSemanticMigrationRegistration().NewJob()
	if _, err := job.Build(context.Background(), &migrationJobLoader{}); err == nil {
		t.Fatalf("Build(no cursor) = nil error, want rejection")
	}
	if _, err := job.Build(context.Background(), nil); err == nil {
		t.Fatalf("Build(nil loader) = nil error, want rejection")
	}
}

func migrationPromptSection(t *testing.T, spec Spec, name string) PromptSectionContent {
	t.Helper()
	for _, section := range spec.PromptSections {
		if section.Name == name {
			return PromptSectionContent{
				Attributes: section.Attributes,
				Body:       section.Body,
			}
		}
	}
	t.Fatalf("prompt section %q missing from spec", name)
	return PromptSectionContent{}
}

// PromptSectionContent carries a prompt section's attributes and body so
// tests can inspect them without re-declaring agent.PromptSection.
type PromptSectionContent struct {
	Attributes map[string]string
	Body       string
}
