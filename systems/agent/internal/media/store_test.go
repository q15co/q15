package media

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/conversation"
)

func TestNewFileStoreRequiresAbsoluteRoot(t *testing.T) {
	if _, err := NewFileStore("relative"); err == nil {
		t.Fatal("NewFileStore() error = nil, want non-nil")
	}
}

func TestFileStoreStoreResolveAndReleaseAll(t *testing.T) {
	root := filepath.Join(t.TempDir(), "media")
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	sourcePath := filepath.Join(t.TempDir(), "image.png")
	if err := os.WriteFile(sourcePath, testPNGBytes, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	ref, err := store.Store(sourcePath, Meta{ContentType: "image/png"}, "scope-a")
	if err != nil {
		t.Fatalf("Store() error = %v", err)
	}
	refAgain, err := store.Store(sourcePath, Meta{ContentType: "image/png"}, "scope-b")
	if err != nil {
		t.Fatalf("Store() second error = %v", err)
	}
	if refAgain != ref {
		t.Fatalf("Store() second ref = %q, want %q", refAgain, ref)
	}

	objectPath, meta, err := store.Resolve(ref)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if meta.ContentType != "image/png" {
		t.Fatalf("meta.ContentType = %q, want image/png", meta.ContentType)
	}
	if _, err := os.Stat(objectPath); err != nil {
		t.Fatalf("stored object missing: %v", err)
	}

	if err := store.ReleaseAll("scope-a"); err != nil {
		t.Fatalf("ReleaseAll(scope-a) error = %v", err)
	}
	if _, _, err := store.Resolve(ref); err != nil {
		t.Fatalf("Resolve() after releasing one scope error = %v", err)
	}

	if err := store.ReleaseAll("scope-b"); err != nil {
		t.Fatalf("ReleaseAll(scope-b) error = %v", err)
	}
	if _, _, err := store.Resolve(ref); err == nil {
		t.Fatal("Resolve() error = nil after releasing all scopes, want non-nil")
	}
}

func TestResolveImagePartDataURLPassesThroughDataURL(t *testing.T) {
	got, err := ResolveImagePartDataURL(
		conversation.Image("", "data:image/png;base64,abcd"),
		nil,
		DefaultMaxImageBytes,
	)
	if err != nil {
		t.Fatalf("ResolveImagePartDataURL() error = %v", err)
	}
	if got != "data:image/png;base64,abcd" {
		t.Fatalf("data URL = %q, want pass-through", got)
	}
}

func TestResolveImagePartDataURLUsesStoredImage(t *testing.T) {
	store, ref := mustStoreTestImage(t)

	got, err := ResolveImagePartDataURL(
		conversation.Image(ref, ""),
		store,
		DefaultMaxImageBytes,
	)
	if err != nil {
		t.Fatalf("ResolveImagePartDataURL() error = %v", err)
	}

	want := "data:image/png;base64," + base64.StdEncoding.EncodeToString(testPNGBytes)
	if got != want {
		t.Fatalf("data URL = %q, want %q", got, want)
	}
}

func TestResolveImagePartDataURLRejectsNonImageMedia(t *testing.T) {
	root := filepath.Join(t.TempDir(), "media")
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	sourcePath := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(sourcePath, []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	ref, err := store.Store(sourcePath, Meta{ContentType: "text/plain"}, "scope")
	if err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	_, err = ResolveImagePartDataURL(conversation.Image(ref, ""), store, DefaultMaxImageBytes)
	if err == nil || !strings.Contains(err.Error(), "is not an image") {
		t.Fatalf("ResolveImagePartDataURL() error = %v, want non-image failure", err)
	}
}

func TestResolveImagePartDataURLRejectsOversizedImage(t *testing.T) {
	store, ref := mustStoreTestImage(t)

	_, err := ResolveImagePartDataURL(conversation.Image(ref, ""), store, 8)
	if err == nil || !strings.Contains(err.Error(), "exceeds maximum size") {
		t.Fatalf("ResolveImagePartDataURL() error = %v, want size limit failure", err)
	}
}

var testPNGBytes = []byte{
	0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n',
	0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R',
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
	0x89, 0x00, 0x00, 0x00, 0x0d, 'I', 'D', 'A', 'T',
	0x78, 0x9c, 0x63, 0xf8, 0xcf, 0xc0, 0x00, 0x00,
	0x03, 0x01, 0x01, 0x00, 0xc9, 0xfe, 0x92, 0xef,
	0x00, 0x00, 0x00, 0x00, 'I', 'E', 'N', 'D',
	0xae, 0x42, 0x60, 0x82,
}

func mustStoreTestImage(t *testing.T) (*FileStore, string) {
	t.Helper()

	root := filepath.Join(t.TempDir(), "media")
	store, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	sourcePath := filepath.Join(t.TempDir(), "image.png")
	if err := os.WriteFile(sourcePath, testPNGBytes, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	ref, err := store.Store(sourcePath, Meta{ContentType: "image/png"}, "scope")
	if err != nil {
		t.Fatalf("Store() error = %v", err)
	}
	return store, ref
}
