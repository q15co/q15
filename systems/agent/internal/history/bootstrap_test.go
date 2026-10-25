package history

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/history/historydb"
)

func TestCutoverRejectsUnpreparedLegacyHistory(t *testing.T) {
	store := testStore(t)
	indexer, err := NewIndexer(
		t.Context(),
		store,
		EmbeddingSpace{Provider: "fixture", Model: "bootstrap", Dimensions: 3},
		&testEmbedder{},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "history/state"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "history/state/head.json"), []byte(`{"last_seq":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := indexer.RequireReady(t.Context(), root); err == nil {
		t.Fatal("legacy root incorrectly bootstrapped an empty database")
	}
	if err := indexer.PrepareCutover(t.Context()); err == nil {
		t.Fatal("unverified source incorrectly prepared")
	}
}

func TestFreshAgentBootstrapsAndChangedSpaceRequiresPreparation(t *testing.T) {
	store := testStore(t)
	indexer, err := NewIndexer(
		t.Context(),
		store,
		EmbeddingSpace{Provider: "fixture", Model: "empty", Dimensions: 3},
		&testEmbedder{},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := indexer.RequireReady(t.Context(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := indexer.RequireReady(t.Context(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	alternate, err := NewIndexer(
		t.Context(),
		store,
		EmbeddingSpace{Provider: "fixture", Model: "changed-space", Dimensions: 3},
		&testEmbedder{},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := alternate.RequireReady(t.Context(), t.TempDir()); err == nil {
		t.Fatal("embedding space changed without preparation")
	}
	appendTestTurn(t, store, "A new turn must receive a digest before preparation.")
	if err := alternate.PrepareCutover(t.Context()); err == nil {
		t.Fatal("missing derived data passed preparation")
	}
}

func TestReadOnlyConnectionCannotWrite(t *testing.T) {
	store := testStore(t)
	readonly, err := OpenReadOnly(t.Context(), os.Getenv("Q15_TEST_POSTGRES_URL"), t.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer readonly.Close()
	if _, err := readonly.Coverage(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := readonly.ReserveTurnSeq(t.Context()); err == nil {
		t.Fatal("read-only connection accepted a write")
	}
	if _, err := historydb.New(store.pool).CountTurns(t.Context(), store.agentID); err != nil {
		t.Fatal(err)
	}
}
