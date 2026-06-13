// Package exec provides the exec tool and its helpers for filtering nix
// bootstrap stderr noise from tool results.
package exec

import (
	"context"
	"encoding/json"
	"fmt"
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
		Description: "Run a command through the configured execution service, returning output if it finishes within the wait window or a session handle if it keeps running",
		PromptGuidance: []string{
			"Use for commands, builds, tests, formatting, git, dev servers, browser sessions, daemons, and interactive CLI workflows.",
			"Every call must include a non-empty packages array of required nix installables.",
			"Set wait_seconds to how long the tool should wait; if the command is still running after that window, the tool returns Session-ID and Next-Event-Index for exec_read, exec_write, or exec_kill.",
			"Use wait_seconds 0 to start a long-running command and return immediately.",
			"Set keep_stdin_open true when you plan to send input later with exec_write.",
			"Non-zero process exit codes are returned as normal tool output with Exit-Code; inspect the payload rather than treating them as tool failures.",
			"If Output-Truncated is true and you need omitted content, re-read from an earlier event cursor with a larger max_output_chars.",
			"Long-running process output may be pipe-buffered; for Python use python -u or flush=True when you need incremental output.",
			"If you want the model to inspect a generated image afterward, write it under a shared root like /workspace and then call load_image on that path.",
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
				"wait_seconds": map[string]any{
					"type":    "integer",
					"minimum": 0,
					"maximum": maxExecWaitSeconds,
					"default": defaultExecWaitSeconds,
				},
				"keep_stdin_open": map[string]any{
					"type":    "boolean",
					"default": false,
				},
				"max_output_chars": map[string]any{
					"type":    "integer",
					"minimum": 0,
					"maximum": maxOutputCharsLimit,
					"default": defaultMaxOutputChars,
				},
			},
			"required": []string{"command", "packages"},
		},
	}
}

// Run executes one shell command from raw JSON tool arguments.
func (e *Exec) Run(ctx context.Context, arguments string) (string, error) {
	var args struct {
		Command        string   `json:"command"`
		Packages       []string `json:"packages"`
		WaitSeconds    *int     `json:"wait_seconds"`
		KeepStdinOpen  bool     `json:"keep_stdin_open"`
		MaxOutputChars *int     `json:"max_output_chars"`
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
	waitSeconds, err := normalizeWaitSeconds(args.WaitSeconds)
	if err != nil {
		return "", err
	}
	maxOutputChars, err := normalizeMaxOutputChars(args.MaxOutputChars)
	if err != nil {
		return "", err
	}
	if e.client == nil {
		return "", fmt.Errorf("no exec service client configured")
	}

	started, err := e.client.StartSession(ctx, &execpb.StartSessionRequest{
		Command:       args.Command,
		Packages:      packages,
		KeepStdinOpen: args.KeepStdinOpen,
	})
	if err != nil {
		return "", fmt.Errorf("start exec session: %w", err)
	}
	if started == nil || started.GetSession() == nil {
		return "", fmt.Errorf("start exec session: response missing session")
	}
	sessionID := strings.TrimSpace(started.GetSession().GetSessionId())
	if sessionID == "" {
		return "", fmt.Errorf("start exec session: response missing session_id")
	}
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

	collector := newSessionOutputCollector(maxOutputChars)
	watched, err := watchSessionInto(
		ctx,
		e.client,
		sessionID,
		0,
		time.Duration(waitSeconds)*time.Second,
		collector,
	)
	if err != nil {
		return "", err
	}

	finalSession, err := e.client.GetSession(ctx, &execpb.GetSessionRequest{SessionId: sessionID})
	if err != nil {
		return "", fmt.Errorf("get exec session %q: %w", sessionID, err)
	}
	session := finalSession.GetSession()
	if session == nil {
		return "", fmt.Errorf("get exec session %q: response missing session", sessionID)
	}

	if watched.TimedOut && isTerminalSession(session) {
		watched, err = watchSessionInto(
			ctx,
			e.client,
			sessionID,
			watched.NextEventIndex,
			finalDrainTimeout,
			collector,
		)
		if err != nil {
			return "", err
		}
	}

	timedOut := !isTerminalSession(session)
	stderr := watched.Stderr
	if session.GetHasExitCode() {
		stderr = filterNixBootstrapStderr(stderr, session.GetExitCode())
	}
	result := formatSessionToolResult(sessionToolResult{
		SessionID:          sessionID,
		Session:            session,
		IncludeTimedOut:    true,
		TimedOut:           timedOut,
		IncludeEventCursor: true,
		NextEventIndex:     watched.NextEventIndex,
		OutputTruncated:    watched.OutputTruncated,
		IncludeOutput:      true,
		Stdout:             watched.Stdout,
		Stderr:             stderr,
	})
	if timedOut {
		return result, nil
	}
	return result, nil
}

func normalizeWaitSeconds(value *int) (int, error) {
	if value == nil {
		return defaultExecWaitSeconds, nil
	}
	if *value < 0 {
		return 0, fmt.Errorf("wait_seconds must be >= 0")
	}
	if *value > maxExecWaitSeconds {
		return 0, fmt.Errorf("wait_seconds must be <= %d", maxExecWaitSeconds)
	}
	return *value, nil
}

func formatExecSessionResult(
	hasExitCode bool,
	exitCode int32,
	terminationReason string,
	stdout string,
	stderr string,
) string {
	// Filter known benign nix bootstrap/fetch chatter on successful runs.
	stderr = filterNixBootstrapStderr(stderr, exitCode)

	lines := make([]string, 0, 6)
	if hasExitCode {
		lines = append(lines, fmt.Sprintf("Exit-Code: %d", exitCode))
	}
	if strings.TrimSpace(terminationReason) != "" {
		lines = append(lines, "Termination-Reason: "+strings.TrimSpace(terminationReason))
	}
	lines = append(lines, "--- STDOUT ---")
	lines = append(lines, stdout)
	if strings.TrimSpace(stderr) != "" {
		lines = append(lines, "--- STDERR ---")
		lines = append(lines, stderr)
	}
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
