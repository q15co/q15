package sandboxbuildah

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/containers/buildah"
	specs "github.com/opencontainers/runtime-spec/specs-go"
	"go.podman.io/storage"
)

const (
	sandboxBaseImage         = "docker.io/library/debian:bookworm-slim"
	sandboxRuntimeLabel      = "nix-only"
	sandboxRuntimeAnnotation = "io.q15.sandbox.runtime"
	sandboxRuntimeValue      = "nix-only-v1"
	sharedNixHostDirEnv      = "Q15_SANDBOX_NIX_STORE_HOST_DIR"
)

// Settings configures the persistent sandbox container and its mounted paths.
type Settings struct {
	ContainerName    string
	WorkspaceHostDir string
	WorkspaceDir     string
	MemoryHostDir    string
	MemoryDir        string
	SkillsHostDir    string
	SkillsDir        string
	Proxy            *ProxySettings
}

// ProxySettings controls proxy and CA wiring for sandbox command execution.
type ProxySettings struct {
	Enabled              bool
	HTTPProxyURL         string
	HTTPSProxyURL        string
	AllProxyURL          string
	NoProxy              string
	CACertHostPath       string
	CACertContainerPath  string
	SetLowercaseProxyEnv bool
	Env                  map[string]string
}

// Validate checks that required sandbox paths and identifiers are present.
func (s Settings) Validate() error {
	if strings.TrimSpace(s.ContainerName) == "" {
		return errors.New("container name is required")
	}
	if strings.TrimSpace(s.WorkspaceHostDir) == "" {
		return errors.New("workspace host dir is required")
	}
	if strings.TrimSpace(s.WorkspaceDir) == "" {
		return errors.New("workspace dir is required")
	}
	if strings.TrimSpace(s.MemoryHostDir) == "" {
		return errors.New("memory host dir is required")
	}
	if strings.TrimSpace(s.MemoryDir) == "" {
		return errors.New("memory dir is required")
	}
	if !filepath.IsAbs(strings.TrimSpace(s.WorkspaceHostDir)) {
		return errors.New("workspace host dir must be an absolute path")
	}
	if !filepath.IsAbs(strings.TrimSpace(s.WorkspaceDir)) {
		return errors.New("workspace dir must be an absolute path")
	}
	if !filepath.IsAbs(strings.TrimSpace(s.MemoryHostDir)) {
		return errors.New("memory host dir must be an absolute path")
	}
	if !filepath.IsAbs(strings.TrimSpace(s.MemoryDir)) {
		return errors.New("memory dir must be an absolute path")
	}
	if strings.TrimSpace(s.SkillsHostDir) != "" || strings.TrimSpace(s.SkillsDir) != "" {
		if strings.TrimSpace(s.SkillsHostDir) == "" {
			return errors.New("skills host dir is required when skills dir is set")
		}
		if strings.TrimSpace(s.SkillsDir) == "" {
			return errors.New("skills dir is required when skills host dir is set")
		}
		if !filepath.IsAbs(strings.TrimSpace(s.SkillsHostDir)) {
			return errors.New("skills host dir must be an absolute path")
		}
		if !filepath.IsAbs(strings.TrimSpace(s.SkillsDir)) {
			return errors.New("skills dir must be an absolute path")
		}
	}
	if err := validateProxySettings(s); err != nil {
		return fmt.Errorf("proxy: %w", err)
	}
	return nil
}

// Prepare ensures the persistent sandbox container and host directories exist.
func Prepare(ctx context.Context, cfg Settings) error {
	cfg = normalizeSettings(cfg)
	verbosef("Prepare: begin for container=%q", cfg.ContainerName)
	if err := ctx.Err(); err != nil {
		verbosef("Prepare: context error before start: %v", err)
		return err
	}
	if err := cfg.Validate(); err != nil {
		verbosef("Prepare: settings validation failed: %v", err)
		return fmt.Errorf("invalid sandbox config: %w", err)
	}
	if err := ensureBuildahProcessEnvironment(); err != nil {
		verbosef("Prepare: buildah process environment failed: %v", err)
		return fmt.Errorf("prepare buildah process environment: %w", err)
	}

	verbosef("Prepare: ensuring workspace host dir exists: %q", cfg.WorkspaceHostDir)
	if err := os.MkdirAll(cfg.WorkspaceHostDir, 0o755); err != nil {
		verbosef("Prepare: mkdir failed: %v", err)
		return fmt.Errorf("create workspace host dir %q: %w", cfg.WorkspaceHostDir, err)
	}
	verbosef("Prepare: ensuring memory host dir exists: %q", cfg.MemoryHostDir)
	if err := os.MkdirAll(cfg.MemoryHostDir, 0o755); err != nil {
		verbosef("Prepare: memory mkdir failed: %v", err)
		return fmt.Errorf("create memory host dir %q: %w", cfg.MemoryHostDir, err)
	}
	if strings.TrimSpace(cfg.SkillsHostDir) != "" {
		verbosef("Prepare: ensuring skills host dir exists: %q", cfg.SkillsHostDir)
		if err := os.MkdirAll(cfg.SkillsHostDir, 0o755); err != nil {
			verbosef("Prepare: skills mkdir failed: %v", err)
			return fmt.Errorf("create skills host dir %q: %w", cfg.SkillsHostDir, err)
		}
	}
	storageHostDir, _ := defaultStorageHostDir()
	if storageHostDir != "" {
		verbosef("Prepare: ensuring storage host dir exists: %q", storageHostDir)
		if err := os.MkdirAll(storageHostDir, 0o755); err != nil {
			verbosef("Prepare: storage mkdir failed: %v", err)
			return fmt.Errorf("create storage host dir %q: %w", storageHostDir, err)
		}
	}
	nixHostDir, err := ensureSharedNixHostDir()
	if err != nil {
		verbosef("Prepare: shared nix host dir setup failed: %v", err)
		return fmt.Errorf("prepare shared nix host dir: %w", err)
	}
	verbosef("Prepare: shared nix store host dir=%q mounted_at=%q", nixHostDir, "/nix")
	if err := ctx.Err(); err != nil {
		verbosef("Prepare: context error after workspace setup: %v", err)
		return err
	}

	store, err := openStore()
	if err != nil {
		verbosef("Prepare: openStore failed: %v", err)
		return fmt.Errorf("open container storage: %w", err)
	}
	verbosef(
		"Prepare: storage opened (graph_root=%q run_root=%q driver=%q)",
		store.GraphRoot(),
		store.RunRoot(),
		store.GraphDriverName(),
	)

	builder, err := openOrCreateBuilder(ctx, store, cfg)
	if err != nil {
		verbosef("Prepare: openOrCreateBuilder failed: %v", err)
		return fmt.Errorf("ensure build container %q: %w", cfg.ContainerName, err)
	}
	if err := ensureNixBootstrap(builder, cfg); err != nil {
		verbosef("Prepare: ensureNixBootstrap failed: %v", err)
		return fmt.Errorf(
			"bootstrap nix in sandbox %q: %w (check sandbox network/proxy/CA settings)",
			cfg.ContainerName,
			err,
		)
	}

	verbosef(
		"Prepare: ready (container=%q container_id=%q base_image=%q)",
		cfg.ContainerName,
		builder.ContainerID,
		sandboxBaseImage,
	)
	return nil
}

// ExecRaw runs a raw command inside the prepared sandbox and returns stdout.
func ExecRaw(ctx context.Context, cfg Settings, command string) (string, error) {
	cfg = normalizeSettings(cfg)
	command = strings.TrimSpace(command)
	if command == "" {
		return "", errors.New("command is required")
	}
	verbosef(
		"ExecRaw: running command in container=%q workdir=%q mount=%q->%q command=%q",
		cfg.ContainerName,
		cfg.WorkspaceDir,
		cfg.WorkspaceHostDir,
		cfg.WorkspaceDir,
		command,
	)
	return execPreparedCommand(ctx, cfg, command, "ExecRaw")
}

func execPreparedCommand(
	ctx context.Context,
	cfg Settings,
	command string,
	actionName string,
) (string, error) {
	if err := cfg.Validate(); err != nil {
		return "", fmt.Errorf("invalid sandbox config: %w", err)
	}
	if err := ctx.Err(); err != nil {
		verbosef("%s: context error before start: %v", actionName, err)
		return "", err
	}
	if err := ensureBuildahProcessEnvironment(); err != nil {
		verbosef("%s: buildah process environment failed: %v", actionName, err)
		return "", fmt.Errorf("prepare buildah process environment: %w", err)
	}
	if _, err := ensureSharedNixHostDir(); err != nil {
		verbosef("%s: shared nix host dir setup failed: %v", actionName, err)
		return "", fmt.Errorf("prepare shared nix host dir: %w", err)
	}

	store, err := openStore()
	if err != nil {
		verbosef("%s: openStore failed: %v", actionName, err)
		return "", fmt.Errorf("open container storage: %w", err)
	}
	builder, err := openOrCreateBuilder(ctx, store, cfg)
	if err != nil {
		verbosef("%s: openOrCreateBuilder failed: %v", actionName, err)
		return "", fmt.Errorf("ensure build container %q: %w", cfg.ContainerName, err)
	}
	return runInBuilder(builder, cfg, command), nil
}

func normalizeSettings(cfg Settings) Settings {
	cfg.ContainerName = strings.TrimSpace(cfg.ContainerName)
	cfg.WorkspaceHostDir = cleanRequiredPath(cfg.WorkspaceHostDir)
	cfg.WorkspaceDir = cleanRequiredPath(cfg.WorkspaceDir)
	cfg.MemoryHostDir = cleanRequiredPath(cfg.MemoryHostDir)
	cfg.MemoryDir = cleanRequiredPath(cfg.MemoryDir)
	cfg.SkillsHostDir = cleanOptionalPath(cfg.SkillsHostDir)
	cfg.SkillsDir = cleanOptionalPath(cfg.SkillsDir)
	cfg.Proxy = normalizeProxySettings(cfg.Proxy)
	return cfg
}

func cleanRequiredPath(raw string) string {
	return filepath.Clean(strings.TrimSpace(raw))
}

func cleanOptionalPath(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	return filepath.Clean(raw)
}

func openStore() (storage.Store, error) {
	options, err := storage.DefaultStoreOptions()
	if err != nil {
		return nil, err
	}
	if graphRoot, ok := defaultStorageHostDir(); ok {
		options.GraphRoot = graphRoot
	}
	options.GraphDriverName = "vfs"
	verbosef(
		"openStore: options graph_root=%q run_root=%q driver=%q graph_driver_options=%q",
		options.GraphRoot,
		options.RunRoot,
		options.GraphDriverName,
		options.GraphDriverOptions,
	)
	return storage.GetStore(options)
}

func defaultStorageHostDir() (string, bool) {
	home := strings.TrimSpace(os.Getenv("HOME"))
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			verbosef("defaultStorageHostDir: unable to resolve user home: %v", err)
			return "", false
		}
	}
	if home == "" {
		return "", false
	}
	return filepath.Join(home, ".local", "share", "q15", "buildah-storage"), true
}

func defaultSharedNixHostDir() (string, bool) {
	if path := strings.TrimSpace(os.Getenv(sharedNixHostDirEnv)); path != "" {
		return path, true
	}

	home := strings.TrimSpace(os.Getenv("HOME"))
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			verbosef("defaultSharedNixHostDir: unable to resolve user home: %v", err)
			return "", false
		}
	}
	if home == "" {
		return "", false
	}
	return filepath.Join(home, ".local", "share", "q15", "nix-store"), true
}

func ensureSharedNixHostDir() (string, error) {
	hostDir, ok := defaultSharedNixHostDir()
	if !ok {
		return "", errors.New("unable to resolve shared nix host dir")
	}
	hostDir = filepath.Clean(strings.TrimSpace(hostDir))
	if hostDir == "" {
		return "", errors.New("shared nix host dir is empty")
	}
	if !filepath.IsAbs(hostDir) {
		return "", fmt.Errorf("%s must be an absolute path", sharedNixHostDirEnv)
	}
	if err := os.MkdirAll(hostDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %q: %w", hostDir, err)
	}
	return hostDir, nil
}

func sharedNixStoreMount() (specs.Mount, error) {
	hostDir, err := ensureSharedNixHostDir()
	if err != nil {
		return specs.Mount{}, err
	}
	return specs.Mount{
		Type:        "bind",
		Source:      hostDir,
		Destination: "/nix",
		Options:     []string{"rbind", "rw"},
	}, nil
}

func openOrCreateBuilder(
	ctx context.Context,
	store storage.Store,
	cfg Settings,
) (*buildah.Builder, error) {
	verbosef("openOrCreateBuilder: trying existing builder %q", cfg.ContainerName)
	builder, err := buildah.OpenBuilder(store, cfg.ContainerName)
	if err == nil {
		recreate, reason := shouldRecreateExistingBuilder(builder)
		if recreate {
			verbosef(
				"openOrCreateBuilder: recreating existing builder %q: %s",
				cfg.ContainerName,
				reason,
			)
			if err := builder.Delete(); err != nil {
				return nil, fmt.Errorf("delete stale builder %q: %w", cfg.ContainerName, err)
			}
		} else {
			verbosef(
				"openOrCreateBuilder: opened existing builder %q (id=%q image=%q)",
				cfg.ContainerName,
				builder.ContainerID,
				builder.FromImage,
			)
			return builder, nil
		}
	} else if !errors.Is(err, storage.ErrContainerUnknown) {
		verbosef("openOrCreateBuilder: open existing failed with non-notfound error: %v", err)
		return nil, err
	}

	verbosef(
		"openOrCreateBuilder: creating builder %q from image %q",
		cfg.ContainerName,
		sandboxBaseImage,
	)
	builder, err = buildah.NewBuilder(ctx, store, buildah.BuilderOptions{
		Container:        cfg.ContainerName,
		FromImage:        sandboxBaseImage,
		PullPolicy:       buildah.PullIfMissing,
		ReportWriter:     io.Discard,
		ConfigureNetwork: buildah.NetworkEnabled,
	})
	if err != nil {
		verbosef("openOrCreateBuilder: create failed: %v", err)
		return nil, err
	}
	builder.SetAnnotation(sandboxRuntimeAnnotation, sandboxRuntimeValue)
	if err := builder.Save(); err != nil {
		return nil, fmt.Errorf(
			"persist runtime annotation for builder %q: %w",
			cfg.ContainerName,
			err,
		)
	}
	verbosef(
		"openOrCreateBuilder: created builder %q (id=%q image=%q)",
		cfg.ContainerName,
		builder.ContainerID,
		builder.FromImage,
	)
	return builder, nil
}

func shouldRecreateExistingBuilder(builder *buildah.Builder) (bool, string) {
	if builder == nil {
		return true, "builder handle is nil"
	}
	annotation := strings.TrimSpace(builder.Annotations()[sandboxRuntimeAnnotation])
	if annotation != sandboxRuntimeValue {
		if annotation == "" {
			return true, "missing sandbox runtime annotation"
		}
		return true, fmt.Sprintf(
			"sandbox runtime annotation mismatch (got %q want %q)",
			annotation,
			sandboxRuntimeValue,
		)
	}
	return false, ""
}

func ensureNixBootstrap(builder *buildah.Builder, cfg Settings) error {
	stdout, stderr, err := runInBuilderRaw(builder, cfg, nixBootstrapCommand())
	if err == nil {
		return nil
	}
	return errors.New(formatCommandOutput(stdout, stderr, err))
}

func nixBootstrapCommand() string {
	return strings.TrimSpace(`
set -eu
if command -v nix >/dev/null 2>&1 && nix --version >/dev/null 2>&1; then
  exit 0
fi

apt-get -o APT::Sandbox::User=root update
DEBIAN_FRONTEND=noninteractive apt-get -o APT::Sandbox::User=root install -y bash ca-certificates curl xz-utils
if [ ! -d /nix ]; then
  echo "/nix is not mounted in the sandbox; configure shared nix store bind mount via Q15_SANDBOX_NIX_STORE_HOST_DIR" >&2
  exit 1
fi
if [ ! -w /nix ]; then
  echo "/nix mount is not writable in the sandbox; set Q15_SANDBOX_NIX_STORE_HOST_DIR to a writable host directory" >&2
  exit 1
fi
mkdir -p /nix/store /nix/var/nix /etc/nix
NIX_CONFIG="$(cat <<'EOF'
build-users-group =
experimental-features = nix-command flakes
sandbox = false
EOF
)"
export NIX_CONFIG
curl -fsSL https://nixos.org/nix/install | sh -s -- --no-daemon --yes --no-channel-add --no-modify-profile
cat > /etc/nix/nix.conf <<'EOF'
build-users-group =
experimental-features = nix-command flakes
sandbox = false
accept-flake-config = true
EOF
`)
}

func runInBuilder(builder *buildah.Builder, cfg Settings, command string) string {
	stdout, stderr, err := runInBuilderRaw(builder, cfg, command)
	return formatCommandOutput(stdout, stderr, err)
}

func runInBuilderRaw(
	builder *buildah.Builder,
	cfg Settings,
	command string,
) ([]byte, []byte, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	mounts := []specs.Mount{
		{
			Type:        "bind",
			Source:      cfg.WorkspaceHostDir,
			Destination: cfg.WorkspaceDir,
			Options:     []string{"rbind", "rw"},
		},
		{
			Type:        "bind",
			Source:      cfg.MemoryHostDir,
			Destination: cfg.MemoryDir,
			Options:     []string{"rbind", "rw"},
		},
	}
	if strings.TrimSpace(cfg.SkillsHostDir) != "" {
		mounts = append(mounts, specs.Mount{
			Type:        "bind",
			Source:      cfg.SkillsHostDir,
			Destination: cfg.SkillsDir,
			Options:     []string{"rbind", "rw"},
		})
	}
	sharedNixMount, err := sharedNixStoreMount()
	if err != nil {
		return nil, nil, fmt.Errorf("resolve shared nix store mount: %w", err)
	}
	mounts = append(mounts, sharedNixMount)
	mounts = append(mounts, proxyExtraMounts(cfg)...)

	err = builder.Run(
		[]string{"/bin/sh", "-c", wrapCommandWithProxyCABundle(cfg, command)},
		buildah.RunOptions{
			Isolation:        buildah.IsolationOCIRootless,
			Stdout:           &stdout,
			Stderr:           &stderr,
			Terminal:         buildah.WithoutTerminal,
			WorkingDir:       cfg.WorkspaceDir,
			ConfigureNetwork: buildah.NetworkEnabled,
			Env:              runEnv(cfg),
			Mounts:           mounts,
		},
	)
	if err != nil {
		verbosef("runInBuilder: command failed in container=%q: %v", cfg.ContainerName, err)
	} else {
		verbosef("runInBuilder: command completed in container=%q (stdout=%d bytes stderr=%d bytes)", cfg.ContainerName, stdout.Len(), stderr.Len())
	}

	return stdout.Bytes(), stderr.Bytes(), err
}

func wrapCommandWithProxyCABundle(cfg Settings, command string) string {
	if cfg.Proxy == nil || !cfg.Proxy.Enabled {
		return command
	}
	caPath := strings.TrimSpace(cfg.Proxy.CACertContainerPath)
	if caPath == "" {
		return command
	}

	quotedCAPath := shellSingleQuote(caPath)
	prefix := strings.Join(
		[]string{
			"if [ ! -r " + quotedCAPath + " ]; then",
			`  echo "proxy CA cert is not readable: ` + caPath + `" >&2`,
			"  exit 78",
			"fi",
			`q15_ca_bundle="/tmp/q15-ca-bundle.crt"`,
			"if [ -r /etc/ssl/certs/ca-certificates.crt ]; then",
			"  cat /etc/ssl/certs/ca-certificates.crt " + quotedCAPath + ` > "$q15_ca_bundle"`,
			"else",
			"  cat " + quotedCAPath + ` > "$q15_ca_bundle"`,
			"fi",
			`export SSL_CERT_FILE="$q15_ca_bundle"`,
			`export NIX_SSL_CERT_FILE="$q15_ca_bundle"`,
			`export NODE_EXTRA_CA_CERTS="$q15_ca_bundle"`,
			`export REQUESTS_CA_BUNDLE="$q15_ca_bundle"`,
			`export CURL_CA_BUNDLE="$q15_ca_bundle"`,
			`export GIT_SSL_CAINFO="$q15_ca_bundle"`,
		},
		"\n",
	)

	return prefix + "\n" + command
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func runEnv(cfg Settings) []string {
	env := []string{
		"PATH=/root/.nix-profile/bin:/nix/var/nix/profiles/default/bin:/nix/var/nix/profiles/per-user/root/profile/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	}
	env = append(env, proxyRunEnv(cfg)...)
	return env
}

func normalizeProxySettings(proxy *ProxySettings) *ProxySettings {
	if proxy == nil {
		return nil
	}
	normalized := *proxy
	normalized.HTTPProxyURL = strings.TrimSpace(proxy.HTTPProxyURL)
	normalized.HTTPSProxyURL = strings.TrimSpace(proxy.HTTPSProxyURL)
	normalized.AllProxyURL = strings.TrimSpace(proxy.AllProxyURL)
	normalized.NoProxy = strings.TrimSpace(proxy.NoProxy)
	if path := strings.TrimSpace(proxy.CACertHostPath); path != "" {
		normalized.CACertHostPath = filepath.Clean(path)
	} else {
		normalized.CACertHostPath = ""
	}
	if path := strings.TrimSpace(proxy.CACertContainerPath); path != "" {
		normalized.CACertContainerPath = filepath.Clean(path)
	} else {
		normalized.CACertContainerPath = ""
	}
	normalized.Env = maps.Clone(proxy.Env)
	return &normalized
}

func validateProxySettings(cfg Settings) error {
	if cfg.Proxy == nil || !cfg.Proxy.Enabled {
		return nil
	}

	p := cfg.Proxy
	if strings.TrimSpace(p.HTTPProxyURL) == "" &&
		strings.TrimSpace(p.HTTPSProxyURL) == "" &&
		strings.TrimSpace(p.AllProxyURL) == "" {
		return errors.New("at least one proxy URL is required when enabled")
	}
	if path := strings.TrimSpace(p.CACertHostPath); path != "" && !filepath.IsAbs(path) {
		return errors.New("ca cert host path must be an absolute path")
	}
	if path := strings.TrimSpace(p.CACertContainerPath); path != "" && !filepath.IsAbs(path) {
		return errors.New("ca cert container path must be an absolute path")
	}
	if (strings.TrimSpace(p.CACertHostPath) == "") != (strings.TrimSpace(p.CACertContainerPath) == "") {
		return errors.New("ca cert host/container paths must be set together")
	}
	return nil
}

func proxyRunEnv(cfg Settings) []string {
	if cfg.Proxy == nil || !cfg.Proxy.Enabled {
		return nil
	}
	p := cfg.Proxy
	var env []string
	appendKV := func(key, value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		env = append(env, key+"="+value)
	}

	if len(p.Env) > 0 {
		keys := make([]string, 0, len(p.Env))
		for key := range p.Env {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			appendKV(key, p.Env[key])
		}
	}

	appendKV("HTTP_PROXY", p.HTTPProxyURL)
	appendKV("HTTPS_PROXY", p.HTTPSProxyURL)
	appendKV("ALL_PROXY", p.AllProxyURL)
	appendKV("NO_PROXY", p.NoProxy)
	if p.SetLowercaseProxyEnv {
		appendKV("http_proxy", p.HTTPProxyURL)
		appendKV("https_proxy", p.HTTPSProxyURL)
		appendKV("all_proxy", p.AllProxyURL)
		appendKV("no_proxy", p.NoProxy)
	}

	if strings.TrimSpace(p.CACertContainerPath) != "" {
		for _, key := range []string{
			"SSL_CERT_FILE",
			"NIX_SSL_CERT_FILE",
			"NODE_EXTRA_CA_CERTS",
			"REQUESTS_CA_BUNDLE",
			"CURL_CA_BUNDLE",
			"GIT_SSL_CAINFO",
		} {
			appendKV(key, p.CACertContainerPath)
		}
	}

	return env
}

func proxyExtraMounts(cfg Settings) []specs.Mount {
	if cfg.Proxy == nil || !cfg.Proxy.Enabled {
		return nil
	}
	p := cfg.Proxy
	if strings.TrimSpace(p.CACertHostPath) == "" || strings.TrimSpace(p.CACertContainerPath) == "" {
		return nil
	}
	return []specs.Mount{
		{
			Type:        "bind",
			Source:      p.CACertHostPath,
			Destination: p.CACertContainerPath,
			Options:     []string{"rbind", "ro"},
		},
	}
}

func formatCommandOutput(stdout []byte, stderr []byte, err error) string {
	var output string
	switch {
	case len(stdout) > 0 && len(stderr) > 0:
		output = string(stdout) + string(stderr)
	case len(stdout) > 0:
		output = string(stdout)
	case len(stderr) > 0:
		output = string(stderr)
	default:
		output = ""
	}

	if err != nil {
		if output == "" {
			return "command failed: " + err.Error()
		}
		return "command failed: " + err.Error() + "\n" + output
	}
	if output == "" {
		return "(no output)"
	}
	return output
}
