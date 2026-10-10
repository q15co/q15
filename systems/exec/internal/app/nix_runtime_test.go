package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
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

func TestRequiredImageRuntimePathsCoverLoaderAndLibrariesPerArchitecture(t *testing.T) {
	t.Parallel()

	base := []string{
		"/etc/zoneinfo",
		"/etc/fonts/fonts.conf",
		"/etc/nix-ld/ld",
		"/etc/nix-ld/lib",
	}

	for _, tc := range []struct {
		goarch string
		loader string
	}{
		{goarch: "amd64", loader: "/lib64/ld-linux-x86-64.so.2"},
		{goarch: "arm64", loader: "/lib/ld-linux-aarch64.so.1"},
	} {
		paths, ok := requiredImageRuntimePaths(tc.goarch)
		if !ok {
			t.Fatalf("requiredImageRuntimePaths(%q) ok = false, want true", tc.goarch)
		}
		for _, want := range append(base, tc.loader) {
			if !slices.Contains(paths, want) {
				t.Errorf("requiredImageRuntimePaths(%q) is missing %q", tc.goarch, want)
			}
		}
		for goarch, loader := range execImageLoaderPaths {
			if goarch == tc.goarch {
				continue
			}
			if slices.Contains(paths, loader) {
				t.Errorf(
					"requiredImageRuntimePaths(%q) requires %q, which belongs to %q",
					tc.goarch, loader, goarch,
				)
			}
		}
	}
}

func TestRequiredImageRuntimePathsRejectUnknownArchitecture(t *testing.T) {
	t.Parallel()

	paths, ok := requiredImageRuntimePaths("riscv64")
	if ok {
		t.Fatal("requiredImageRuntimePaths(\"riscv64\") ok = true, want false")
	}
	if !slices.Equal(paths, requiredImageRuntimePathsBase) {
		t.Fatalf(
			"requiredImageRuntimePaths(\"riscv64\") = %v, want the base paths %v",
			paths, requiredImageRuntimePathsBase,
		)
	}
}

// execImageLoaderSystems maps a Go architecture to the nixpkgs system whose
// glibc provides the dynamic loader the image installs for it.
var execImageLoaderSystems = map[string]string{
	"amd64": "x86_64-linux",
	"arm64": "aarch64-linux",
}

// TestExecImageLoaderPathsMatchNixpkgs checks the loader file names this
// binary expects against the ones nixpkgs links binaries against for the same
// architectures, which is the value the image build installs. Hardcoding the
// x86_64 loader for both is what broke the arm64 image build, and the two
// names only agree by accident on one of the two platforms.
func TestExecImageLoaderPathsMatchNixpkgs(t *testing.T) {
	t.Parallel()

	nix, err := exec.LookPath("nix")
	if err != nil {
		if os.Getenv("Q15_REQUIRE_NIX") != "" {
			t.Fatalf("nix is required here but not installed: %v", err)
		}
		t.Skip("nix is not installed, so the loader names cannot be checked against nixpkgs")
	}

	for goarch, loader := range execImageLoaderPaths {
		system, ok := execImageLoaderSystems[goarch]
		if !ok {
			t.Errorf("no nixpkgs system is known for %q", goarch)
			continue
		}

		attr := "nixpkgs#legacyPackages." + system + ".stdenv.cc.bintools.dynamicLinker"
		out, err := exec.Command(
			nix,
			"eval",
			"--raw",
			"--extra-experimental-features", "nix-command",
			"--extra-experimental-features", "flakes",
			attr,
		).Output()
		if err != nil {
			t.Fatalf("nix eval %q error = %v", attr, err)
		}

		want := filepath.Base(loader)
		got := filepath.Base(strings.TrimSpace(string(out)))
		if got != want {
			t.Errorf(
				"%s links against %q, but requiredImageRuntimePaths wants %q",
				system, got, want,
			)
		}
	}
}

// loaderAssignment matches a loader name written into docker/exec.Dockerfile
// rather than derived at build time.
var loaderAssignment = regexp.MustCompile(`LD_SO="ld-linux`)

// TestExecDockerfileDerivesItsLoaderPath keeps the image build from going back
// to a hardcoded loader name, which is what produced an arm64 image the
// aarch64 glibc could not satisfy.
func TestExecDockerfileDerivesItsLoaderPath(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "docker", "exec.Dockerfile"))
	if err != nil {
		t.Fatalf("ReadFile(exec.Dockerfile) error = %v", err)
	}
	dockerfile := string(raw)

	if !strings.Contains(dockerfile, "dynamicLinker") {
		t.Error("exec.Dockerfile no longer asks nixpkgs for the loader path")
	}
	if loaderAssignment.MatchString(dockerfile) {
		t.Error("exec.Dockerfile assigns a loader file name instead of deriving it")
	}
}

func TestRuntimePathsHealthyAcceptsExistingSymlinkTargets(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(root, "store", "fonts.conf")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("MkdirAll(target dir) error = %v", err)
	}
	if err := os.WriteFile(target, []byte("<fontconfig/>"), 0o644); err != nil {
		t.Fatalf("WriteFile(target) error = %v", err)
	}
	link := filepath.Join(root, "etc", "fonts.conf")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatalf("MkdirAll(link dir) error = %v", err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink(link) error = %v", err)
	}

	healthy, err := runtimePathsHealthy([]string{link})
	if err != nil {
		t.Fatalf("runtimePathsHealthy() error = %v", err)
	}
	if !healthy {
		t.Fatalf("expected runtime paths to be healthy")
	}
}

func TestRuntimePathsHealthyRejectsBrokenSymlinkTargets(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	link := filepath.Join(root, "etc", "fonts.conf")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatalf("MkdirAll(link dir) error = %v", err)
	}
	if err := os.Symlink(filepath.Join(root, "missing", "fonts.conf"), link); err != nil {
		t.Fatalf("Symlink(link) error = %v", err)
	}

	healthy, err := runtimePathsHealthy([]string{link})
	if err != nil {
		t.Fatalf("runtimePathsHealthy() error = %v", err)
	}
	if healthy {
		t.Fatalf("expected runtime paths to be unhealthy")
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
