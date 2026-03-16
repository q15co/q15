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

	cmd := exec.Command("/bin/sh", "-lc", buildNixShellBashCommand(req.Command, req.Packages))
	cmd.Dir = req.WorkingDir
	cmd.Env = mergeEnv(os.Environ(), req.Env)

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

	cmd := exec.Command("/bin/sh", "-lc", req.Command)
	cmd.Dir = req.WorkingDir
	cmd.Env = mergeEnv(os.Environ(), req.Env)

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

func buildNixShellBashCommand(command string, packages []string) string {
	parts := []string{
		"command -v nix >/dev/null 2>&1 || { echo 'nix not found in exec service runtime' >&2; exit 127; };",
		"if [ -n \"${NIX_SSL_CERT_FILE:-}\" ] && [ ! -r \"${NIX_SSL_CERT_FILE}\" ]; then echo \"NIX_SSL_CERT_FILE is set but not readable: ${NIX_SSL_CERT_FILE}\" >&2; exit 78; fi;",
		"nix",
		"--extra-experimental-features",
		shellSingleQuote("nix-command flakes"),
		"--option",
		"ssl-cert-file",
		"\"${NIX_SSL_CERT_FILE:-/etc/ssl/certs/ca-certificates.crt}\"",
		"shell",
	}
	for _, pkg := range packages {
		parts = append(parts, shellSingleQuote(pkg))
	}
	parts = append(parts, "--command", "/bin/sh", "-lc", shellSingleQuote(command))
	return strings.Join(parts, " ")
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
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
