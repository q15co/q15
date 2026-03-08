package sandboxbuildah

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/containers/buildah"
)

func TestSettingsValidateRequiresAbsolutePaths(t *testing.T) {
	cfg := Settings{
		ContainerName:    "q15-test",
		WorkspaceHostDir: "/tmp/q15-test",
		WorkspaceDir:     "/workspace",
		MemoryHostDir:    "/tmp/q15-test/.q15-memory",
		MemoryDir:        "/memory",
		SkillsHostDir:    "/tmp/q15-skills",
		SkillsDir:        "/skills",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}

	cfg.WorkspaceHostDir = "relative"
	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected validation error for relative workspace host dir")
	}
}

func TestShouldRecreateExistingBuilderByRuntimeAnnotation(t *testing.T) {
	tests := []struct {
		name    string
		builder *buildah.Builder
		want    bool
	}{
		{
			name:    "missing annotation",
			builder: &buildah.Builder{},
			want:    true,
		},
		{
			name: "mismatched annotation",
			builder: &buildah.Builder{
				ImageAnnotations: map[string]string{sandboxRuntimeAnnotation: "legacy"},
			},
			want: true,
		},
		{
			name: "expected annotation",
			builder: &buildah.Builder{
				ImageAnnotations: map[string]string{sandboxRuntimeAnnotation: sandboxRuntimeValue},
			},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := shouldRecreateExistingBuilder(tc.builder)
			if got != tc.want {
				t.Fatalf("shouldRecreateExistingBuilder() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNixBootstrapCommandIncludesRootlessSafeAptFlags(t *testing.T) {
	cmd := nixBootstrapCommand()
	if !strings.Contains(
		cmd,
		"need_base_packages=0",
	) {
		t.Fatalf("bootstrap command missing base package precheck initialization: %q", cmd)
	}
	if !strings.Contains(
		cmd,
		"for required_path in /bin/bash /usr/bin/curl /usr/bin/xz /etc/ssl/certs/ca-certificates.crt; do",
	) {
		t.Fatalf("bootstrap command missing base package readiness check: %q", cmd)
	}
	if !strings.Contains(cmd, "apt-get -o APT::Sandbox::User=root update") {
		t.Fatalf("bootstrap command missing apt update sandbox override: %q", cmd)
	}
	if !strings.Contains(
		cmd,
		"DEBIAN_FRONTEND=noninteractive apt-get -o APT::Sandbox::User=root install -y bash ca-certificates curl xz-utils",
	) {
		t.Fatalf("bootstrap command missing apt install sandbox override: %q", cmd)
	}
	if !strings.Contains(cmd, "if [ ! -d /nix ]; then") {
		t.Fatalf("bootstrap command missing /nix mount presence check: %q", cmd)
	}
	if !strings.Contains(cmd, "if [ ! -w /nix ]; then") {
		t.Fatalf("bootstrap command missing /nix mount writability check: %q", cmd)
	}
	if !strings.Contains(cmd, "mkdir -p /nix/store /nix/var/nix") {
		t.Fatalf("bootstrap command missing nix store dir creation: %q", cmd)
	}
	if !strings.Contains(
		cmd,
		"if command -v nix >/dev/null 2>&1 && nix --version >/dev/null 2>&1; then",
	) {
		t.Fatalf("bootstrap command missing nix precheck: %q", cmd)
	}
	if !strings.Contains(cmd, "NIX_CONFIG=\"$(cat <<'EOF'") {
		t.Fatalf("bootstrap command missing inline NIX_CONFIG template: %q", cmd)
	}
	if !strings.Contains(cmd, "build-users-group =") {
		t.Fatalf("bootstrap command missing build-users-group override: %q", cmd)
	}
	if !strings.Contains(cmd, "export NIX_CONFIG") {
		t.Fatalf("bootstrap command missing NIX_CONFIG export: %q", cmd)
	}
	if !strings.Contains(cmd, "accept-flake-config = true") {
		t.Fatalf("bootstrap command missing accept-flake-config override: %q", cmd)
	}
	if !strings.Contains(
		cmd,
		"curl -fsSL https://nixos.org/nix/install | sh -s -- --no-daemon --yes --no-channel-add --no-modify-profile",
	) {
		t.Fatalf("bootstrap command missing nix installer invocation: %q", cmd)
	}
	if !strings.Contains(cmd, "cat > /etc/nix/nix.conf <<'EOF'") {
		t.Fatalf("bootstrap command missing persistent /etc/nix/nix.conf write: %q", cmd)
	}
}

func TestNixBootstrapCommandEnsuresBasePackagesBeforeNixPrecheck(t *testing.T) {
	cmd := nixBootstrapCommand()
	aptCheck := "DEBIAN_FRONTEND=noninteractive apt-get -o APT::Sandbox::User=root install -y bash ca-certificates curl xz-utils"
	nixCheck := "if command -v nix >/dev/null 2>&1 && nix --version >/dev/null 2>&1; then"
	aptIndex := strings.Index(cmd, aptCheck)
	nixIndex := strings.Index(cmd, nixCheck)
	if aptIndex == -1 || nixIndex == -1 {
		t.Fatalf("bootstrap command missing expected snippets: %q", cmd)
	}
	if aptIndex > nixIndex {
		t.Fatalf("bootstrap command checks nix before ensuring CA/base packages: %q", cmd)
	}
}

func TestEnsureSharedNixHostDir_OverrideCreatesDir(t *testing.T) {
	overridePath := filepath.Join(t.TempDir(), "nix-store")
	t.Setenv(sharedNixHostDirEnv, overridePath)

	got, err := ensureSharedNixHostDir()
	if err != nil {
		t.Fatalf("ensureSharedNixHostDir() error = %v", err)
	}
	if got != overridePath {
		t.Fatalf("shared nix host dir = %q, want %q", got, overridePath)
	}
	info, err := os.Stat(got)
	if err != nil {
		t.Fatalf("stat shared nix host dir: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("shared nix host path is not a directory: %q", got)
	}
}

func TestEnsureSharedNixHostDir_DefaultUsesQ15PathUnderHome(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv(sharedNixHostDirEnv, "")

	got, err := ensureSharedNixHostDir()
	if err != nil {
		t.Fatalf("ensureSharedNixHostDir() error = %v", err)
	}
	want := filepath.Join(homeDir, ".local", "share", "q15", "nix-store")
	if got != want {
		t.Fatalf("shared nix host dir = %q, want %q", got, want)
	}
}

func TestEnsureSharedNixHostDir_OverrideMustBeAbsolute(t *testing.T) {
	t.Setenv(sharedNixHostDirEnv, "relative/path")

	if _, err := ensureSharedNixHostDir(); err == nil {
		t.Fatalf("expected absolute-path validation error")
	}
}

func TestSharedNixStoreMountUsesResolvedHostDir(t *testing.T) {
	hostDir := t.TempDir()
	t.Setenv(sharedNixHostDirEnv, hostDir)

	mount, err := sharedNixStoreMount()
	if err != nil {
		t.Fatalf("sharedNixStoreMount() error = %v", err)
	}
	if mount.Source != hostDir {
		t.Fatalf("mount source = %q, want %q", mount.Source, hostDir)
	}
	if mount.Destination != "/nix" {
		t.Fatalf("mount destination = %q, want %q", mount.Destination, "/nix")
	}
}
