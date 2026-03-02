package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/agent"
)

type CommandExecutor interface {
	Exec(ctx context.Context, command string) (string, error)
}

type Shell struct {
	exec CommandExecutor
}

func NewShell(exec CommandExecutor) *Shell {
	return &Shell{exec: exec}
}

func (s *Shell) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name:        "exec_shell",
		Description: "Execute a command in the sandbox using nix shell with explicitly requested packages",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]string{
					"type": "string",
				},
				"packages": map[string]any{
					"type": "array",
					"items": map[string]string{
						"type": "string",
					},
					"minItems": 1,
				},
			},
			"required": []string{"command", "packages"},
		},
	}
}

func (s *Shell) Run(ctx context.Context, arguments string) (string, error) {
	var args struct {
		Command  string   `json:"command"`
		Packages []string `json:"packages"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments JSON: %w", err)
	}
	args.Command = strings.TrimSpace(args.Command)
	if args.Command == "" {
		return "", fmt.Errorf("missing required argument: command")
	}
	packages, err := normalizePackages(args.Packages)
	if err != nil {
		return "", err
	}
	if s.exec == nil {
		return "", fmt.Errorf("no command executor configured")
	}

	command := buildNixShellExecCommand(args.Command, packages)
	fmt.Printf("CMD> %s\n", command)
	output, err := s.exec.Exec(ctx, command)
	if err != nil {
		return "", err
	}
	fmt.Printf("OUT> %s\n", output)
	return output, nil
}

func normalizePackages(packages []string) ([]string, error) {
	if len(packages) == 0 {
		return nil, fmt.Errorf("missing required argument: packages")
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

func buildNixShellExecCommand(command string, packages []string) string {
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

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
