package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNixRuntimeHealthyRecognizesRequiredMarkers(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeTestExecutable(t, filepath.Join(root, "var/nix/profiles/default/bin/nix"))
	writeTestExecutable(t, filepath.Join(root, "var/nix/profiles/default/bin/bash"))
	if err := os.MkdirAll(filepath.Join(root, "store"), 0o755); err != nil {
		t.Fatalf("MkdirAll(store) error = %v", err)
	}

	healthy, err := nixRuntimeHealthy(root)
	if err != nil {
		t.Fatalf("nixRuntimeHealthy() error = %v", err)
	}
	if !healthy {
		t.Fatalf("expected nix runtime at %q to be healthy", root)
	}
}

func TestNixRuntimeHealthyRejectsMissingMarkers(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "store"), 0o755); err != nil {
		t.Fatalf("MkdirAll(store) error = %v", err)
	}
	writeTestExecutable(t, filepath.Join(root, "var/nix/profiles/default/bin/bash"))

	healthy, err := nixRuntimeHealthy(root)
	if err != nil {
		t.Fatalf("nixRuntimeHealthy() error = %v", err)
	}
	if healthy {
		t.Fatalf("expected nix runtime at %q to be unhealthy", root)
	}
}

func TestNixBootstrapSourceAvailableAcceptsProfileSymlinks(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "store"), 0o755); err != nil {
		t.Fatalf("MkdirAll(store) error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "var/nix/profiles"), 0o755); err != nil {
		t.Fatalf("MkdirAll(profiles) error = %v", err)
	}
	if err := os.Symlink("/nix/var/nix/profiles/default-1-link", filepath.Join(root, "var/nix/profiles/default")); err != nil {
		t.Fatalf("Symlink(default) error = %v", err)
	}
	if err := os.Symlink("/nix/store/source-root-profile", filepath.Join(root, "var/nix/profiles/default-1-link")); err != nil {
		t.Fatalf("Symlink(default-1-link) error = %v", err)
	}

	available, err := nixBootstrapSourceAvailable(root)
	if err != nil {
		t.Fatalf("nixBootstrapSourceAvailable() error = %v", err)
	}
	if !available {
		t.Fatalf("expected bootstrap source at %q to be available", root)
	}
}

func TestCopyTreeCopiesFilesAndSymlinks(t *testing.T) {
	t.Parallel()

	sourceRoot := t.TempDir()
	targetRoot := t.TempDir()

	writeTestExecutable(t, filepath.Join(sourceRoot, "var/nix/profiles/default/bin/nix"))
	writeTestExecutable(t, filepath.Join(sourceRoot, "var/nix/profiles/default/bin/bash"))
	if err := os.MkdirAll(filepath.Join(sourceRoot, "store"), 0o755); err != nil {
		t.Fatalf("MkdirAll(store) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, "store/marker"), []byte("alpha"), 0o644); err != nil {
		t.Fatalf("WriteFile(marker) error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(sourceRoot, "var/nix/profiles"), 0o755); err != nil {
		t.Fatalf("MkdirAll(profiles) error = %v", err)
	}
	if err := os.Symlink("default", filepath.Join(sourceRoot, "var/nix/profiles/current")); err != nil {
		t.Fatalf("Symlink(current) error = %v", err)
	}

	if err := copyTree(sourceRoot, targetRoot); err != nil {
		t.Fatalf("copyTree() error = %v", err)
	}

	content, err := os.ReadFile(filepath.Join(targetRoot, "store/marker"))
	if err != nil {
		t.Fatalf("ReadFile(marker) error = %v", err)
	}
	if got := string(content); got != "alpha" {
		t.Fatalf("marker content = %q, want %q", got, "alpha")
	}

	linkTarget, err := os.Readlink(filepath.Join(targetRoot, "var/nix/profiles/current"))
	if err != nil {
		t.Fatalf("Readlink(current) error = %v", err)
	}
	if linkTarget != "default" {
		t.Fatalf("link target = %q, want %q", linkTarget, "default")
	}
}

func writeTestExecutable(t *testing.T, path string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
}
