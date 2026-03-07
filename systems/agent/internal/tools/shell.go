// Package tools provides model-callable runtime tools for the agent.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/agent"
)

// NixShellBashExecutor runs a command inside the sandbox via nix shell and
// bash with explicit packages.
type NixShellBashExecutor interface {
	ExecNixShellBash(ctx context.Context, command string, packages []string) (string, error)
}

// NixShellBash executes commands in the sandbox via nix shell and bash with
// explicitly requested packages.
type NixShellBash struct {
	exec NixShellBashExecutor
}

// NewNixShellBash constructs an exec_nix_shell_bash tool backed by the
// provided executor.
func NewNixShellBash(exec NixShellBashExecutor) *NixShellBash {
	return &NixShellBash{exec: exec}
}

// Definition returns the tool schema exposed to the model.
func (s *NixShellBash) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name:        "exec_nix_shell_bash",
		Description: "Execute a command in the sandbox via nix shell and /bin/bash -c with explicitly requested packages",
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

// Run executes one shell command from raw JSON tool arguments.
func (s *NixShellBash) Run(ctx context.Context, arguments string) (string, error) {
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

	return s.exec.ExecNixShellBash(ctx, args.Command, packages)
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
