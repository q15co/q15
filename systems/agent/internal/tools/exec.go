// Package tools provides model-callable runtime tools for the agent.
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/q15co/q15/libs/exec-contract/execpb"
	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/execution"
)

// Exec runs one command through the configured execution service.
type Exec struct {
	client execution.Service
}

// NewExec constructs an exec tool backed by the provided session service client.
func NewExec(client execution.Service) *Exec {
	return &Exec{client: client}
}

// Definition returns the tool schema exposed to the model.
func (e *Exec) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name:        "exec",
		Description: "Execute a command through the configured execution service using session-backed command execution",
		PromptGuidance: []string{
			"Use for commands, builds, tests, formatting, git, and other CLI workflows.",
			"Every call must include a non-empty packages array of required nix installables.",
			"The exec service starts a command session and waits for it to complete before returning stdout, stderr, and exit status.",
		},
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
func (e *Exec) Run(ctx context.Context, arguments string) (string, error) {
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
	if e.client == nil {
		return "", fmt.Errorf("no exec service client configured")
	}

	started, err := e.client.StartSession(ctx, &execpb.StartSessionRequest{
		Command:       args.Command,
		Packages:      packages,
		KeepStdinOpen: false,
	})
	if err != nil {
		return "", fmt.Errorf("start exec session: %w", err)
	}
	sessionID := started.GetSession().GetSessionId()
	defer func() {
		if ctx.Err() == nil || sessionID == "" {
			return
		}
		terminateCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = e.client.TerminateSession(terminateCtx, &execpb.TerminateSessionRequest{
			SessionId: sessionID,
			Force:     true,
		})
	}()

	stream, err := e.client.WatchSession(ctx, &execpb.WatchSessionRequest{
		SessionId: sessionID,
	})
	if err != nil {
		return "", fmt.Errorf("watch exec session %q: %w", sessionID, err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	var sawTerminal bool
	for {
		resp, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return "", fmt.Errorf("watch exec session %q: %w", sessionID, err)
		}
		event := resp.GetEvent()
		if event == nil {
			continue
		}
		if chunk := event.GetStdout(); chunk != nil {
			stdout.Write(chunk.GetData())
		}
		if chunk := event.GetStderr(); chunk != nil {
			stderr.Write(chunk.GetData())
		}
		if event.GetExited() != nil || event.GetTerminated() != nil {
			sawTerminal = true
		}
	}

	finalSession, err := e.client.GetSession(ctx, &execpb.GetSessionRequest{SessionId: sessionID})
	if err != nil {
		return "", fmt.Errorf("get exec session %q: %w", sessionID, err)
	}

	result := formatExecSessionResult(
		finalSession.GetSession().GetHasExitCode(),
		finalSession.GetSession().GetExitCode(),
		finalSession.GetSession().GetTerminationReason(),
		stdout.String(),
		stderr.String(),
	)
	if finalSession.GetSession().GetState() == execpb.SessionState_SESSION_STATE_TERMINATED &&
		sawTerminal {
		return "", fmt.Errorf("%s", result)
	}
	if finalSession.GetSession().GetHasExitCode() && finalSession.GetSession().GetExitCode() != 0 {
		return "", fmt.Errorf("%s", result)
	}
	return result, nil
}

func formatExecSessionResult(
	hasExitCode bool,
	exitCode int32,
	terminationReason string,
	stdout string,
	stderr string,
) string {
	lines := make([]string, 0, 6)
	if hasExitCode {
		lines = append(lines, fmt.Sprintf("Exit-Code: %d", exitCode))
	}
	if strings.TrimSpace(terminationReason) != "" {
		lines = append(lines, "Termination-Reason: "+strings.TrimSpace(terminationReason))
	}
	lines = append(lines, "--- STDOUT ---")
	lines = append(lines, stdout)
	lines = append(lines, "--- STDERR ---")
	lines = append(lines, stderr)
	return strings.Join(lines, "\n")
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
