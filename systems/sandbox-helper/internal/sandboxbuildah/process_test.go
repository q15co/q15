package sandboxbuildah

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"go.podman.io/storage/pkg/unshare"
)

func TestPreferNixWrappersInPathPrependsWrappersDir(t *testing.T) {
	wrappersDir := t.TempDir()
	restore := installProcessTestHooks(t, wrappersDir)
	defer restore()

	if err := os.Setenv("PATH", "/usr/bin:/bin"); err != nil {
		t.Fatalf("set PATH: %v", err)
	}

	if err := preferNixWrappersInPath(); err != nil {
		t.Fatalf("preferNixWrappersInPath(): %v", err)
	}

	parts := strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))
	if len(parts) == 0 || parts[0] != wrappersDir {
		t.Fatalf("PATH = %q, want first entry %q", os.Getenv("PATH"), wrappersDir)
	}
}

func TestEnsureNixOSUIDMapWrappersPassesWithWrappers(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux-only rootless sandbox runtime")
	}
	if !unshare.IsRootless() {
		t.Skip("requires rootless test environment")
	}

	wrappersDir := t.TempDir()
	restore := installProcessTestHooks(t, wrappersDir)
	defer restore()

	writeExecutableWithMode(t, filepath.Join(wrappersDir, "newuidmap"), 0o4755)
	writeExecutableWithMode(t, filepath.Join(wrappersDir, "newgidmap"), 0o2755)
	statFunc = func(name string) (os.FileInfo, error) {
		switch name {
		case wrappersDir, filepath.Clean(wrappersDir):
			return fakeFileInfo{name: filepath.Base(wrappersDir), mode: os.ModeDir | 0o755}, nil
		case filepath.Join(wrappersDir, "newuidmap"):
			return fakeFileInfo{name: "newuidmap", mode: os.ModeSetuid | 0o755}, nil
		case filepath.Join(wrappersDir, "newgidmap"):
			return fakeFileInfo{name: "newgidmap", mode: os.ModeSetgid | 0o755}, nil
		default:
			return os.Stat(name)
		}
	}

	if err := os.Setenv("PATH", "/usr/bin:/bin"); err != nil {
		t.Fatalf("set PATH: %v", err)
	}
	if err := preferNixWrappersInPath(); err != nil {
		t.Fatalf("preferNixWrappersInPath(): %v", err)
	}

	if err := ensureNixOSUIDMapWrappers(); err != nil {
		t.Fatalf("ensureNixOSUIDMapWrappers(): %v", err)
	}
}

func TestEnsureNixOSUIDMapWrappersFailsForNixStoreShadowBinary(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux-only rootless sandbox runtime")
	}
	if !unshare.IsRootless() {
		t.Skip("requires rootless test environment")
	}

	restore := installProcessTestHooks(t, "/run/wrappers/bin")
	defer restore()

	lookPathFunc = func(file string) (string, error) {
		switch file {
		case "newuidmap":
			return "/nix/store/abc123-shadow-4.18.0/bin/newuidmap", nil
		case "newgidmap":
			return "/run/wrappers/bin/newgidmap", nil
		default:
			return "", exec.ErrNotFound
		}
	}

	err := ensureNixOSUIDMapWrappers()
	if err == nil {
		t.Fatal("ensureNixOSUIDMapWrappers() error = nil, want failure")
	}
	msg := err.Error()
	if !strings.Contains(msg, "-shadow-") {
		t.Fatalf("error = %q, want shadow path mention", msg)
	}
	if !strings.Contains(msg, "/run/wrappers/bin") {
		t.Fatalf("error = %q, want wrappers remediation", msg)
	}
}

func TestEnsureNixOSUIDMapWrappersFailsWithoutSetIDBits(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux-only rootless sandbox runtime")
	}
	if !unshare.IsRootless() {
		t.Skip("requires rootless test environment")
	}

	wrappersDir := t.TempDir()
	restore := installProcessTestHooks(t, wrappersDir)
	defer restore()

	writeExecutableWithMode(t, filepath.Join(wrappersDir, "newuidmap"), 0o0755)
	writeExecutableWithMode(t, filepath.Join(wrappersDir, "newgidmap"), 0o2755)

	if err := os.Setenv("PATH", wrappersDir); err != nil {
		t.Fatalf("set PATH: %v", err)
	}

	err := ensureNixOSUIDMapWrappers()
	if err == nil {
		t.Fatal("ensureNixOSUIDMapWrappers() error = nil, want failure")
	}
	if !strings.Contains(err.Error(), "setuid/setgid") {
		t.Fatalf("error = %q, want setid failure", err)
	}
}

func installProcessTestHooks(t *testing.T, wrappersDir string) func() {
	t.Helper()

	oldWrappersDir := nixWrappersBinDir
	oldLookPath := lookPathFunc
	oldStat := statFunc
	oldPath := os.Getenv("PATH")

	nixWrappersBinDir = wrappersDir
	lookPathFunc = exec.LookPath
	statFunc = os.Stat

	return func() {
		nixWrappersBinDir = oldWrappersDir
		lookPathFunc = oldLookPath
		statFunc = oldStat
		if err := os.Setenv("PATH", oldPath); err != nil {
			t.Fatalf("restore PATH: %v", err)
		}
	}
}

func writeExecutableWithMode(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %s to %#o: %v", path, mode, err)
	}
}

type fakeFileInfo struct {
	name string
	mode os.FileMode
}

func (f fakeFileInfo) Name() string       { return f.name }
func (f fakeFileInfo) Size() int64        { return 0 }
func (f fakeFileInfo) Mode() os.FileMode  { return f.mode }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fakeFileInfo) Sys() any           { return nil }
