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
		Description: "Execute a command in the agent sandbox shell",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]string{
					"type": "string",
				},
			},
			"required": []string{"command"},
		},
	}
}

func (s *Shell) Run(ctx context.Context, arguments string) (string, error) {
	var args struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments JSON: %w", err)
	}
	args.Command = strings.TrimSpace(args.Command)
	if args.Command == "" {
		return "", fmt.Errorf("missing required argument: command")
	}
	if s.exec == nil {
		return "", fmt.Errorf("no command executor configured")
	}

	fmt.Printf("CMD> %s\n", args.Command)
	output, err := s.exec.Exec(ctx, args.Command)
	if err != nil {
		return "", err
	}
	fmt.Printf("OUT> %s\n", output)
	return output, nil
}
