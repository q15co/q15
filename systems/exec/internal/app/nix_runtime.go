package app

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
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
	if healthy {
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
