package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"q15.co/sandbox/internal/agent"
)

type Shell struct{}

func NewShell() *Shell {
	return &Shell{}
}

func (s *Shell) Definitions() []agent.ToolDefinition {
	return []agent.ToolDefinition{
		{
			Name:        "exec_shell",
			Description: "Execute a command in the fish shell",
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

	fmt.Printf("CMD> %s\n", args.Command)
	output := runShellCommand(ctx, args.Command)
	fmt.Printf("OUT> %s\n", output)
	return output, nil
}

func runShellCommand(parent context.Context, command string) string {
	shellPath, shellArgs, err := findShell()
	if err != nil {
		return "shell error: " + err.Error()
	}

	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()

	args := append(shellArgs, command)
	cmd := exec.CommandContext(ctx, shellPath, args...)
	output, err := cmd.CombinedOutput()

	switch {
	case ctx.Err() == context.DeadlineExceeded:
		if len(output) == 0 {
			return "command timed out after 30s"
		}
		return "command timed out after 30s\n" + string(output)
	case err != nil:
		if len(output) == 0 {
			return "command failed: " + err.Error()
		}
		return "command failed: " + err.Error() + "\n" + string(output)
	case len(output) == 0:
		return "(no output)"
	default:
		return string(output)
	}
}

func findShell() (path string, args []string, err error) {
	if p, e := exec.LookPath("fish"); e == nil {
		return p, []string{"-c"}, nil
	}
	if p, e := exec.LookPath("bash"); e == nil {
		return p, []string{"-lc"}, nil
	}
	if p, e := exec.LookPath("sh"); e == nil {
		return p, []string{"-c"}, nil
	}
	return "", nil, fmt.Errorf("no shell found in PATH (tried fish, bash, sh)")
}
