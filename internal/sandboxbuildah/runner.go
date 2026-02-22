package sandboxbuildah

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/containers/buildah"
	specs "github.com/opencontainers/runtime-spec/specs-go"
	nettypes "go.podman.io/common/libnetwork/types"
	"go.podman.io/storage"
)

type Settings struct {
	ContainerName    string
	FromImage        string
	WorkspaceHostDir string
	WorkspaceDir     string
	Network          string
}

func (s Settings) Validate() error {
	if strings.TrimSpace(s.ContainerName) == "" {
		return errors.New("container name is required")
	}
	if strings.TrimSpace(s.FromImage) == "" {
		return errors.New("from image is required")
	}
	if strings.TrimSpace(s.WorkspaceHostDir) == "" {
		return errors.New("workspace host dir is required")
	}
	if strings.TrimSpace(s.WorkspaceDir) == "" {
		return errors.New("workspace dir is required")
	}
	if !filepath.IsAbs(strings.TrimSpace(s.WorkspaceHostDir)) {
		return errors.New("workspace host dir must be an absolute path")
	}
	if !filepath.IsAbs(strings.TrimSpace(s.WorkspaceDir)) {
		return errors.New("workspace dir must be an absolute path")
	}
	if _, err := normalizeNetworkMode(s.Network); err != nil {
		return fmt.Errorf("network: %w", err)
	}
	return nil
}

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
	storageHostDir, _ := defaultStorageHostDir()
	if storageHostDir != "" {
		verbosef("Prepare: ensuring storage host dir exists: %q", storageHostDir)
		if err := os.MkdirAll(storageHostDir, 0o755); err != nil {
			verbosef("Prepare: storage mkdir failed: %v", err)
			return fmt.Errorf("create storage host dir %q: %w", storageHostDir, err)
		}
	}
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
	verbosef(
		"Prepare: ready (container=%q container_id=%q from_image=%q network=%q)",
		cfg.ContainerName,
		builder.ContainerID,
		builder.FromImage,
		cfg.Network,
	)
	return nil
}

func Exec(ctx context.Context, cfg Settings, command string) (string, error) {
	cfg = normalizeSettings(cfg)
	command = strings.TrimSpace(command)
	if command == "" {
		return "", errors.New("command is required")
	}
	if err := cfg.Validate(); err != nil {
		return "", fmt.Errorf("invalid sandbox config: %w", err)
	}
	if err := ctx.Err(); err != nil {
		verbosef("Exec: context error before start: %v", err)
		return "", err
	}
	if err := ensureBuildahProcessEnvironment(); err != nil {
		verbosef("Exec: buildah process environment failed: %v", err)
		return "", fmt.Errorf("prepare buildah process environment: %w", err)
	}

	store, err := openStore()
	if err != nil {
		verbosef("Exec: openStore failed: %v", err)
		return "", fmt.Errorf("open container storage: %w", err)
	}
	builder, err := openOrCreateBuilder(ctx, store, cfg)
	if err != nil {
		verbosef("Exec: openOrCreateBuilder failed: %v", err)
		return "", fmt.Errorf("ensure build container %q: %w", cfg.ContainerName, err)
	}

	verbosef(
		"Exec: running command in container=%q workdir=%q mount=%q->%q command=%q",
		cfg.ContainerName,
		cfg.WorkspaceDir,
		cfg.WorkspaceHostDir,
		cfg.WorkspaceDir,
		command,
	)
	return runInBuilder(builder, cfg, command), nil
}

func normalizeSettings(cfg Settings) Settings {
	cfg.ContainerName = strings.TrimSpace(cfg.ContainerName)
	cfg.FromImage = strings.TrimSpace(cfg.FromImage)
	cfg.WorkspaceHostDir = filepath.Clean(strings.TrimSpace(cfg.WorkspaceHostDir))
	cfg.WorkspaceDir = filepath.Clean(strings.TrimSpace(cfg.WorkspaceDir))
	cfg.Network = normalizeNetworkModeOrDefault(cfg.Network)
	return cfg
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

func openOrCreateBuilder(
	ctx context.Context,
	store storage.Store,
	cfg Settings,
) (*buildah.Builder, error) {
	networkEnabled := cfg.Network == "enabled"
	var disabledNet nettypes.ContainerNetwork
	if !networkEnabled {
		disabledNet = newDisabledNetwork()
	}

	verbosef("openOrCreateBuilder: trying existing builder %q", cfg.ContainerName)
	builder, err := buildah.OpenBuilder(store, cfg.ContainerName)
	if err == nil {
		if disabledNet != nil {
			builder.NetworkInterface = disabledNet
		}
		verbosef(
			"openOrCreateBuilder: opened existing builder %q (id=%q image=%q)",
			cfg.ContainerName,
			builder.ContainerID,
			builder.FromImage,
		)
		return builder, nil
	}
	if !errors.Is(err, storage.ErrContainerUnknown) {
		verbosef("openOrCreateBuilder: open existing failed with non-notfound error: %v", err)
		return nil, err
	}
	verbosef(
		"openOrCreateBuilder: builder %q not found, creating from image %q",
		cfg.ContainerName,
		cfg.FromImage,
	)

	builderOpts := buildah.BuilderOptions{
		Container:    cfg.ContainerName,
		FromImage:    cfg.FromImage,
		PullPolicy:   buildah.PullIfMissing,
		ReportWriter: io.Discard,
	}
	if networkEnabled {
		builderOpts.ConfigureNetwork = buildah.NetworkEnabled
	} else {
		builderOpts.NetworkInterface = disabledNet
		builderOpts.ConfigureNetwork = buildah.NetworkDisabled
	}
	builder, err = buildah.NewBuilder(ctx, store, builderOpts)
	if err != nil {
		verbosef("openOrCreateBuilder: create failed: %v", err)
		return nil, err
	}
	if disabledNet != nil {
		builder.NetworkInterface = disabledNet
	}
	verbosef(
		"openOrCreateBuilder: created builder %q (id=%q image=%q)",
		cfg.ContainerName,
		builder.ContainerID,
		builder.FromImage,
	)
	return builder, nil
}

func runInBuilder(builder *buildah.Builder, cfg Settings, command string) string {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	err := builder.Run(
		[]string{"/bin/sh", "-c", command},
		buildah.RunOptions{
			Isolation:        buildah.IsolationOCIRootless,
			Stdout:           &stdout,
			Stderr:           &stderr,
			Terminal:         buildah.WithoutTerminal,
			WorkingDir:       cfg.WorkspaceDir,
			ConfigureNetwork: runNetworkPolicy(cfg),
			Mounts: []specs.Mount{
				{
					Type:        "bind",
					Source:      cfg.WorkspaceHostDir,
					Destination: cfg.WorkspaceDir,
					Options:     []string{"rbind", "rw"},
				},
			},
		},
	)
	if err != nil {
		verbosef("runInBuilder: command failed in container=%q: %v", cfg.ContainerName, err)
	} else {
		verbosef("runInBuilder: command completed in container=%q (stdout=%d bytes stderr=%d bytes)", cfg.ContainerName, stdout.Len(), stderr.Len())
	}

	return formatCommandOutput(stdout.Bytes(), stderr.Bytes(), err)
}

func runNetworkPolicy(cfg Settings) buildah.NetworkConfigurationPolicy {
	if cfg.Network == "enabled" {
		return buildah.NetworkEnabled
	}
	return buildah.NetworkDisabled
}

func normalizeNetworkModeOrDefault(mode string) string {
	normalized, err := normalizeNetworkMode(mode)
	if err != nil {
		return strings.ToLower(strings.TrimSpace(mode))
	}
	return normalized
}

func normalizeNetworkMode(mode string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "disabled":
		return "disabled", nil
	case "enabled":
		return "enabled", nil
	default:
		return "", errors.New(`must be "enabled" or "disabled"`)
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
