package media

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSweepWebRetainsTranscriptSharedRecentAndOtherChannelMedia(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	old := now.Add(-48 * time.Hour)
	refs := make(map[string]string)
	for _, name := range []string{"orphan", "transcript", "shared", "recent", "telegram"} {
		path := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(path, []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
		scope := "web-conversation:" + name
		if name == "telegram" {
			scope = "telegram:1"
		}
		ref, err := store.Store(path, Meta{Filename: name}, scope)
		if err != nil {
			t.Fatal(err)
		}
		refs[name] = ref
		if name == "shared" {
			if _, err := store.Store(path, Meta{}, "telegram:shared"); err != nil {
				t.Fatal(err)
			}
		}
		if name != "recent" {
			if err := os.Chtimes(store.scopePath(scope), old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := store.SweepWeb(map[string]struct{}{refs["transcript"]: {}}, now.Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Resolve(refs["orphan"]); err == nil {
		t.Fatal("orphan kept")
	}
	for _, name := range []string{"transcript", "shared", "recent", "telegram"} {
		if _, _, err := store.Resolve(refs[name]); err != nil {
			t.Fatalf("%s lost: %v", name, err)
		}
	}
	if err := store.SweepWeb(map[string]struct{}{}, now.Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Resolve(refs["transcript"]); err == nil {
		t.Fatal("pruned transcript media kept")
	}
}
