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
	if err := os.MkdirAll(filepath.Join(root, "var/nix/profiles/q15-floor"), 0o755); err != nil {
		t.Fatalf("MkdirAll(q15-floor) error = %v", err)
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

func TestNixRuntimeHealthyRejectsMissingFloorProfile(t *testing.T) {
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
	if healthy {
		t.Fatal("expected a runtime without the q15-floor profile to be unhealthy")
	}
}

func TestRequiredImageRuntimePathsCoverLoaderAndLibrariesPerArchitecture(t *testing.T) {
	t.Parallel()

	base := []string{
		"/etc/zoneinfo",
		"/etc/fonts/fonts.conf",
		"/etc/nix-ld/ld",
		"/etc/nix-ld/lib",
		"/etc/q15/exec-floor.manifest",
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
	if err := os.Symlink("/nix/store/source-floor-profile", filepath.Join(root, "var/nix/profiles/q15-floor")); err != nil {
		t.Fatalf("Symlink(q15-floor) error = %v", err)
	}

	available, err := nixBootstrapSourceAvailable(root)
	if err != nil {
		t.Fatalf("nixBootstrapSourceAvailable() error = %v", err)
	}
	if !available {
		t.Fatalf("expected bootstrap source at %q to be available", root)
	}
}

func TestBootstrapNixRuntimeReconcilesExecFloorVersion(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name           string
		marker         string
		writeMarker    bool
		inPlaceUpgrade bool
		breakFloor     bool
		wantReseed     bool
	}{
		{
			name:        "matching marker does not reseed",
			marker:      "floor-v1\n",
			writeMarker: true,
		},
		{
			name:       "missing marker reseeds",
			wantReseed: true,
		},
		{
			name:           "mismatched marker reseeds and replaces an in-place upgrade",
			marker:         "floor-v0\n",
			writeMarker:    true,
			inPlaceUpgrade: true,
			wantReseed:     true,
		},
		{
			name:        "matching marker repairs a broken floor",
			marker:      "floor-v1\n",
			writeMarker: true,
			breakFloor:  true,
			wantReseed:  true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sourceRoot := t.TempDir()
			runtimeRoot := t.TempDir()
			imageRoot := t.TempDir()
			usrBinDir := filepath.Join(imageRoot, "usr", "bin")
			manifestPath := filepath.Join(imageRoot, "etc", "q15", "exec-floor.manifest")
			imageVersionPath := filepath.Join(imageRoot, "etc", "q15", "exec-floor.version")
			runtimeVersionPath := filepath.Join(runtimeRoot, ".q15-exec-floor.version")
			writeBootstrapFloorFixture(t, sourceRoot, runtimeRoot, usrBinDir, "sed")
			writeTestFile(t, manifestPath, "sed\n")
			writeTestFile(t, imageVersionPath, "floor-v1\n")
			writeTestFile(t, filepath.Join(sourceRoot, "store", "reseed-sentinel"), "source\n")

			// Start with a healthy volume, then remove the sentinel so a later
			// copy is observable without intercepting copyTree itself.
			if err := copyTree(sourceRoot, runtimeRoot); err != nil {
				t.Fatalf("copyTree(initial fixture) error = %v", err)
			}
			if err := os.Remove(filepath.Join(runtimeRoot, "store", "reseed-sentinel")); err != nil {
				t.Fatalf("Remove(reseed-sentinel) error = %v", err)
			}
			if tc.writeMarker {
				writeTestFile(t, runtimeVersionPath, tc.marker)
			}
			if tc.inPlaceUpgrade {
				profilePath := filepath.Join(runtimeRoot, "var", "nix", "profiles", "q15-floor")
				if err := os.Remove(profilePath); err != nil {
					t.Fatalf("Remove(upgraded q15-floor) error = %v", err)
				}
				if err := os.Symlink("q15-floor-99-link", profilePath); err != nil {
					t.Fatalf("Symlink(upgraded q15-floor) error = %v", err)
				}
			}
			if tc.breakFloor {
				if err := os.Remove(filepath.Join(runtimeRoot, "store", "floor", "bin", "sed")); err != nil {
					t.Fatalf("Remove(runtime sed) error = %v", err)
				}
			}

			err := bootstrapNixRuntimeWithConfig(nixRuntimeConfig{
				runtimeDir:         runtimeRoot,
				bootstrapSourceDir: sourceRoot,
				imageVersionPath:   imageVersionPath,
				runtimeVersionPath: runtimeVersionPath,
				manifestPath:       manifestPath,
				binaryDir:          usrBinDir,
				imageRuntimePaths:  []string{manifestPath},
			})
			if err != nil {
				t.Fatalf("bootstrapNixRuntimeWithConfig() error = %v", err)
			}

			_, err = os.Stat(filepath.Join(runtimeRoot, "store", "reseed-sentinel"))
			reseeded := err == nil
			if reseeded != tc.wantReseed {
				t.Fatalf("reseeded = %t, want %t (stat error = %v)", reseeded, tc.wantReseed, err)
			}
			marker, err := os.ReadFile(runtimeVersionPath)
			if err != nil {
				t.Fatalf("ReadFile(runtime floor marker) error = %v", err)
			}
			if got := string(marker); got != "floor-v1\n" {
				t.Fatalf("runtime floor marker = %q, want %q", got, "floor-v1\\n")
			}
			if tc.inPlaceUpgrade {
				profilePath := filepath.Join(runtimeRoot, "var", "nix", "profiles", "q15-floor")
				target, err := os.Readlink(profilePath)
				if err != nil {
					t.Fatalf("Readlink(q15-floor) error = %v", err)
				}
				want := filepath.Join(runtimeRoot, "var", "nix", "profiles", "q15-floor-1-link")
				if target != want {
					t.Fatalf("q15-floor target = %q, want image target %q", target, want)
				}
			}
		})
	}
}

func TestBootstrapNixRuntimeNamesMissingExecFloorBinary(t *testing.T) {
	t.Parallel()

	sourceRoot := t.TempDir()
	runtimeRoot := t.TempDir()
	imageRoot := t.TempDir()
	usrBinDir := filepath.Join(imageRoot, "usr", "bin")
	manifestPath := filepath.Join(imageRoot, "etc", "q15", "exec-floor.manifest")
	imageVersionPath := filepath.Join(imageRoot, "etc", "q15", "exec-floor.version")
	writeBootstrapFloorFixture(t, sourceRoot, runtimeRoot, usrBinDir, "sed")
	writeTestFile(t, manifestPath, "sed\npatch\n")
	writeTestFile(t, imageVersionPath, "floor-v1\n")
	writeTestFile(t, filepath.Join(runtimeRoot, ".q15-exec-floor.version"), "floor-v0\n")

	err := bootstrapNixRuntimeWithConfig(nixRuntimeConfig{
		runtimeDir:         runtimeRoot,
		bootstrapSourceDir: sourceRoot,
		imageVersionPath:   imageVersionPath,
		runtimeVersionPath: filepath.Join(runtimeRoot, ".q15-exec-floor.version"),
		manifestPath:       manifestPath,
		binaryDir:          usrBinDir,
		imageRuntimePaths:  []string{manifestPath},
	})
	if err == nil || !strings.Contains(err.Error(), `exec floor binary "patch"`) {
		t.Fatalf("bootstrapNixRuntimeWithConfig() error = %v, want missing patch binary", err)
	}
	marker, readErr := os.ReadFile(filepath.Join(runtimeRoot, ".q15-exec-floor.version"))
	if readErr != nil {
		t.Fatalf("ReadFile(runtime floor marker) error = %v", readErr)
	}
	if got := string(marker); got != "floor-v0\n" {
		t.Fatalf("runtime floor marker advanced after failed verification: %q", got)
	}
}

func TestBootstrapNixRuntimeNamesRequiredBinaryWhenImageMetadataIsMissing(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name           string
		removeManifest bool
		removeVersion  bool
	}{
		{name: "manifest", removeManifest: true},
		{name: "version", removeVersion: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sourceRoot := t.TempDir()
			runtimeRoot := t.TempDir()
			imageRoot := t.TempDir()
			usrBinDir := filepath.Join(imageRoot, "usr", "bin")
			manifestPath := filepath.Join(imageRoot, "etc", "q15", "exec-floor.manifest")
			imageVersionPath := filepath.Join(imageRoot, "etc", "q15", "exec-floor.version")
			writeBootstrapFloorFixture(t, sourceRoot, runtimeRoot, usrBinDir, "sed")
			writeTestFile(t, manifestPath, "sed\n")
			writeTestFile(t, imageVersionPath, "floor-v1\n")
			if tc.removeManifest {
				if err := os.Remove(manifestPath); err != nil {
					t.Fatalf("Remove(manifest) error = %v", err)
				}
			}
			if tc.removeVersion {
				if err := os.Remove(imageVersionPath); err != nil {
					t.Fatalf("Remove(version) error = %v", err)
				}
			}

			err := bootstrapNixRuntimeWithConfig(nixRuntimeConfig{
				runtimeDir:         runtimeRoot,
				bootstrapSourceDir: sourceRoot,
				imageVersionPath:   imageVersionPath,
				runtimeVersionPath: filepath.Join(runtimeRoot, ".q15-exec-floor.version"),
				manifestPath:       manifestPath,
				binaryDir:          usrBinDir,
				imageRuntimePaths:  []string{manifestPath},
			})
			if err == nil || !strings.Contains(err.Error(), `exec floor binary "sed"`) {
				t.Fatalf(
					"bootstrapNixRuntimeWithConfig() error = %v, want required sed binary",
					err,
				)
			}
		})
	}
}

func writeBootstrapFloorFixture(
	t *testing.T,
	sourceRoot string,
	runtimeRoot string,
	usrBinDir string,
	binaries ...string,
) {
	t.Helper()

	writeTestExecutable(t, filepath.Join(sourceRoot, "store", "default", "bin", "nix"))
	writeTestExecutable(t, filepath.Join(sourceRoot, "store", "default", "bin", "bash"))
	for _, binary := range binaries {
		writeTestExecutable(t, filepath.Join(sourceRoot, "store", "floor", "bin", binary))
	}
	profileDir := filepath.Join(sourceRoot, "var", "nix", "profiles")
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", profileDir, err)
	}
	defaultGenerationTarget := filepath.Join(runtimeRoot, "store", "default")
	if err := os.Symlink(defaultGenerationTarget, filepath.Join(profileDir, "default-1-link")); err != nil {
		t.Fatalf("Symlink(default-1-link) error = %v", err)
	}
	defaultProfileTarget := filepath.Join(runtimeRoot, "var", "nix", "profiles", "default-1-link")
	if err := os.Symlink(defaultProfileTarget, filepath.Join(profileDir, "default")); err != nil {
		t.Fatalf("Symlink(default) error = %v", err)
	}
	generationTarget := filepath.Join(runtimeRoot, "store", "floor")
	if err := os.Symlink(generationTarget, filepath.Join(profileDir, "q15-floor-1-link")); err != nil {
		t.Fatalf("Symlink(q15-floor-1-link) error = %v", err)
	}
	profileTarget := filepath.Join(runtimeRoot, "var", "nix", "profiles", "q15-floor-1-link")
	if err := os.Symlink(profileTarget, filepath.Join(profileDir, "q15-floor")); err != nil {
		t.Fatalf("Symlink(q15-floor) error = %v", err)
	}
	for _, binary := range binaries {
		link := filepath.Join(usrBinDir, binary)
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(link), err)
		}
		target := filepath.Join(runtimeRoot, "var", "nix", "profiles", "q15-floor", "bin", binary)
		if err := os.Symlink(target, link); err != nil {
			t.Fatalf("Symlink(%q) error = %v", link, err)
		}
	}
}

func writeTestFile(t *testing.T, path string, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
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

func TestCopyRegularFileAtomicallyReplacesTarget(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sourcePath := filepath.Join(root, "source")
	targetPath := filepath.Join(root, "target")
	peerPath := filepath.Join(root, "peer")
	writeTestFile(t, sourcePath, "new contents")
	writeTestFile(t, targetPath, "old contents")
	if err := os.Link(targetPath, peerPath); err != nil {
		t.Fatalf("Link(peer) error = %v", err)
	}
	before, err := os.Lstat(targetPath)
	if err != nil {
		t.Fatalf("Lstat(target before copy) error = %v", err)
	}

	if err := copyRegularFile(sourcePath, targetPath, 0o751); err != nil {
		t.Fatalf("copyRegularFile() error = %v", err)
	}
	content, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("ReadFile(target) error = %v", err)
	}
	if got := string(content); got != "new contents" {
		t.Fatalf("target contents = %q, want new contents", got)
	}
	peer, err := os.ReadFile(peerPath)
	if err != nil {
		t.Fatalf("ReadFile(peer) error = %v", err)
	}
	if got := string(peer); got != "old contents" {
		t.Fatalf("existing inode was modified in place: peer contents = %q", got)
	}
	after, err := os.Lstat(targetPath)
	if err != nil {
		t.Fatalf("Lstat(target after copy) error = %v", err)
	}
	if os.SameFile(before, after) {
		t.Fatal("target inode was retained; copy did not rename a replacement")
	}
	if got := after.Mode().Perm(); got != 0o751 {
		t.Fatalf("target mode = %o, want 751", got)
	}
}

func TestCopySymlinkKeepsIdenticalLinkAndAtomicallyReplacesChangedLink(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sourcePath := filepath.Join(root, "source")
	targetPath := filepath.Join(root, "target")
	if err := os.Symlink("generation-1", sourcePath); err != nil {
		t.Fatalf("Symlink(source) error = %v", err)
	}
	if err := os.Symlink("generation-1", targetPath); err != nil {
		t.Fatalf("Symlink(target) error = %v", err)
	}
	before, err := os.Lstat(targetPath)
	if err != nil {
		t.Fatalf("Lstat(target before copy) error = %v", err)
	}

	if err := copySymlink(sourcePath, targetPath); err != nil {
		t.Fatalf("copySymlink(identical) error = %v", err)
	}
	after, err := os.Lstat(targetPath)
	if err != nil {
		t.Fatalf("Lstat(target after identical copy) error = %v", err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("identical symlink was replaced instead of left intact")
	}

	if err := os.Remove(sourcePath); err != nil {
		t.Fatalf("Remove(source) error = %v", err)
	}
	if err := os.Symlink("generation-2", sourcePath); err != nil {
		t.Fatalf("Symlink(changed source) error = %v", err)
	}
	if err := copySymlink(sourcePath, targetPath); err != nil {
		t.Fatalf("copySymlink(changed) error = %v", err)
	}
	targetValue, err := os.Readlink(targetPath)
	if err != nil {
		t.Fatalf("Readlink(target) error = %v", err)
	}
	if targetValue != "generation-2" {
		t.Fatalf("target link = %q, want generation-2", targetValue)
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
