package sandboxbuildah

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/containers/buildah"
	"go.podman.io/storage/pkg/homedir"
	"go.podman.io/storage/pkg/unshare"
)

var (
	buildahEnvOnce sync.Once
	buildahEnvErr  error

	nixWrappersBinDir = "/run/wrappers/bin"
	lookPathFunc      = exec.LookPath
	statFunc          = os.Stat
)

// InitProcess must be called at the start of the helper main(). If it returns
// true, the helper main should return immediately because a Buildah reexec
// handler ran.
func InitProcess() bool {
	verbosef("InitProcess: calling buildah.InitReexec (args=%q)", os.Args)
	reexec := buildah.InitReexec()
	if reexec {
		verbosef("InitProcess: buildah reexec handler ran, returning from helper main")
	} else {
		verbosef("InitProcess: normal helper process path")
	}
	return reexec
}

// EnsureProcessEnvironment prepares rootless Buildah process state. Call this
// as early as possible in the helper process to match the Buildah CLI pattern.
func EnsureProcessEnvironment() error {
	return ensureBuildahProcessEnvironment()
}

func ensureBuildahProcessEnvironment() error {
	verbosef("ensureBuildahProcessEnvironment: begin")
	buildahEnvOnce.Do(func() {
		verbosef(
			"ensureBuildahProcessEnvironment: first-time setup (euid=%d rootless=%v rootless_uid=%d userns_env=%q XDG_RUNTIME_DIR=%q)",
			os.Geteuid(),
			unshare.IsRootless(),
			unshare.GetRootlessUID(),
			os.Getenv(unshare.UsernsEnvName),
			os.Getenv("XDG_RUNTIME_DIR"),
		)
		if err := setXDGRuntimeDir(); err != nil {
			buildahEnvErr = err
			verbosef("ensureBuildahProcessEnvironment: setXDGRuntimeDir failed: %v", err)
			return
		}
		if err := preferNixWrappersInPath(); err != nil {
			buildahEnvErr = err
			verbosef("ensureBuildahProcessEnvironment: preferNixWrappersInPath failed: %v", err)
			return
		}
		if os.Getenv(unshare.UsernsEnvName) != "" {
			verbosef(
				"ensureBuildahProcessEnvironment: skipping unshare because %s is already set to %q",
				unshare.UsernsEnvName,
				os.Getenv(unshare.UsernsEnvName),
			)
			return
		}
		if err := ensureNixOSUIDMapWrappers(); err != nil {
			buildahEnvErr = err
			verbosef("ensureBuildahProcessEnvironment: ensureNixOSUIDMapWrappers failed: %v", err)
			return
		}
		if !hasCgoUnshareConstructor {
			buildahEnvErr = errors.New(
				"q15-sandbox-helper was built with CGO disabled; rootless Buildah userns reexec requires CGO (rebuild helper with CGO_ENABLED=1)",
			)
			verbosef(
				"ensureBuildahProcessEnvironment: helper missing CGO unshare constructor hook",
			)
			return
		}
		verbosef(
			"ensureBuildahProcessEnvironment: calling unshare.MaybeReexecUsingUserNamespace(false)",
		)
		unshare.MaybeReexecUsingUserNamespace(false)
		verbosef(
			"ensureBuildahProcessEnvironment: returned from unshare.MaybeReexecUsingUserNamespace(false)",
		)
	})
	if buildahEnvErr != nil {
		verbosef("ensureBuildahProcessEnvironment: error: %v", buildahEnvErr)
	} else {
		verbosef("ensureBuildahProcessEnvironment: ready")
	}
	return buildahEnvErr
}

func preferNixWrappersInPath() error {
	if _, err := statFunc(nixWrappersBinDir); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat %s: %w", nixWrappersBinDir, err)
	}

	pathEnv := os.Getenv("PATH")
	parts := strings.Split(pathEnv, string(os.PathListSeparator))
	for _, part := range parts {
		if part == nixWrappersBinDir {
			verbosef("preferNixWrappersInPath: PATH already contains %s", nixWrappersBinDir)
			return nil
		}
	}

	newPath := nixWrappersBinDir
	if pathEnv != "" {
		newPath = nixWrappersBinDir + string(os.PathListSeparator) + pathEnv
	}
	if err := os.Setenv("PATH", newPath); err != nil {
		return fmt.Errorf("set PATH: %w", err)
	}
	verbosef("preferNixWrappersInPath: prepended %s to PATH", nixWrappersBinDir)
	return nil
}

func setXDGRuntimeDir() error {
	if !unshare.IsRootless() || os.Getenv("XDG_RUNTIME_DIR") != "" {
		verbosef(
			"setXDGRuntimeDir: no change (rootless=%v, XDG_RUNTIME_DIR=%q)",
			unshare.IsRootless(),
			os.Getenv("XDG_RUNTIME_DIR"),
		)
		return nil
	}

	runtimeDir, err := homedir.GetRuntimeDir()
	if err != nil {
		return fmt.Errorf("resolve XDG runtime dir: %w", err)
	}
	if err := os.Setenv("XDG_RUNTIME_DIR", runtimeDir); err != nil {
		return fmt.Errorf("set XDG_RUNTIME_DIR: %w", err)
	}
	verbosef("setXDGRuntimeDir: set XDG_RUNTIME_DIR=%q", runtimeDir)
	return nil
}

func ensureNixOSUIDMapWrappers() error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("rootless Buildah sandbox runtime is Linux-only (got %s)", runtime.GOOS)
	}
	if !unshare.IsRootless() {
		verbosef("ensureNixOSUIDMapWrappers: skipping (not rootless)")
		return nil
	}

	verbosef("ensureNixOSUIDMapWrappers: PATH=%q", os.Getenv("PATH"))
	uidmapPath, err := lookPathFunc("newuidmap")
	if err != nil {
		return fmt.Errorf(
			"resolve newuidmap in PATH (expected %q): %w",
			filepath.Join(nixWrappersBinDir, "newuidmap"),
			err,
		)
	}
	gidmapPath, err := lookPathFunc("newgidmap")
	if err != nil {
		return fmt.Errorf(
			"resolve newgidmap in PATH (expected %q): %w",
			filepath.Join(nixWrappersBinDir, "newgidmap"),
			err,
		)
	}
	verbosef(
		"ensureNixOSUIDMapWrappers: resolved newuidmap=%q newgidmap=%q",
		uidmapPath,
		gidmapPath,
	)

	expectedUIDMap := filepath.Join(nixWrappersBinDir, "newuidmap")
	expectedGIDMap := filepath.Join(nixWrappersBinDir, "newgidmap")
	if uidmapPath != expectedUIDMap || gidmapPath != expectedGIDMap {
		msg := fmt.Sprintf(
			"rootless uidmap helpers must resolve to NixOS wrappers (expected newuidmap=%q newgidmap=%q; got newuidmap=%q newgidmap=%q)",
			expectedUIDMap,
			expectedGIDMap,
			uidmapPath,
			gidmapPath,
		)
		if looksLikeNixStoreShadowUIDMapBinary(uidmapPath) ||
			looksLikeNixStoreShadowUIDMapBinary(gidmapPath) {
			msg += "; resolved Nix store shadow uidmap helper(s), which are not usable for rootless user namespaces"
		}
		msg += "; remove `shadow` from the devshell and rely on /run/wrappers/bin"
		return errors.New(msg)
	}

	if err := requireAnySetIDBit(expectedUIDMap); err != nil {
		return err
	}
	if err := requireAnySetIDBit(expectedGIDMap); err != nil {
		return err
	}
	return nil
}

func requireAnySetIDBit(path string) error {
	info, err := statFunc(path)
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	mode := info.Mode()
	if mode&os.ModeSetuid == 0 && mode&os.ModeSetgid == 0 {
		return fmt.Errorf(
			"%s must have setuid/setgid bit set for rootless user namespaces (mode=%#o); remove `shadow` from the devshell and rely on /run/wrappers/bin",
			path,
			uint32(mode),
		)
	}
	return nil
}

func looksLikeNixStoreShadowUIDMapBinary(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	if !strings.HasPrefix(path, "/nix/store/") {
		return false
	}
	base := filepath.Base(path)
	if base != "newuidmap" && base != "newgidmap" {
		return false
	}
	return strings.Contains(path, "-shadow-")
}
