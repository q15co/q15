package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"q15.co/sandbox/internal/agent"
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

func (s *Shell) Definitions() []agent.ToolDefinition {
	return []agent.ToolDefinition{
		{
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
		},
	}
}

func (s *Shell) Run(ctx context.Context, call agent.ToolCall) (string, error) {
	if call.Name != "exec_shell" {
		return "", fmt.Errorf("unsupported tool: %s", call.Name)
	}

	var args struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
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
