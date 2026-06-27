package selectionstore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenMissingFileIsEmpty(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "selection.json"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if store.HasInteractive() {
		t.Fatal("HasInteractive() = true, want false on first run")
	}
	if got := store.Interactive(); got.IsValid() {
		t.Fatalf("Interactive() = %+v, want empty", got)
	}
}

func TestSetInteractivePersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "selection.json")

	first, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if err := first.SetInteractive("ollama-cloud", "kimi-k2.7-code"); err != nil {
		t.Fatalf("SetInteractive() error = %v", err)
	}
	if got := first.Interactive(); got.Provider != "ollama-cloud" || got.Model != "kimi-k2.7-code" {
		t.Fatalf("Interactive() = %+v", got)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	if got := reopened.Interactive(); got.Provider != "ollama-cloud" ||
		got.Model != "kimi-k2.7-code" {
		t.Fatalf("reopened Interactive() = %+v, want persisted pair", got)
	}
}

func TestCognitionOverridesRoundTripAndClear(t *testing.T) {
	path := filepath.Join(t.TempDir(), "selection.json")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	if _, ok := store.Cognition("working_memory.consolidate"); ok {
		t.Fatal("missing cognition override reported as present")
	}
	if err := store.SetCognition("working_memory.consolidate", "moonshot", "kimi-k2"); err != nil {
		t.Fatalf("SetCognition() error = %v", err)
	}
	if err := store.SetCognition("semantic_memory.extract", "openai", "gpt-5.5"); err != nil {
		t.Fatalf("SetCognition() error = %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	got, ok := reopened.Cognition("working_memory.consolidate")
	if !ok || got.Provider != "moonshot" || got.Model != "kimi-k2" {
		t.Fatalf("reopened cognition override = %+v ok=%v", got, ok)
	}
	if len(reopened.CognitionSelections()) != 2 {
		t.Fatalf("cognition overrides = %d, want 2", len(reopened.CognitionSelections()))
	}

	if err := reopened.ClearCognition("working_memory.consolidate"); err != nil {
		t.Fatalf("ClearCognition() error = %v", err)
	}
	if _, ok := reopened.Cognition("working_memory.consolidate"); ok {
		t.Fatal("cleared cognition override still present")
	}
}

func TestRejectsNewerSchemaVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "selection.json")
	if err := writeFile(path, `{"schema_version": 99}`); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("expected error for newer schema version")
	}
}

// TestSetInteractiveLeavesMemoryUnchangedOnPersistFailure guards against a
// persistence failure (here simulated by an unwritable directory) leaving the
// in-process store advanced past the on-disk state.
func TestSetInteractiveLeavesMemoryUnchangedOnPersistFailure(t *testing.T) {
	// A path whose parent cannot be created makes the atomic write fail.
	store, err := Open(filepath.Join(t.TempDir(), "selection.json"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	store.path = "/dev/null/cannot-create/selection.json"

	if err := store.SetInteractive("p", "m"); err == nil {
		t.Fatal("expected SetInteractive to fail on unwritable path")
	}
	if store.HasInteractive() {
		t.Fatalf(
			"in-memory interactive advanced despite persistence failure: %+v",
			store.Interactive(),
		)
	}
}

func TestSetCognitionLeavesMemoryUnchangedOnPersistFailure(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "selection.json"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if err := store.SetCognition("working_memory.consolidate", "p", "m"); err != nil {
		t.Fatalf("seed SetCognition() error = %v", err)
	}
	store.path = "/dev/null/cannot-create/selection.json"

	if err := store.SetCognition("semantic_memory.extract", "q", "n"); err == nil {
		t.Fatal("expected SetCognition to fail on unwritable path")
	}
	overrides := store.CognitionSelections()
	if _, ok := overrides["semantic_memory.extract"]; ok {
		t.Fatal("in-memory cognition advanced despite persistence failure")
	}
	if _, ok := overrides["working_memory.consolidate"]; !ok {
		t.Fatal("pre-existing cognition override lost on failed sibling write")
	}
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}
