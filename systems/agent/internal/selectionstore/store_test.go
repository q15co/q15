package selectionstore

import (
	"os"
	"path/filepath"
	"strings"
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

func TestDecisionSlotRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "selection.json")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if store.Decision().IsValid() {
		t.Fatal("decision slot non-empty on first run")
	}
	if err := store.SetInteractive("p", "chat"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetDecision("p", "clef"); err != nil {
		t.Fatalf("SetDecision() error = %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	if got := reopened.Interactive(); got.Provider != "p" || got.Model != "chat" {
		t.Fatalf("reopened interactive = %+v", got)
	}
	if got := reopened.Decision(); got.Provider != "p" || got.Model != "clef" {
		t.Fatalf("reopened decision = %+v", got)
	}
	// A later slot write keeps the sibling slots.
	if err := reopened.SetDecision("p", "clef-flash"); err != nil {
		t.Fatal(err)
	}
	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := again.Decision(); got.Model != "clef-flash" {
		t.Fatalf("decision after rewrite = %+v", got)
	}
	if got := again.Interactive(); got.Model != "chat" {
		t.Fatalf("interactive lost after decision rewrite: %+v", got)
	}
}

func TestSlotSetRequiresProviderAndModel(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "selection.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetDecision("p", ""); err == nil ||
		!strings.Contains(err.Error(), "decision selection requires provider and model") {
		t.Fatalf("SetDecision() error = %v", err)
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

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}
