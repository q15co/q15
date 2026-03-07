package sandboxbuildah

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ExecNixShellBashRequest describes a command run that requires explicit Nix
// packages and executes via bash inside a nix shell.
type ExecNixShellBashRequest struct {
	Command  string
	Packages []string
}

// ExecNixShellBash runs a command inside a nix shell and bash with explicit
// packages.
func ExecNixShellBash(
	ctx context.Context,
	cfg Settings,
	req ExecNixShellBashRequest,
) (string, error) {
	req, err := normalizeExecNixShellBashRequest(req)
	if err != nil {
		return "", err
	}
	cfg = normalizeSettings(cfg)
	command := buildNixShellBashCommand(req.Command, req.Packages)
	verbosef(
		"ExecNixShellBash: running command in container=%q workdir=%q mount=%q->%q requested_command=%q packages=%v assembled_command=%q",
		cfg.ContainerName,
		cfg.WorkspaceDir,
		cfg.WorkspaceHostDir,
		cfg.WorkspaceDir,
		req.Command,
		req.Packages,
		command,
	)
	return execPreparedCommand(ctx, cfg, command, "ExecNixShellBash")
}

func normalizeExecNixShellBashRequest(
	req ExecNixShellBashRequest,
) (ExecNixShellBashRequest, error) {
	req.Command = strings.TrimSpace(req.Command)
	if req.Command == "" {
		return ExecNixShellBashRequest{}, errors.New("command is required")
	}
	packages, err := normalizeExecNixShellBashPackages(req.Packages)
	if err != nil {
		return ExecNixShellBashRequest{}, err
	}
	req.Packages = packages
	return req, nil
}

func normalizeExecNixShellBashPackages(packages []string) ([]string, error) {
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

func buildNixShellBashCommand(command string, packages []string) string {
	parts := []string{
		"command -v nix >/dev/null 2>&1 || { echo 'nix not found in sandbox' >&2; exit 127; };",
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
	parts = append(
		parts,
		"--command",
		"/bin/bash",
		"-c",
		shellSingleQuote(command),
	)
	return strings.Join(parts, " ")
}
