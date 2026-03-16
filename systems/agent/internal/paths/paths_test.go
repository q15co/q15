package paths

import (
	"path/filepath"
	"testing"
)

func TestDefaultConfigDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := DefaultConfigDir()
	if err != nil {
		t.Fatalf("DefaultConfigDir error = %v", err)
	}

	want := filepath.Join(home, ".config", "q15")
	if got != want {
		t.Fatalf("DefaultConfigDir = %q, want %q", got, want)
	}
}

func TestPathJoiners(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "my-q15")
	if got, want := AuthPath(dir), filepath.Join(dir, "auth.json"); got != want {
		t.Fatalf("AuthPath = %q, want %q", got, want)
	}
}

func TestDefaultAuthPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir := filepath.Join(home, ".config", "q15")
	authPath, err := DefaultAuthPath()
	if err != nil {
		t.Fatalf("DefaultAuthPath error = %v", err)
	}
	if want := filepath.Join(dir, "auth.json"); authPath != want {
		t.Fatalf("DefaultAuthPath = %q, want %q", authPath, want)
	}
}
