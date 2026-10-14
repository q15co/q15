package cognition

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// SemanticMigrationCursorArtifactPath is the cognition artifact that carries
// the semantic migration cursor between runs. It is stored under state/, so
// it cannot collide with a per-job state file, which lives under
// cognition/triggers/jobs/.
const SemanticMigrationCursorArtifactPath = "state/semantic_migration.json"

// semanticMigrationCursorVersion is the cursor format version. A cursor with
// a different version is rejected so a resuming run cannot misread an
// older-shaped file.
const semanticMigrationCursorVersion = 1

// SemanticMigrationFileCursor records how far the supervised semantic
// migration has walked one source file.
type SemanticMigrationFileCursor struct {
	// Offset is the byte offset, in the source file's coordinates as the
	// file stands now, where the next chunk starts. The migration deletes
	// converted lines only after they are ledgered, so the offset advances
	// by the bytes that actually left the file.
	Offset int64 `json:"offset"`
	// Hash is the SHA-256 of the file content as the migration last
	// recorded it: the file's hash when the pass opened the file, refreshed
	// after every rewrite the migration performs. A resumed run compares it
	// with the file's current hash and reports a mismatch instead of
	// converting across a file that moved under the migration.
	Hash string `json:"hash"`
	// Finished records the file as fully drained and rewritten.
	Finished bool `json:"finished,omitempty"`
}

// SemanticMigrationCursor is the migration's persisted cursor: which semantic
// source files the pass is walking and, per file, how far it has walked.
type SemanticMigrationCursor struct {
	// Version guards the cursor format.
	Version int `json:"version"`
	// Files is keyed by the repository-relative slash path of each source
	// file the pass has opened.
	Files map[string]SemanticMigrationFileCursor `json:"files"`
}

// SemanticMigrationArtifactStore is the slice of the persistence surface the
// migration needs: read, write and delete one job-owned cognition artifact.
// *memory.Store satisfies it.
type SemanticMigrationArtifactStore interface {
	LoadCognitionArtifact(ctx context.Context, relativePath string) (Artifact, error)
	StoreCognitionArtifact(ctx context.Context, artifact Artifact) error
	DeleteCognitionArtifact(ctx context.Context, relativePath string) error
}

// SemanticContentHash returns the SHA-256 of one source file's content, the
// hash the cursor records per file.
func SemanticContentHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// LoadSemanticMigrationCursor reads the cursor artifact. A missing cursor is
// not an error: it returns the zero cursor, which reports that no migration
// is running.
func LoadSemanticMigrationCursor(
	ctx context.Context,
	store SemanticMigrationArtifactStore,
) (SemanticMigrationCursor, error) {
	artifact, err := store.LoadCognitionArtifact(ctx, SemanticMigrationCursorArtifactPath)
	if err != nil {
		return SemanticMigrationCursor{}, err
	}
	cursor := SemanticMigrationCursor{Version: semanticMigrationCursorVersion}
	content := strings.TrimSpace(artifact.Content)
	if content == "" {
		return cursor, nil
	}
	if err := json.Unmarshal([]byte(content), &cursor); err != nil {
		return SemanticMigrationCursor{}, fmt.Errorf(
			"decode semantic migration cursor: %w",
			err,
		)
	}
	if cursor.Version != semanticMigrationCursorVersion {
		return SemanticMigrationCursor{}, fmt.Errorf(
			"semantic migration cursor version %d, want %d",
			cursor.Version,
			semanticMigrationCursorVersion,
		)
	}
	return cursor, nil
}

// SaveSemanticMigrationCursor persists the cursor artifact.
func SaveSemanticMigrationCursor(
	ctx context.Context,
	store SemanticMigrationArtifactStore,
	cursor SemanticMigrationCursor,
) error {
	cursor.Version = semanticMigrationCursorVersion
	data, err := json.MarshalIndent(cursor, "", "  ")
	if err != nil {
		return fmt.Errorf("encode semantic migration cursor: %w", err)
	}
	if err := store.StoreCognitionArtifact(ctx, Artifact{
		RelativePath: SemanticMigrationCursorArtifactPath,
		Content:      string(data) + "\n",
	}); err != nil {
		return fmt.Errorf("store semantic migration cursor: %w", err)
	}
	return nil
}

// ClearSemanticMigrationCursor deletes the cursor artifact. A cursor that is
// already absent counts as cleared, so finishing a migration twice is safe.
func ClearSemanticMigrationCursor(ctx context.Context, store SemanticMigrationArtifactStore) error {
	if err := store.DeleteCognitionArtifact(ctx, SemanticMigrationCursorArtifactPath); err != nil {
		return fmt.Errorf("clear semantic migration cursor: %w", err)
	}
	return nil
}

// SemanticMigrationCursorHasOpenFile reports whether a cursor exists and at
// least one source file is still unfinished. This is the trigger condition:
// no cursor, no run; a cursor whose files are all finished produces no run.
func SemanticMigrationCursorHasOpenFile(cursor SemanticMigrationCursor) bool {
	if cursor.Version != semanticMigrationCursorVersion || len(cursor.Files) == 0 {
		return false
	}
	for _, file := range cursor.Files {
		if !file.Finished {
			return true
		}
	}
	return false
}

// SemanticMigrationFileCursorFor returns the per-file record for one source
// path and whether the cursor tracks that file.
func SemanticMigrationFileCursorFor(
	cursor SemanticMigrationCursor,
	relativePath string,
) (SemanticMigrationFileCursor, bool) {
	file, ok := cursor.Files[relativePath]
	return file, ok
}

// SemanticMigrationSourcePaths returns the memory-relative slash paths of the
// three canonical semantic source files the migration walks, in the
// canonical drain order. The operator's start command creates the cursor over
// exactly this set; the three-source restriction itself lives in
// semanticMigrationSources.
func SemanticMigrationSourcePaths() []string {
	return append([]string(nil), semanticMigrationSources...)
}

// SemanticMigrationResidueSourcePath is the repository-relative slash path of
// the staged archive fold residue, the migration's fourth source. It is walked
// last, after the three canonical roots, and deleted on completion.
const SemanticMigrationResidueSourcePath = "working/archive-residue.md"

// SemanticMigrationCursorExtraSourcePaths returns the cursor-tracked paths
// that are not one of the canonical semantic roots, in sorted order. The
// migration walks these after the canonical sources, so the staged archive
// residue is always opened last.
func SemanticMigrationCursorExtraSourcePaths(cursor SemanticMigrationCursor) []string {
	var extras []string
	for relativePath := range cursor.Files {
		if isCanonicalMigrationSource(relativePath) {
			continue
		}
		extras = append(extras, relativePath)
	}
	sort.Strings(extras)
	return extras
}

// isCanonicalMigrationSource reports whether relativePath is one of the three
// canonical semantic roots.
func isCanonicalMigrationSource(relativePath string) bool {
	for _, source := range semanticMigrationSources {
		if source == relativePath {
			return true
		}
	}
	return false
}

// SemanticChunk is one bounded slice of a semantic source file, read for the
// migration pass so that no run ever carries a whole oversized file.
type SemanticChunk struct {
	// RelativePath is the repository-relative slash path the chunk came from.
	RelativePath string
	// Offset is the byte offset the chunk starts at.
	Offset int64
	// Content is the chunk, cut back to the last line boundary a chunk of
	// length bytes contains, so a caller never converts half an entry.
	Content string
	// Size is the file's size at read time.
	Size int64
	// More reports whether the file continues past this chunk.
	More bool
}

// SemanticLineHash hashes the content of one source line with its trailing
// newline excluded, so the last line of a file without a final newline hashes
// the same as it would with one. The migration uses it to link a ledger line
// to the exact source bytes it drained.
func SemanticLineHash(line string) string {
	return SemanticContentHash(strings.TrimSuffix(line, "\n"))
}
