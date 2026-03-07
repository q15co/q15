package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	sandboxcontract "github.com/q15co/q15/libs/sandbox-contract"
)

// Settings configures the persistent sandbox container and its mounted paths.
type Settings struct {
	ContainerName    string
	WorkspaceHostDir string
	WorkspaceDir     string
	MemoryHostDir    string
	MemoryDir        string
	Proxy            *ProxySettings
}

// ProxySettings reuses the sandbox helper IPC contract shape directly.
// Keeping this as an alias removes one translation layer in the agent sandbox adapter.
type ProxySettings = sandboxcontract.ProxySettings

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
	if err := validateProxySettings(s); err != nil {
		return fmt.Errorf("proxy: %w", err)
	}
	return nil
}

// Sandbox manages a persistent helper-backed execution environment.
type Sandbox struct {
	cfg       Settings
	mu        sync.Mutex
	prepared  bool
	helperBin string
}

// New normalizes cfg and returns a sandbox handle.
func New(cfg Settings) *Sandbox {
	cfg.ContainerName = strings.TrimSpace(cfg.ContainerName)
	cfg.WorkspaceHostDir = filepath.Clean(strings.TrimSpace(cfg.WorkspaceHostDir))
	cfg.WorkspaceDir = filepath.Clean(strings.TrimSpace(cfg.WorkspaceDir))
	cfg.MemoryHostDir = filepath.Clean(strings.TrimSpace(cfg.MemoryHostDir))
	cfg.MemoryDir = filepath.Clean(strings.TrimSpace(cfg.MemoryDir))
	cfg.Proxy = normalizeProxySettings(cfg.Proxy)
	verbosef(
		"New: container=%q sandbox_runtime=%q workspace_host_dir=%q workspace_dir=%q",
		cfg.ContainerName,
		"nix-only",
		cfg.WorkspaceHostDir,
		cfg.WorkspaceDir,
	)
	return &Sandbox{cfg: cfg}
}

// Prepare ensures the sandbox container and required host directories exist.
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
	verbosef("Prepare: ensuring memory host dir exists: %q", s.cfg.MemoryHostDir)
	if err := os.MkdirAll(s.cfg.MemoryHostDir, 0o755); err != nil {
		verbosef("Prepare: memory mkdir failed: %v", err)
		return fmt.Errorf("create memory host dir %q: %w", s.cfg.MemoryHostDir, err)
	}
	if err := ctx.Err(); err != nil {
		verbosef("Prepare: context error after workspace setup: %v", err)
		return err
	}

	if _, err := s.runHelperLocked(ctx, "prepare", newHelperRequest(s.cfg)); err != nil {
		verbosef("Prepare: helper failed: %v", err)
		return err
	}

	s.prepared = true
	verbosef("Prepare: ready (container=%q)", s.cfg.ContainerName)
	return nil
}

// ExecRaw runs a raw command inside the prepared sandbox and returns stdout.
func (s *Sandbox) ExecRaw(ctx context.Context, command string) (string, error) {
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
		verbosef("ExecRaw: context error before run for container=%q: %v", s.cfg.ContainerName, err)
		return "", err
	}

	verbosef(
		"ExecRaw: running command in container=%q workdir=%q mount=%q->%q command=%q",
		s.cfg.ContainerName,
		s.cfg.WorkspaceDir,
		s.cfg.WorkspaceHostDir,
		s.cfg.WorkspaceDir,
		command,
	)
	return s.runHelperLocked(ctx, "exec-raw", newRawExecHelperRequest(s.cfg, command))
}

// ExecNixShellBash runs a command inside the prepared sandbox via nix shell and
// bash with explicit Nix packages.
func (s *Sandbox) ExecNixShellBash(
	ctx context.Context,
	command string,
	packages []string,
) (string, error) {
	req, err := newExecNixShellBashRequest(command, packages)
	if err != nil {
		return "", err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.prepared {
		return "", errors.New("sandbox is not prepared")
	}
	if err := ctx.Err(); err != nil {
		verbosef(
			"ExecNixShellBash: context error before run for container=%q: %v",
			s.cfg.ContainerName,
			err,
		)
		return "", err
	}

	logExecNixShellBashRequest(s.cfg.ContainerName, req)
	output, err := s.runHelperLocked(
		ctx,
		"exec-nix-shell-bash",
		newExecNixShellBashHelperRequest(s.cfg, req),
	)
	if err != nil {
		logExecNixShellBashFailure(s.cfg.ContainerName, req, err)
		return "", err
	}
	return output, nil
}

// ReadFile reads text from the sandbox workspace or memory roots through the helper.
func (s *Sandbox) ReadFile(
	ctx context.Context,
	path string,
	offsetLines int,
	limitLines int,
) (ReadFileResult, error) {
	req, err := newReadFileRequest(path, offsetLines, limitLines)
	if err != nil {
		return ReadFileResult{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.prepared {
		return ReadFileResult{}, errors.New("sandbox is not prepared")
	}
	if err := ctx.Err(); err != nil {
		return ReadFileResult{}, err
	}

	resp, err := s.callHelperLocked(ctx, "read-file", newReadFileHelperRequest(s.cfg, req))
	if err != nil {
		return ReadFileResult{}, err
	}
	if resp.ReadFile == nil {
		return ReadFileResult{}, errors.New("sandbox helper returned empty read_file result")
	}
	return *resp.ReadFile, nil
}

// WriteFile writes text into the sandbox workspace or memory roots through the helper.
func (s *Sandbox) WriteFile(
	ctx context.Context,
	path string,
	content string,
) (WriteFileResult, error) {
	req, err := newWriteFileRequest(path, content)
	if err != nil {
		return WriteFileResult{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.prepared {
		return WriteFileResult{}, errors.New("sandbox is not prepared")
	}
	if err := ctx.Err(); err != nil {
		return WriteFileResult{}, err
	}

	resp, err := s.callHelperLocked(ctx, "write-file", newWriteFileHelperRequest(s.cfg, req))
	if err != nil {
		return WriteFileResult{}, err
	}
	if resp.WriteFile == nil {
		return WriteFileResult{}, errors.New("sandbox helper returned empty write_file result")
	}
	return *resp.WriteFile, nil
}

// EditFile performs one exact text replacement through the sandbox helper.
func (s *Sandbox) EditFile(
	ctx context.Context,
	path string,
	oldText string,
	newText string,
) (EditFileResult, error) {
	req, err := newEditFileRequest(path, oldText, newText)
	if err != nil {
		return EditFileResult{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.prepared {
		return EditFileResult{}, errors.New("sandbox is not prepared")
	}
	if err := ctx.Err(); err != nil {
		return EditFileResult{}, err
	}

	resp, err := s.callHelperLocked(ctx, "edit-file", newEditFileHelperRequest(s.cfg, req))
	if err != nil {
		return EditFileResult{}, err
	}
	if resp.EditFile == nil {
		return EditFileResult{}, errors.New("sandbox helper returned empty edit_file result")
	}
	return *resp.EditFile, nil
}

// ApplyPatch applies a multi-file patch through the sandbox helper.
func (s *Sandbox) ApplyPatch(
	ctx context.Context,
	patch string,
) (ApplyPatchResult, error) {
	req, err := newApplyPatchRequest(patch)
	if err != nil {
		return ApplyPatchResult{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.prepared {
		return ApplyPatchResult{}, errors.New("sandbox is not prepared")
	}
	if err := ctx.Err(); err != nil {
		return ApplyPatchResult{}, err
	}

	resp, err := s.callHelperLocked(ctx, "apply-patch", newApplyPatchHelperRequest(s.cfg, req))
	if err != nil {
		return ApplyPatchResult{}, err
	}
	if resp.ApplyPatch == nil {
		return ApplyPatchResult{}, errors.New("sandbox helper returned empty apply_patch result")
	}
	return *resp.ApplyPatch, nil
}

func (s *Sandbox) runHelperLocked(
	ctx context.Context,
	action string,
	req sandboxcontract.HelperRequest,
) (string, error) {
	resp, err := s.callHelperLocked(ctx, action, req)
	if err != nil {
		return "", err
	}
	return resp.Output, nil
}

func (s *Sandbox) helperMetadata(ctx context.Context) (RuntimeMetadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	resp, err := s.callHelperLocked(ctx, "metadata", newHelperRequest(s.cfg))
	if err != nil {
		return RuntimeMetadata{}, err
	}
	if resp.Metadata == nil {
		return RuntimeMetadata{}, errors.New("sandbox helper returned empty metadata")
	}
	return *resp.Metadata, nil
}

func (s *Sandbox) callHelperLocked(
	ctx context.Context,
	action string,
	req sandboxcontract.HelperRequest,
) (sandboxcontract.HelperResponse, error) {
	helperBin, err := s.helperBinaryLocked()
	if err != nil {
		return sandboxcontract.HelperResponse{}, err
	}

	reqBytes, err := json.Marshal(req)
	if err != nil {
		return sandboxcontract.HelperResponse{}, fmt.Errorf("marshal helper request: %w", err)
	}

	cmd := helperCommand(ctx, helperBin, action)
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

	var resp sandboxcontract.HelperResponse
	parseErr := json.Unmarshal(stdout.Bytes(), &resp)
	if parseErr != nil && stdout.Len() > 0 {
		verbosef("helper: response parse failed action=%q: %v", action, parseErr)
	}

	if runErr != nil {
		if resp.Error != "" {
			return sandboxcontract.HelperResponse{}, fmt.Errorf(
				"sandbox helper %q failed: %s",
				action,
				resp.Error,
			)
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg == "" {
			return sandboxcontract.HelperResponse{}, fmt.Errorf(
				"sandbox helper %q failed: %w",
				action,
				runErr,
			)
		}
		return sandboxcontract.HelperResponse{}, fmt.Errorf(
			"sandbox helper %q failed: %s",
			action,
			msg,
		)
	}
	if parseErr != nil {
		return sandboxcontract.HelperResponse{}, fmt.Errorf(
			"decode sandbox helper response for %q: %w",
			action,
			parseErr,
		)
	}
	if resp.Error != "" {
		return sandboxcontract.HelperResponse{}, errors.New(resp.Error)
	}
	return resp, nil
}

func toContractSettings(cfg Settings) sandboxcontract.Settings {
	return sandboxcontract.Settings{
		ContainerName:    cfg.ContainerName,
		WorkspaceHostDir: cfg.WorkspaceHostDir,
		WorkspaceDir:     cfg.WorkspaceDir,
		MemoryHostDir:    cfg.MemoryHostDir,
		MemoryDir:        cfg.MemoryDir,
		Proxy:            cfg.Proxy,
	}
}

func newHelperRequest(cfg Settings) sandboxcontract.HelperRequest {
	return sandboxcontract.HelperRequest{Settings: toContractSettings(cfg)}
}

func newRawExecHelperRequest(cfg Settings, command string) sandboxcontract.HelperRequest {
	req := newHelperRequest(cfg)
	req.Command = command
	return req
}

func newExecNixShellBashHelperRequest(
	cfg Settings,
	req ExecNixShellBashRequest,
) sandboxcontract.HelperRequest {
	payload := newHelperRequest(cfg)
	payload.ExecNixShellBash = &sandboxcontract.ExecNixShellBashRequest{
		Command:  req.Command,
		Packages: append([]string(nil), req.Packages...),
	}
	return payload
}

func newReadFileHelperRequest(
	cfg Settings,
	req sandboxcontract.ReadFileRequest,
) sandboxcontract.HelperRequest {
	payload := newHelperRequest(cfg)
	payload.ReadFile = &sandboxcontract.ReadFileRequest{
		Path:        req.Path,
		OffsetLines: req.OffsetLines,
		LimitLines:  req.LimitLines,
	}
	return payload
}

func newWriteFileHelperRequest(
	cfg Settings,
	req sandboxcontract.WriteFileRequest,
) sandboxcontract.HelperRequest {
	payload := newHelperRequest(cfg)
	payload.WriteFile = &sandboxcontract.WriteFileRequest{
		Path:    req.Path,
		Content: req.Content,
	}
	return payload
}

func newEditFileHelperRequest(
	cfg Settings,
	req sandboxcontract.EditFileRequest,
) sandboxcontract.HelperRequest {
	payload := newHelperRequest(cfg)
	payload.EditFile = &sandboxcontract.EditFileRequest{
		Path:    req.Path,
		OldText: req.OldText,
		NewText: req.NewText,
	}
	return payload
}

func newApplyPatchHelperRequest(
	cfg Settings,
	req sandboxcontract.ApplyPatchRequest,
) sandboxcontract.HelperRequest {
	payload := newHelperRequest(cfg)
	payload.ApplyPatch = &sandboxcontract.ApplyPatchRequest{Patch: req.Patch}
	return payload
}

func newExecNixShellBashRequest(
	command string,
	packages []string,
) (ExecNixShellBashRequest, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return ExecNixShellBashRequest{}, errors.New("command is required")
	}
	normalizedPackages, err := normalizePackages(packages)
	if err != nil {
		return ExecNixShellBashRequest{}, err
	}
	return ExecNixShellBashRequest{
		Command:  command,
		Packages: normalizedPackages,
	}, nil
}

func newReadFileRequest(
	path string,
	offsetLines int,
	limitLines int,
) (sandboxcontract.ReadFileRequest, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return sandboxcontract.ReadFileRequest{}, errors.New("path is required")
	}
	if offsetLines < 0 {
		return sandboxcontract.ReadFileRequest{}, errors.New("offset_lines must be >= 0")
	}
	if limitLines < 0 {
		return sandboxcontract.ReadFileRequest{}, errors.New("limit_lines must be >= 0")
	}
	return sandboxcontract.ReadFileRequest{
		Path:        path,
		OffsetLines: offsetLines,
		LimitLines:  limitLines,
	}, nil
}

func newWriteFileRequest(
	path string,
	content string,
) (sandboxcontract.WriteFileRequest, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return sandboxcontract.WriteFileRequest{}, errors.New("path is required")
	}
	return sandboxcontract.WriteFileRequest{
		Path:    path,
		Content: content,
	}, nil
}

func newEditFileRequest(
	path string,
	oldText string,
	newText string,
) (sandboxcontract.EditFileRequest, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return sandboxcontract.EditFileRequest{}, errors.New("path is required")
	}
	if oldText == "" {
		return sandboxcontract.EditFileRequest{}, errors.New("old_text is required")
	}
	return sandboxcontract.EditFileRequest{
		Path:    path,
		OldText: oldText,
		NewText: newText,
	}, nil
}

func newApplyPatchRequest(patch string) (sandboxcontract.ApplyPatchRequest, error) {
	if strings.TrimSpace(patch) == "" {
		return sandboxcontract.ApplyPatchRequest{}, errors.New("patch is required")
	}
	return sandboxcontract.ApplyPatchRequest{Patch: patch}, nil
}

func normalizePackages(packages []string) ([]string, error) {
	if len(packages) == 0 {
		return nil, errors.New("packages are required")
	}
	out := make([]string, 0, len(packages))
	for i, pkg := range packages {
		pkg = strings.TrimSpace(pkg)
		if pkg == "" {
			return nil, fmt.Errorf("packages[%d] must not be empty", i)
		}
		out = append(out, pkg)
	}
	return out, nil
}

func helperCommand(ctx context.Context, helperBin, action string) *exec.Cmd {
	return exec.CommandContext(ctx, helperBin, action)
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
		"sandbox helper binary not found (build ./systems/sandbox-helper and set Q15_SANDBOX_HELPER_BIN if needed)",
	)
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
