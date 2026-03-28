package service

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"syscall"
)

const (
	runtimeBashPackage = "nixpkgs#bash"
	defaultSSLCertFile = "/etc/ssl/certs/ca-certificates.crt"
	nixBootstrapHint   = "this usually means /nix is mounted empty or missing its bootstrap runtime"
)

// NixShellExecutor starts commands through nix shell and bash.
type NixShellExecutor struct{}

// Type reports the executor implementation label.
func (NixShellExecutor) Type() string {
	return "local-nix-shell"
}

// Start launches one command in a nix shell and returns process pipes.
func (NixShellExecutor) Start(ctx context.Context, req CommandRequest) (*RunningCommand, error) {
	req, err := normalizeCommandRequest(req)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	env := mergeEnv(os.Environ(), req.Env)
	nixPath, err := resolveRuntimeExecutable("nix")
	if err != nil {
		return nil, err
	}
	sslCertFile, err := resolveNixSSLCertFile(env)
	if err != nil {
		return nil, err
	}

	cmd := exec.Command(nixPath, buildNixShellArgs(req.Command, req.Packages, sslCertFile)...)
	cmd.Dir = req.WorkingDir
	cmd.Env = env

	return startPreparedCommand(cmd)
}

// ShellExecutor is a test-friendly executor that runs commands directly via bash.
type ShellExecutor struct{}

// Type reports the executor implementation label.
func (ShellExecutor) Type() string {
	return "local-shell"
}

// Start launches one command directly via bash.
func (ShellExecutor) Start(ctx context.Context, req CommandRequest) (*RunningCommand, error) {
	req, err := normalizeCommandRequest(req)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	bashPath, err := resolveRuntimeExecutable("bash")
	if err != nil {
		return nil, err
	}

	cmd := exec.Command(bashPath, "-lc", req.Command)
	cmd.Dir = req.WorkingDir
	cmd.Env = mergeEnv(os.Environ(), req.Env)

	return startPreparedCommand(cmd)
}

func startPreparedCommand(cmd *exec.Cmd) (*RunningCommand, error) {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open stdin pipe: %w", err)
	}
	stdout, stdoutWriter := io.Pipe()
	stderr, stderrWriter := io.Pipe()
	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderrWriter
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stdoutWriter.Close()
		_ = stderr.Close()
		_ = stderrWriter.Close()
		return nil, fmt.Errorf("start command: %w", err)
	}

	return &RunningCommand{
		Stdin:  stdin,
		Stdout: stdout,
		Stderr: stderr,
		Wait: func() error {
			err := cmd.Wait()
			_ = stdoutWriter.Close()
			_ = stderrWriter.Close()
			return err
		},
		Terminate: func(force bool) error {
			if cmd.Process == nil {
				return nil
			}
			sig := syscall.SIGTERM
			if force {
				sig = syscall.SIGKILL
			}
			if err := cmd.Process.Signal(sig); err != nil && !isAlreadyExited(err) {
				return err
			}
			return nil
		},
	}, nil
}

func normalizeCommandRequest(req CommandRequest) (CommandRequest, error) {
	req.Command = strings.TrimSpace(req.Command)
	if req.Command == "" {
		return CommandRequest{}, fmt.Errorf("command is required")
	}
	req.WorkingDir = strings.TrimSpace(req.WorkingDir)
	if req.WorkingDir == "" {
		return CommandRequest{}, fmt.Errorf("working dir is required")
	}
	req.Packages = normalizePackages(req.Packages)
	return req, nil
}

func normalizePackages(packages []string) []string {
	out := make([]string, 0, len(packages))
	for _, pkg := range packages {
		pkg = strings.TrimSpace(pkg)
		if pkg == "" {
			continue
		}
		out = append(out, pkg)
	}
	return out
}

func buildNixShellArgs(command string, packages []string, sslCertFile string) []string {
	packages = ensurePackage(packages, runtimeBashPackage)

	args := []string{
		"--extra-experimental-features",
		"nix-command flakes",
		"--option",
		"ssl-cert-file",
		sslCertFile,
		"shell",
	}
	args = append(args, packages...)
	args = append(args, "--command", "bash", "-lc", command)
	return args
}

func ensurePackage(packages []string, required string) []string {
	if slices.Contains(packages, required) {
		return append([]string(nil), packages...)
	}

	out := make([]string, 0, len(packages)+1)
	out = append(out, packages...)
	out = append(out, required)
	return out
}

func resolveRuntimeExecutable(name string) (string, error) {
	path, err := exec.LookPath(name)
	if err == nil {
		return path, nil
	}
	return "", fmt.Errorf(
		"runtime dependency %q not found in PATH: %w; %s",
		name,
		err,
		nixBootstrapHint,
	)
}

func resolveNixSSLCertFile(env []string) (string, error) {
	path := strings.TrimSpace(envValue(env, "NIX_SSL_CERT_FILE"))
	if path == "" {
		path = defaultSSLCertFile
	}
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf(
			"NIX_SSL_CERT_FILE %q is not readable: %w; %s",
			path,
			err,
			nixBootstrapHint,
		)
	}
	return path, nil
}

func envValue(entries []string, key string) string {
	for _, entry := range entries {
		entryKey, value, ok := strings.Cut(entry, "=")
		if ok && entryKey == key {
			return value
		}
	}
	return ""
}

func mergeEnv(base []string, overrides []string) []string {
	if len(overrides) == 0 {
		return slices.Clone(base)
	}

	indexByKey := make(map[string]int, len(base))
	out := make([]string, 0, len(base)+len(overrides))
	for _, entry := range base {
		key := envKey(entry)
		if key == "" {
			continue
		}
		indexByKey[key] = len(out)
		out = append(out, entry)
	}
	for _, entry := range overrides {
		key := envKey(entry)
		if key == "" {
			continue
		}
		if idx, ok := indexByKey[key]; ok {
			out[idx] = entry
			continue
		}
		indexByKey[key] = len(out)
		out = append(out, entry)
	}
	return out
}

func envKey(entry string) string {
	key, _, ok := strings.Cut(entry, "=")
	if !ok {
		return ""
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	return key
}

func isAlreadyExited(err error) bool {
	return err == io.EOF || strings.Contains(strings.ToLower(err.Error()), "finished")
}
