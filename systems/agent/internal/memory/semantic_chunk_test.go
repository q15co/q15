package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/cognition"
)

func newSemanticChunkStore(t *testing.T) (*Store, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "memory")
	store := newTestStore(root, &fakeCommitter{})
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	return store, root
}

func writeSemanticSource(t *testing.T, root string, relativePath string, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", path, err)
	}
}

// migrationSourceLines builds numbered whole lines of a fixed width so chunk
// boundaries are predictable in tests.
func migrationSourceLines(count int) string {
	var builder strings.Builder
	for i := 0; i < count; i++ {
		builder.WriteString(strings.Repeat("=", 7))
		builder.WriteString(" ")
		builder.WriteString(strings.Repeat(string(rune('a'+i%26)), 6))
		builder.WriteString("\n")
	}
	return builder.String()
}

func TestStoreLoadSemanticChunkCutsBackToLineBoundaries(t *testing.T) {
	t.Parallel()
	store, root := newSemanticChunkStore(t)
	content := migrationSourceLines(20)
	writeSemanticSource(t, root, semanticFactsRelativePath, content)

	chunk, err := store.LoadSemanticChunk(
		context.Background(),
		semanticFactsRelativePath,
		0,
		50,
	)
	if err != nil {
		t.Fatalf("LoadSemanticChunk() error = %v", err)
	}
	// The 50-byte window cuts back to the last whole line it holds: 15-byte
	// lines mean three lines (45 bytes) fit, the fourth does not.
	if !strings.HasSuffix(chunk.Content, "\n") {
		t.Fatalf("chunk content = %q, want a whole line ending in a newline", chunk.Content)
	}
	if got, want := len(chunk.Content), 3*15; got != want {
		t.Fatalf("chunk content length = %d, want %d (three whole 15-byte lines)", got, want)
	}
	if got, want := chunk.Offset, int64(0); got != want {
		t.Fatalf("chunk offset = %d, want %d", got, want)
	}
	if got, want := chunk.Size, int64(len(content)); got != want {
		t.Fatalf("chunk size = %d, want %d", got, want)
	}
	if !chunk.More {
		t.Fatalf("chunk.More = false, want true with a 300-byte file and 50-byte window")
	}
	// Mid-file offsets still end only at line boundaries.
	chunk, err = store.LoadSemanticChunk(
		context.Background(),
		semanticFactsRelativePath,
		8,
		40,
	)
	if err != nil {
		t.Fatalf("LoadSemanticChunk(offset 8) error = %v", err)
	}
	if !strings.HasSuffix(chunk.Content, "\n") {
		t.Fatalf("mid-file chunk = %q, want a whole line ending", chunk.Content)
	}
	if !strings.HasPrefix(chunk.Content, "aaaaaa") {
		t.Fatalf("mid-file chunk = %q, want it to start inside line zero's letters", chunk.Content)
	}
	if got, want := len(chunk.Content), 37; got != want {
		t.Fatalf(
			"mid-file chunk length = %d, want %d (offset 8 through line two's newline)",
			got,
			want,
		)
	}
	// A tail window holding only the final unterminated line is that line.
	tail, err := store.LoadSemanticChunk(
		context.Background(),
		semanticFactsRelativePath,
		chunk.Size-8,
		8,
	)
	if err != nil {
		t.Fatalf("LoadSemanticChunk(tail) error = %v", err)
	}
	if tail.More {
		t.Fatalf("tail chunk reports More, want false at end of file")
	}
	if !strings.HasSuffix(strings.TrimSpace(tail.Content), "t") {
		t.Fatalf("tail chunk = %q, want the final line", tail.Content)
	}
}

func TestStoreLoadSemanticChunkRejectsUnboundedWindows(t *testing.T) {
	t.Parallel()
	store, root := newSemanticChunkStore(t)
	// One line longer than the window, followed by later content: the chunk
	// must be empty and report more, never a half line.
	head := strings.Repeat("a", 40) + "\n"
	writeSemanticSource(t, root, semanticFactsRelativePath, head+"tail line\n")

	chunk, err := store.LoadSemanticChunk(
		context.Background(),
		semanticFactsRelativePath,
		0,
		16,
	)
	if err != nil {
		t.Fatalf("LoadSemanticChunk() error = %v", err)
	}
	if chunk.Content != "" {
		t.Fatalf("chunk content = %q, want empty: the window holds no line boundary", chunk.Content)
	}
	if !chunk.More {
		t.Fatalf("chunk.More = false, want true: the file continues past the window")
	}
	// A file that ends mid-line is that whole line, not a half line.
	writeSemanticSource(t, root, semanticPreferencesRelativePath, "abc")
	chunk, err = store.LoadSemanticChunk(
		context.Background(),
		semanticPreferencesRelativePath,
		0,
		16,
	)
	if err != nil {
		t.Fatalf("LoadSemanticChunk(odd tail) error = %v", err)
	}
	if got, want := chunk.Content, "abc"; got != want {
		t.Fatalf("odd tail chunk = %q, want %q", got, want)
	}
	if chunk.More {
		t.Fatalf("odd tail chunk reports More, want false")
	}
}

func TestStoreLoadSemanticChunkRestrictedToSources(t *testing.T) {
	t.Parallel()
	store, root := newSemanticChunkStore(t)
	ctx := context.Background()
	for _, relativePath := range []string{
		"",
		"semantic/topics/x.md",
		"core/AGENT.md",
		"../secrets.md",
	} {
		if _, err := store.LoadSemanticChunk(ctx, relativePath, 0, 16); err == nil {
			t.Fatalf("LoadSemanticChunk(%q) = nil error, want rejection", relativePath)
		}
	}
	if _, err := store.LoadSemanticChunk(ctx, semanticFactsRelativePath, 0, 0); err == nil {
		t.Fatalf("LoadSemanticChunk(length 0) = nil error, want rejection")
	}
	// A missing source file is an empty chunk, not an error.
	if err := os.Remove(filepath.Join(root, "semantic", "projects.md")); err != nil {
		t.Fatalf("Remove(projects) error = %v", err)
	}
	chunk, err := store.LoadSemanticChunk(ctx, semanticProjectsRelativePath, 0, 16)
	if err != nil {
		t.Fatalf("LoadSemanticChunk(missing) error = %v", err)
	}
	if chunk.Content != "" || chunk.More || chunk.Size != 0 {
		t.Fatalf("missing source chunk = %+v, want empty with no more and zero size", chunk)
	}
}

func TestStoreRemoveSemanticLinesDrainsExactlyTheCitedLines(t *testing.T) {
	t.Parallel()
	store, root := newSemanticChunkStore(t)
	ctx := context.Background()
	bullet := "- a legacy bullet about deploy targets\n"
	other := "- a different legacy bullet\n"
	// The same line appears twice: only the offset the caller cites may leave
	// the file, so the second copy keeps its bytes. A drain that took a set of
	// hashes instead would have removed both.
	content := "# Semantic Facts\n\n## Grounded Inferences\n\n" + bullet + other + bullet
	writeSemanticSource(t, root, semanticFactsRelativePath, content)
	citedOffset := int64(strings.Index(content, bullet))
	if citedOffset == int64(strings.LastIndex(content, bullet)) {
		t.Fatalf("fixture placed the bullet once, want it twice")
	}
	cited := cognition.SemanticDrainLine{
		ByteOffset: citedOffset,
		Hash:       cognition.SemanticLineHash(bullet),
	}

	newSize, err := store.RemoveSemanticLines(
		ctx,
		semanticFactsRelativePath,
		[]cognition.SemanticDrainLine{cited},
	)
	if err != nil {
		t.Fatalf("RemoveSemanticLines() error = %v", err)
	}
	want := content[:citedOffset] + content[citedOffset+int64(len(bullet)):]
	if !strings.Contains(want, bullet) {
		t.Fatalf("fixture lost the uncited duplicate: %q", want)
	}
	data, err := os.ReadFile(filepath.Join(root, "semantic", "facts.md"))
	if err != nil {
		t.Fatalf("ReadFile(facts) error = %v", err)
	}
	if string(data) != want {
		t.Fatalf("drained file = %q, want %q", string(data), want)
	}
	if got, wantSize := newSize, int64(len(want)); got != wantSize {
		t.Fatalf("new size = %d, want %d", got, wantSize)
	}

	// A citation that does not name a line of the file as it stands fails
	// loudly and leaves every byte in place.
	rejected := []struct {
		name  string
		path  string
		lines []cognition.SemanticDrainLine
	}{
		{"no cited lines", semanticFactsRelativePath, nil},
		{
			"offset begins no line",
			semanticFactsRelativePath,
			[]cognition.SemanticDrainLine{{
				ByteOffset: citedOffset + 1,
				Hash:       cognition.SemanticLineHash(bullet),
			}},
		},
		{
			"offset past the end",
			semanticFactsRelativePath,
			[]cognition.SemanticDrainLine{{
				ByteOffset: int64(len(want)) + 1,
				Hash:       cognition.SemanticLineHash(bullet),
			}},
		},
		{
			"hash does not match the line",
			semanticFactsRelativePath,
			[]cognition.SemanticDrainLine{{
				ByteOffset: citedOffset,
				Hash:       cognition.SemanticLineHash("absent line\n"),
			}},
		},
		{
			"empty hash",
			semanticFactsRelativePath,
			[]cognition.SemanticDrainLine{{ByteOffset: citedOffset, Hash: "  "}},
		},
		{
			"negative offset",
			semanticFactsRelativePath,
			[]cognition.SemanticDrainLine{{
				ByteOffset: -1,
				Hash:       cognition.SemanticLineHash(bullet),
			}},
		},
		{
			"repeated offset",
			semanticFactsRelativePath,
			[]cognition.SemanticDrainLine{cited, cited},
		},
		{"non-source path", "semantic/topics/x.md", []cognition.SemanticDrainLine{cited}},
	}
	for _, tc := range rejected {
		if _, err := store.RemoveSemanticLines(ctx, tc.path, tc.lines); err == nil {
			t.Fatalf("RemoveSemanticLines(%s) = nil error, want failure", tc.name)
		}
		data, err := os.ReadFile(filepath.Join(root, "semantic", "facts.md"))
		if err != nil {
			t.Fatalf("ReadFile(facts) error = %v", err)
		}
		if string(data) != want {
			t.Fatalf("file changed after a rejected drain (%s): %q", tc.name, string(data))
		}
	}
}
