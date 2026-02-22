package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
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

type Sandbox struct {
	cfg       Settings
	mu        sync.Mutex
	prepared  bool
	helperBin string
}

func New(cfg Settings) *Sandbox {
	cfg.ContainerName = strings.TrimSpace(cfg.ContainerName)
	cfg.FromImage = strings.TrimSpace(cfg.FromImage)
	cfg.WorkspaceHostDir = filepath.Clean(strings.TrimSpace(cfg.WorkspaceHostDir))
	cfg.WorkspaceDir = filepath.Clean(strings.TrimSpace(cfg.WorkspaceDir))
	cfg.Network = normalizeNetworkModeOrDefault(cfg.Network)
	verbosef(
		"New: container=%q from_image=%q workspace_host_dir=%q workspace_dir=%q network=%q",
		cfg.ContainerName,
		cfg.FromImage,
		cfg.WorkspaceHostDir,
		cfg.WorkspaceDir,
		cfg.Network,
	)
	return &Sandbox{cfg: cfg}
}

func (s *Sandbox) Prepare(ctx context.Context) error {
	verbosef("Prepare: begin for container=%q", s.cfg.ContainerName)
	if err := ctx.Err(); err != nil {
		verbosef("Prepare: context error before start: %v", err)
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.prepared {
		verbosef("Prepare: already prepared for container=%q", s.cfg.ContainerName)
		return nil
	}
	if err := s.cfg.Validate(); err != nil {
		verbosef("Prepare: settings validation failed: %v", err)
		return fmt.Errorf("invalid sandbox config: %w", err)
	}
	verbosef("Prepare: ensuring workspace host dir exists: %q", s.cfg.WorkspaceHostDir)
	if err := os.MkdirAll(s.cfg.WorkspaceHostDir, 0o755); err != nil {
		verbosef("Prepare: mkdir failed: %v", err)
		return fmt.Errorf("create workspace host dir %q: %w", s.cfg.WorkspaceHostDir, err)
	}
	if err := ctx.Err(); err != nil {
		verbosef("Prepare: context error after workspace setup: %v", err)
		return err
	}

	if _, err := s.runHelperLocked(ctx, "prepare", ""); err != nil {
		verbosef("Prepare: helper failed: %v", err)
		return err
	}

	s.prepared = true
	verbosef("Prepare: ready (container=%q)", s.cfg.ContainerName)
	return nil
}

func (s *Sandbox) Exec(ctx context.Context, command string) (string, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return "", errors.New("command is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.prepared {
		return "", errors.New("sandbox is not prepared")
	}
	if err := ctx.Err(); err != nil {
		verbosef("Exec: context error before run for container=%q: %v", s.cfg.ContainerName, err)
		return "", err
	}

	verbosef(
		"Exec: running command in container=%q workdir=%q mount=%q->%q command=%q",
		s.cfg.ContainerName,
		s.cfg.WorkspaceDir,
		s.cfg.WorkspaceHostDir,
		s.cfg.WorkspaceDir,
		command,
	)
	return s.runHelperLocked(ctx, "exec", command)
}

func (s *Sandbox) runHelperLocked(
	ctx context.Context,
	action string,
	command string,
) (string, error) {
	helperBin, err := s.helperBinaryLocked()
	if err != nil {
		return "", err
	}

	reqBytes, err := json.Marshal(HelperRequest{
		Settings: s.cfg,
		Command:  command,
	})
	if err != nil {
		return "", fmt.Errorf("marshal helper request: %w", err)
	}

	cmd := helperCommand(ctx, helperBin, action, s.cfg)
	cmd.Stdin = bytes.NewReader(reqBytes)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	if VerboseEnabled() {
		cmd.Stderr = io.MultiWriter(&stderr, os.Stdout)
	} else {
		cmd.Stderr = &stderr
	}

	verbosef(
		"helper: exec=%q args=%q action=%q container=%q",
		cmd.Path,
		cmd.Args,
		action,
		s.cfg.ContainerName,
	)
	runErr := cmd.Run()

	var resp HelperResponse
	parseErr := json.Unmarshal(stdout.Bytes(), &resp)
	if parseErr != nil && stdout.Len() > 0 {
		verbosef("helper: response parse failed action=%q: %v", action, parseErr)
	}

	if runErr != nil {
		if resp.Error != "" {
			return "", fmt.Errorf("sandbox helper %q failed: %s", action, resp.Error)
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg == "" {
			return "", fmt.Errorf("sandbox helper %q failed: %w", action, runErr)
		}
		return "", fmt.Errorf("sandbox helper %q failed: %s", action, msg)
	}
	if parseErr != nil {
		return "", fmt.Errorf("decode sandbox helper response for %q: %w", action, parseErr)
	}
	if resp.Error != "" {
		return "", errors.New(resp.Error)
	}
	return resp.Output, nil
}

func helperCommand(ctx context.Context, helperBin, action string, _ Settings) *exec.Cmd {
	if usePodmanUnshare() {
		cmd := exec.CommandContext(ctx, "podman", "unshare", helperBin, action)
		cmd.Env = filterEnv(os.Environ(), "STORAGE_DRIVER")
		return cmd
	}
	return exec.CommandContext(ctx, helperBin, action)
}

func usePodmanUnshare() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("Q15_SANDBOX_USE_PODMAN_UNSHARE"))) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	if runtime.GOOS != "linux" || os.Geteuid() == 0 {
		return false
	}
	if _, err := exec.LookPath("podman"); err != nil {
		verbosef("usePodmanUnshare: default disabled because podman is not in PATH: %v", err)
		return false
	}
	return true
}

func filterEnv(env []string, blockedKeys ...string) []string {
	if len(blockedKeys) == 0 || len(env) == 0 {
		return env
	}
	blocked := make(map[string]struct{}, len(blockedKeys))
	for _, k := range blockedKeys {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		blocked[k] = struct{}{}
	}
	out := make([]string, 0, len(env))
	for _, kv := range env {
		key, _, ok := strings.Cut(kv, "=")
		if !ok {
			out = append(out, kv)
			continue
		}
		if _, deny := blocked[key]; deny {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func (s *Sandbox) helperBinaryLocked() (string, error) {
	if s.helperBin != "" {
		return s.helperBin, nil
	}

	path, err := resolveHelperBinary()
	if err != nil {
		return "", err
	}
	s.helperBin = path
	return s.helperBin, nil
}

func resolveHelperBinary() (string, error) {
	if p := strings.TrimSpace(os.Getenv("Q15_SANDBOX_HELPER_BIN")); p != "" {
		if strings.ContainsRune(p, os.PathSeparator) {
			return p, nil
		}
		resolved, err := exec.LookPath(p)
		if err == nil {
			return resolved, nil
		}
		return "", fmt.Errorf("resolve Q15_SANDBOX_HELPER_BIN=%q: %w", p, err)
	}

	if exe, err := os.Executable(); err == nil {
		sibling := filepath.Join(filepath.Dir(exe), "q15-sandbox-helper")
		if _, err := os.Stat(sibling); err == nil {
			return sibling, nil
		}
	}

	if p, err := exec.LookPath("q15-sandbox-helper"); err == nil {
		return p, nil
	}

	return "", errors.New(
		"sandbox helper binary not found (build ./cmd/q15-sandbox-helper and set Q15_SANDBOX_HELPER_BIN if needed)",
	)
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
