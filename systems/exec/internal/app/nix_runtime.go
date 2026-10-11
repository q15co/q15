package app

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
)

const (
	runtimeNixDir         = "/nix"
	bootstrapNixSourceDir = "/var/lib/q15/bootstrap-nix"
)

var requiredNixRuntimeMarkers = []string{
	"store",
	"var/nix/profiles/default/bin/nix",
	"var/nix/profiles/default/bin/bash",
}

// requiredImageRuntimePathsBase are the architecture independent paths baked
// into the exec image by docker/exec.Dockerfile. The nix-ld shim, its real
// loader target, and the library directory let exec sessions start unpatched
// distro-built ELF binaries; without them the first foreign ELF a session runs
// fails.
var requiredImageRuntimePathsBase = []string{
	"/etc/zoneinfo",
	"/etc/fonts/fonts.conf",
	"/etc/nix-ld/ld",
	"/etc/nix-ld/lib",
}

// execImageLoaderPaths maps a Go architecture to the FHS path where the exec
// image installs the nix-ld shim. The Dockerfile selects the same path from
// the build's target architecture, so an architecture missing here is one
// whose image would place the shim where no binary looks for it.
var execImageLoaderPaths = map[string]string{
	"amd64": "/lib64/ld-linux-x86-64.so.2",
	"arm64": "/lib/ld-linux-aarch64.so.1",
}

// requiredImageRuntimePaths reports the paths the exec image must provide on
// goarch. An unknown architecture reports the base paths and false so callers
// fail loudly instead of silently checking less.
func requiredImageRuntimePaths(goarch string) ([]string, bool) {
	paths := append([]string{}, requiredImageRuntimePathsBase...)

	loader, ok := execImageLoaderPaths[goarch]
	if !ok {
		return paths, false
	}
	return append(paths, loader), true
}

// execImageRuntimeHealthy reports whether the image paths this binary depends
// on exist in the container it is running in.
func execImageRuntimeHealthy() (bool, error) {
	paths, ok := requiredImageRuntimePaths(runtime.GOARCH)
	if !ok {
		return false, fmt.Errorf(
			"no exec image loader path known for GOARCH %q",
			runtime.GOARCH,
		)
	}
	return runtimePathsHealthy(paths)
}

var requiredBootstrapSourceMarkers = []string{
	"store",
	"var/nix/profiles/default",
	"var/nix/profiles/default-1-link",
}

func bootstrapNixRuntime() error {
	healthy, err := nixRuntimeHealthy(runtimeNixDir)
	if err != nil {
		return err
	}
	imagePathsHealthy, err := execImageRuntimeHealthy()
	if err != nil {
		return err
	}
	if healthy && imagePathsHealthy {
		return nil
	}

	sourceHealthy, err := nixBootstrapSourceAvailable(bootstrapNixSourceDir)
	if err != nil {
		return err
	}
	if !sourceHealthy {
		return fmt.Errorf(
			"nix runtime missing required bootstrap markers in %q",
			bootstrapNixSourceDir,
		)
	}

	if err := copyTree(bootstrapNixSourceDir, runtimeNixDir); err != nil {
		return fmt.Errorf("bootstrap /nix from %q: %w", bootstrapNixSourceDir, err)
	}

	healthy, err = nixRuntimeHealthy(runtimeNixDir)
	if err != nil {
		return err
	}
	if !healthy {
		return fmt.Errorf("bootstrapped /nix is still missing required runtime markers")
	}
	imagePathsHealthy, err = execImageRuntimeHealthy()
	if err != nil {
		return err
	}
	if !imagePathsHealthy {
		return fmt.Errorf("bootstrapped /nix is still missing required image runtime paths")
	}
	return nil
}

func nixBootstrapSourceAvailable(root string) (bool, error) {
	root = filepath.Clean(root)
	for _, marker := range requiredBootstrapSourceMarkers {
		path := filepath.Join(root, filepath.FromSlash(marker))
		info, err := os.Lstat(path)
		if err != nil {
			if os.IsNotExist(err) {
				return false, nil
			}
			return false, fmt.Errorf("lstat %q: %w", path, err)
		}
		if marker == "store" {
			if !info.IsDir() {
				return false, nil
			}
			continue
		}
		if info.IsDir() {
			return false, nil
		}
	}
	return true, nil
}

func nixRuntimeHealthy(root string) (bool, error) {
	root = filepath.Clean(root)
	for _, marker := range requiredNixRuntimeMarkers {
		path := filepath.Join(root, filepath.FromSlash(marker))
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				return false, nil
			}
			return false, fmt.Errorf("stat %q: %w", path, err)
		}
		if marker == "store" {
			if !info.IsDir() {
				return false, nil
			}
			continue
		}
		if info.IsDir() {
			return false, nil
		}
		if info.Mode()&0o111 == 0 {
			return false, nil
		}
	}
	return true, nil
}

func runtimePathsHealthy(paths []string) (bool, error) {
	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			if os.IsNotExist(err) {
				return false, nil
			}
			return false, fmt.Errorf("stat %q: %w", path, err)
		}
	}
	return true, nil
}

func copyTree(sourceRoot string, targetRoot string) error {
	sourceRoot = filepath.Clean(sourceRoot)
	targetRoot = filepath.Clean(targetRoot)

	return filepath.WalkDir(
		sourceRoot,
		func(sourcePath string, _ fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}

			relPath, err := filepath.Rel(sourceRoot, sourcePath)
			if err != nil {
				return err
			}
			if relPath == "." {
				return os.MkdirAll(targetRoot, 0o755)
			}

			targetPath := filepath.Join(targetRoot, relPath)
			info, err := os.Lstat(sourcePath)
			if err != nil {
				return err
			}

			switch mode := info.Mode(); {
			case mode.IsDir():
				if err := ensureDirectory(targetPath, info.Mode().Perm()); err != nil {
					return err
				}
			case mode&os.ModeSymlink != 0:
				if err := copySymlink(sourcePath, targetPath); err != nil {
					return err
				}
			case mode.IsRegular():
				if err := copyRegularFile(sourcePath, targetPath, info.Mode().Perm()); err != nil {
					return err
				}
			default:
				return fmt.Errorf("unsupported file mode %s for %q", mode.String(), sourcePath)
			}

			return nil
		},
	)
}

func ensureDirectory(path string, perm fs.FileMode) error {
	info, err := os.Lstat(path)
	if err == nil && !info.IsDir() {
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(path, perm); err != nil {
		return err
	}
	return os.Chmod(path, perm)
}

func copySymlink(sourcePath string, targetPath string) error {
	targetValue, err := os.Readlink(sourcePath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return err
	}
	if err := os.RemoveAll(targetPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Symlink(targetValue, targetPath)
}

func copyRegularFile(sourcePath string, targetPath string, perm fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return err
	}

	sourceFile, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	targetFile, err := os.OpenFile(targetPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(targetFile, sourceFile); err != nil {
		_ = targetFile.Close()
		return err
	}
	if err := targetFile.Close(); err != nil {
		return err
	}
	return os.Chmod(targetPath, perm)
}
