package sandboxbuildah

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/containers/buildah"
	"go.podman.io/storage/pkg/homedir"
	"go.podman.io/storage/pkg/unshare"
)

var (
	buildahEnvOnce sync.Once
	buildahEnvErr  error
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
		if skipUnshareEnabled() {
			if err := markRootlessUsernsConfigured(); err != nil {
				buildahEnvErr = err
				verbosef(
					"ensureBuildahProcessEnvironment: markRootlessUsernsConfigured failed: %v",
					err,
				)
				return
			}
			verbosef(
				"ensureBuildahProcessEnvironment: skipping unshare due to Q15_SANDBOX_SKIP_UNSHARE=%q",
				os.Getenv("Q15_SANDBOX_SKIP_UNSHARE"),
			)
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

func markRootlessUsernsConfigured() error {
	if !unshare.IsRootless() {
		verbosef("markRootlessUsernsConfigured: skipping (not rootless)")
		return nil
	}

	current := os.Getenv(unshare.UsernsEnvName)
	if current == "done" {
		verbosef(
			"markRootlessUsernsConfigured: %s already set to %q",
			unshare.UsernsEnvName,
			current,
		)
		return nil
	}
	if err := os.Setenv(unshare.UsernsEnvName, "done"); err != nil {
		return fmt.Errorf("set %s=done: %w", unshare.UsernsEnvName, err)
	}
	verbosef(
		"markRootlessUsernsConfigured: set %s=done (skip-unshare compatibility mode, not a real userns reexec)",
		unshare.UsernsEnvName,
	)
	return nil
}

func skipUnshareEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("Q15_SANDBOX_SKIP_UNSHARE"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
