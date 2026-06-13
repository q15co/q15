package exec

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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	defaultExecWaitSeconds  = 30
	maxExecWaitSeconds      = 300
	defaultMaxOutputChars   = 20000
	maxOutputCharsLimit     = 200000
	defaultListLimit        = 100
	maxListLimit            = 500
	defaultCommandChars     = 200
	maxCommandCharsLimit    = 5000
	readSessionWatchTimeout = time.Second
	finalDrainTimeout       = 100 * time.Millisecond
)

// List enumerates tracked exec sessions.
type List struct {
	client execution.Service
}

// NewList constructs an exec_list tool backed by the provided session client.
func NewList(client execution.Service) *List {
	return &List{client: client}
}

// Definition returns the tool schema exposed to the model.
func (l *List) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name:        "exec_list",
		Description: "List all tracked exec sessions known to the execution service",
		PromptGuidance: []string{
			"Use to discover running, orphaned, or recently finished exec sessions when the session ID is not in context.",
			"Use state running to focus on live sessions; leave state as all when you need the full session history.",
			"Commands are truncated by default for readability; raise max_command_chars only when the command text itself matters.",
			"Use returned Session-ID values with exec_read, exec_write, or exec_kill.",
		},
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"state": map[string]any{
					"type": "string",
					"enum": []string{
						"all",
						"active",
						"terminal",
						"starting",
						"running",
						"terminating",
						"exited",
						"terminated",
						"failed",
					},
					"default": "all",
				},
				"limit": map[string]any{
					"type":    "integer",
					"minimum": 0,
					"maximum": maxListLimit,
					"default": defaultListLimit,
				},
				"max_command_chars": map[string]any{
					"type":    "integer",
					"minimum": 1,
					"maximum": maxCommandCharsLimit,
					"default": defaultCommandChars,
				},
			},
			"additionalProperties": false,
		},
	}
}

// Run lists all sessions from raw JSON arguments.
func (l *List) Run(ctx context.Context, arguments string) (string, error) {
	arguments = strings.TrimSpace(arguments)
	if arguments == "" {
		arguments = "{}"
	}
	var args struct {
		State           string `json:"state"`
		Limit           *int   `json:"limit"`
		MaxCommandChars *int   `json:"max_command_chars"`
	}
	dec := json.NewDecoder(strings.NewReader(arguments))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&args); err != nil {
		return "", fmt.Errorf("invalid arguments JSON: %w", err)
	}
	stateFilter, err := normalizeListStateFilter(args.State)
	if err != nil {
		return "", err
	}
	limit, err := normalizeListLimit(args.Limit)
	if err != nil {
		return "", err
	}
	maxCommandChars, err := normalizeCommandChars(args.MaxCommandChars)
	if err != nil {
		return "", err
	}
	if l.client == nil {
		return "", fmt.Errorf("no exec service client configured")
	}

	resp, err := l.client.ListSessions(ctx, &execpb.ListSessionsRequest{})
	if err != nil {
		return "", fmt.Errorf("list exec sessions: %w", err)
	}
	return formatSessionListResult(resp.GetSessions(), sessionListOptions{
		StateFilter:     stateFilter,
		Limit:           limit,
		MaxCommandChars: maxCommandChars,
	}), nil
}

// Read polls output and state from an existing exec session.
type Read struct {
	client execution.Service
}

// NewRead constructs an exec_read tool backed by the provided session client.
func NewRead(client execution.Service) *Read {
	return &Read{client: client}
}

// Definition returns the tool schema exposed to the model.
func (r *Read) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name:        "exec_read",
		Description: "Read new stdout and stderr from a running exec session without blocking indefinitely",
		PromptGuidance: []string{
			"Use to poll output from running sessions returned by exec.",
			"Pass the previous Next-Event-Index value as after_event_index to avoid rereading old output.",
			"Call again when the session is still running and you need more output.",
			"Use streams to read stdout, stderr, or both when one stream is too noisy.",
			"If Output-Truncated is true and you need omitted content, re-read from an earlier event cursor with a larger max_output_chars.",
			"Long-running process output may be pipe-buffered; for Python use python -u or flush=True when you need incremental output.",
		},
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session_id": map[string]string{
					"type": "string",
				},
				"after_event_index": map[string]any{
					"type":    "integer",
					"minimum": 0,
					"default": 0,
				},
				"max_output_chars": map[string]any{
					"type":    "integer",
					"minimum": 0,
					"maximum": maxOutputCharsLimit,
					"default": defaultMaxOutputChars,
				},
				"streams": map[string]any{
					"type": "string",
					"enum": []string{
						"both",
						"stdout",
						"stderr",
					},
					"default": "both",
				},
			},
			"required": []string{"session_id"},
		},
	}
}

// Run reads available events from one session from raw JSON arguments.
func (r *Read) Run(ctx context.Context, arguments string) (string, error) {
	var args struct {
		SessionID       string `json:"session_id"`
		AfterEventIndex int64  `json:"after_event_index"`
		MaxOutputChars  *int   `json:"max_output_chars"`
		Streams         string `json:"streams"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments JSON: %w", err)
	}
	sessionID, err := normalizeSessionID(args.SessionID)
	if err != nil {
		return "", err
	}
	if args.AfterEventIndex < 0 {
		return "", fmt.Errorf("after_event_index must be >= 0")
	}
	maxOutputChars, err := normalizeMaxOutputChars(args.MaxOutputChars)
	if err != nil {
		return "", err
	}
	streams, err := normalizeStreams(args.Streams)
	if err != nil {
		return "", err
	}
	if r.client == nil {
		return "", fmt.Errorf("no exec service client configured")
	}

	collector := newSessionOutputCollectorFor(
		maxOutputChars,
		streams.includeStdout(),
		streams.includeStderr(),
	)
	watched, err := watchSessionInto(
		ctx,
		r.client,
		sessionID,
		args.AfterEventIndex,
		readSessionWatchTimeout,
		collector,
	)
	if err != nil {
		return "", err
	}
	snapshot, err := r.client.GetSession(ctx, &execpb.GetSessionRequest{SessionId: sessionID})
	if err != nil {
		return "", fmt.Errorf("get exec session %q: %w", sessionID, err)
	}

	return formatSessionToolResult(sessionToolResult{
		SessionID:           sessionID,
		Session:             snapshot.GetSession(),
		IncludeStillRunning: true,
		StillRunning:        !isTerminalSession(snapshot.GetSession()),
		IncludeEventCursor:  true,
		NextEventIndex:      watched.NextEventIndex,
		OutputTruncated:     watched.OutputTruncated,
		IncludeOutput:       true,
		IncludeStdout:       streams.includeStdout(),
		IncludeStderr:       streams.includeStderr(),
		Stdout:              watched.Stdout,
		Stderr:              watched.Stderr,
	}), nil
}

// Write sends stdin bytes into an existing exec session.
type Write struct {
	client execution.Service
}

// NewWrite constructs an exec_write tool backed by the provided session client.
func NewWrite(client execution.Service) *Write {
	return &Write{client: client}
}

// Definition returns the tool schema exposed to the model.
func (w *Write) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name:        "exec_write",
		Description: "Write stdin bytes into a running interactive exec session",
		PromptGuidance: []string{
			"Use only for sessions started with keep_stdin_open true.",
			"By default, a trailing newline is appended when data does not already end with one; set append_newline false for raw byte writes.",
			"Set close_stdin to true when no more input will be sent.",
		},
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session_id": map[string]string{
					"type": "string",
				},
				"data": map[string]string{
					"type": "string",
				},
				"close_stdin": map[string]any{
					"type":    "boolean",
					"default": false,
				},
				"append_newline": map[string]any{
					"type":    "boolean",
					"default": true,
				},
			},
			"required": []string{"session_id", "data"},
		},
	}
}

// Run writes stdin to one session from raw JSON arguments.
func (w *Write) Run(ctx context.Context, arguments string) (string, error) {
	var args struct {
		SessionID     string `json:"session_id"`
		Data          string `json:"data"`
		CloseStdin    bool   `json:"close_stdin"`
		AppendNewline *bool  `json:"append_newline"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments JSON: %w", err)
	}
	sessionID, err := normalizeSessionID(args.SessionID)
	if err != nil {
		return "", err
	}
	if args.Data == "" && !args.CloseStdin {
		return "", fmt.Errorf("missing required argument: data")
	}
	data := args.Data
	appendNewline := true
	if args.AppendNewline != nil {
		appendNewline = *args.AppendNewline
	}
	if appendNewline && data != "" && !strings.HasSuffix(data, "\n") {
		data += "\n"
	}
	if w.client == nil {
		return "", fmt.Errorf("no exec service client configured")
	}

	resp, err := w.client.WriteSessionStdin(ctx, &execpb.WriteSessionStdinRequest{
		SessionId:  sessionID,
		Data:       []byte(data),
		CloseStdin: args.CloseStdin,
	})
	if err != nil {
		return "", fmt.Errorf("write exec session %q stdin: %w", sessionID, err)
	}

	return formatSessionToolResult(sessionToolResult{
		SessionID:           sessionID,
		Session:             resp.GetSession(),
		IncludeBytesWritten: true,
		BytesWritten:        resp.GetBytesWritten(),
	}), nil
}

// Kill terminates an existing exec session.
type Kill struct {
	client execution.Service
}

// NewKill constructs an exec_kill tool backed by the provided session client.
func NewKill(client execution.Service) *Kill {
	return &Kill{client: client}
}

// Definition returns the tool schema exposed to the model.
func (k *Kill) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name:        "exec_kill",
		Description: "Terminate a running exec session",
		PromptGuidance: []string{
			"Use to stop background sessions returned by exec once they are no longer needed.",
			"Leave force false for graceful termination; set force true only when graceful termination is not enough.",
			"Use exec_read after killing if you need final stdout, stderr, or terminal events.",
		},
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session_id": map[string]string{
					"type": "string",
				},
				"force": map[string]any{
					"type":    "boolean",
					"default": false,
				},
			},
			"required": []string{"session_id"},
		},
	}
}

// Run terminates one session from raw JSON arguments.
func (k *Kill) Run(ctx context.Context, arguments string) (string, error) {
	var args struct {
		SessionID string `json:"session_id"`
		Force     bool   `json:"force"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments JSON: %w", err)
	}
	sessionID, err := normalizeSessionID(args.SessionID)
	if err != nil {
		return "", err
	}
	if k.client == nil {
		return "", fmt.Errorf("no exec service client configured")
	}

	resp, err := k.client.TerminateSession(ctx, &execpb.TerminateSessionRequest{
		SessionId: sessionID,
		Force:     args.Force,
	})
	if err != nil {
		return "", fmt.Errorf("terminate exec session %q: %w", sessionID, err)
	}

	return formatSessionToolResult(sessionToolResult{
		SessionID:    sessionID,
		Session:      resp.GetSession(),
		IncludeForce: true,
		Force:        args.Force,
	}), nil
}

type watchSessionResult struct {
	Stdout          string
	Stderr          string
	NextEventIndex  int64
	TimedOut        bool
	OutputTruncated bool
}

func watchSessionInto(
	ctx context.Context,
	client execution.Service,
	sessionID string,
	afterEventIndex int64,
	timeout time.Duration,
	collector *sessionOutputCollector,
) (watchSessionResult, error) {
	if timeout <= 0 {
		return collector.result(afterEventIndex, true), nil
	}

	result := watchSessionResult{NextEventIndex: afterEventIndex}
	watchCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	stream, err := client.WatchSession(watchCtx, &execpb.WatchSessionRequest{
		SessionId:       sessionID,
		AfterEventIndex: afterEventIndex,
	})
	if err != nil {
		if isExpectedWatchTimeout(ctx, watchCtx, err) {
			return collector.result(result.NextEventIndex, true), nil
		}
		return result, fmt.Errorf("watch exec session %q: %w", sessionID, err)
	}

	for {
		resp, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return collector.result(result.NextEventIndex, false), nil
			}
			if isExpectedWatchTimeout(ctx, watchCtx, err) {
				return collector.result(result.NextEventIndex, true), nil
			}
			return result, fmt.Errorf("watch exec session %q: %w", sessionID, err)
		}
		event := resp.GetEvent()
		if event == nil {
			continue
		}
		if event.GetEventIndex() > result.NextEventIndex {
			result.NextEventIndex = event.GetEventIndex()
		}
		if chunk := event.GetStdout(); chunk != nil {
			collector.write(chunk.GetData(), true)
		}
		if chunk := event.GetStderr(); chunk != nil {
			collector.write(chunk.GetData(), false)
		}
		if isSessionTerminalEvent(event) {
			return collector.result(result.NextEventIndex, false), nil
		}
	}
}

type sessionOutputCollector struct {
	stdout        bytes.Buffer
	stderr        bytes.Buffer
	remaining     int
	includeStdout bool
	includeStderr bool
	truncated     bool
}

func newSessionOutputCollector(maxOutputChars int) *sessionOutputCollector {
	return newSessionOutputCollectorFor(maxOutputChars, true, true)
}

func newSessionOutputCollectorFor(
	maxOutputChars int,
	includeStdout bool,
	includeStderr bool,
) *sessionOutputCollector {
	return &sessionOutputCollector{
		remaining:     maxOutputChars,
		includeStdout: includeStdout,
		includeStderr: includeStderr,
	}
}

func (c *sessionOutputCollector) write(data []byte, stdout bool) {
	if c == nil || len(data) == 0 {
		return
	}
	if stdout && !c.includeStdout {
		return
	}
	if !stdout && !c.includeStderr {
		return
	}
	if c.remaining <= 0 {
		c.truncated = true
		return
	}
	if len(data) > c.remaining {
		data = data[:c.remaining]
		c.truncated = true
	}
	c.remaining -= len(data)
	if stdout {
		c.stdout.Write(data)
		return
	}
	c.stderr.Write(data)
}

func (c *sessionOutputCollector) result(
	nextEventIndex int64,
	timedOut bool,
) watchSessionResult {
	if c == nil {
		return watchSessionResult{
			NextEventIndex: nextEventIndex,
			TimedOut:       timedOut,
		}
	}
	return watchSessionResult{
		Stdout:          c.stdout.String(),
		Stderr:          c.stderr.String(),
		NextEventIndex:  nextEventIndex,
		TimedOut:        timedOut,
		OutputTruncated: c.truncated,
	}
}

func isExpectedWatchTimeout(
	parentCtx context.Context,
	watchCtx context.Context,
	err error,
) bool {
	if parentCtx.Err() != nil {
		return false
	}
	return errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(watchCtx.Err(), context.DeadlineExceeded) ||
		status.Code(err) == codes.DeadlineExceeded
}

func isSessionTerminalEvent(event *execpb.SessionEvent) bool {
	return event.GetExited() != nil || event.GetTerminated() != nil
}

func isTerminalSession(session *execpb.Session) bool {
	if session == nil {
		return false
	}
	switch session.GetState() {
	case execpb.SessionState_SESSION_STATE_EXITED,
		execpb.SessionState_SESSION_STATE_TERMINATED,
		execpb.SessionState_SESSION_STATE_FAILED:
		return true
	default:
		return false
	}
}

func normalizeSessionID(sessionID string) (string, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", fmt.Errorf("missing required argument: session_id")
	}
	return sessionID, nil
}

func normalizeMaxOutputChars(value *int) (int, error) {
	if value == nil {
		return defaultMaxOutputChars, nil
	}
	if *value < 0 {
		return 0, fmt.Errorf("max_output_chars must be >= 0")
	}
	if *value > maxOutputCharsLimit {
		return 0, fmt.Errorf("max_output_chars must be <= %d", maxOutputCharsLimit)
	}
	return *value, nil
}

type streamSelection string

const (
	streamSelectionBoth   streamSelection = "both"
	streamSelectionStdout streamSelection = "stdout"
	streamSelectionStderr streamSelection = "stderr"
)

func normalizeStreams(value string) (streamSelection, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return streamSelectionBoth, nil
	}
	switch streamSelection(value) {
	case streamSelectionBoth, streamSelectionStdout, streamSelectionStderr:
		return streamSelection(value), nil
	default:
		return "", fmt.Errorf("streams must be one of: both, stdout, stderr")
	}
}

func (s streamSelection) includeStdout() bool {
	return s == streamSelectionBoth || s == streamSelectionStdout
}

func (s streamSelection) includeStderr() bool {
	return s == streamSelectionBoth || s == streamSelectionStderr
}

type sessionToolResult struct {
	SessionID string
	Session   *execpb.Session

	IncludeTimedOut bool
	TimedOut        bool

	IncludeStillRunning bool
	StillRunning        bool

	IncludeEventCursor bool
	NextEventIndex     int64

	OutputTruncated bool

	IncludeBytesWritten bool
	BytesWritten        int64

	IncludeForce bool
	Force        bool

	IncludeOutput bool
	IncludeStdout bool
	IncludeStderr bool
	Stdout        string
	Stderr        string
}

type sessionListOptions struct {
	StateFilter     listStateFilter
	Limit           int
	MaxCommandChars int
}

type listStateFilter string

const (
	listStateFilterAll         listStateFilter = "all"
	listStateFilterActive      listStateFilter = "active"
	listStateFilterTerminal    listStateFilter = "terminal"
	listStateFilterStarting    listStateFilter = "starting"
	listStateFilterRunning     listStateFilter = "running"
	listStateFilterTerminating listStateFilter = "terminating"
	listStateFilterExited      listStateFilter = "exited"
	listStateFilterTerminated  listStateFilter = "terminated"
	listStateFilterFailed      listStateFilter = "failed"
)

func formatSessionListResult(sessions []*execpb.Session, opts sessionListOptions) string {
	nonNilSessions := make([]*execpb.Session, 0, len(sessions))
	for _, session := range sessions {
		if session != nil {
			nonNilSessions = append(nonNilSessions, session)
		}
	}

	filtered := filterSessions(nonNilSessions, opts.StateFilter)
	shown := limitedSessions(filtered, opts.Limit)
	omitted := len(filtered) - len(shown)

	lines := []string{formatSessionListSummary(
		nonNilSessions,
		len(shown),
		opts.StateFilter,
		omitted,
	)}
	for i, session := range shown {
		lines = append(lines, fmt.Sprintf("--- Session %d ---", i+1))
		lines = append(lines, "Session-ID: "+session.GetSessionId())
		lines = append(lines, "State: "+formatSessionState(session.GetState()))
		if command := strings.TrimSpace(session.GetCommand()); command != "" {
			lines = append(
				lines,
				"Command: "+truncateCommand(singleLine(command), opts.MaxCommandChars),
			)
		}
		if workingDir := strings.TrimSpace(session.GetWorkingDir()); workingDir != "" {
			lines = append(lines, "Working-Dir: "+workingDir)
		}
		if packages := session.GetPackages(); len(packages) > 0 {
			lines = append(lines, "Packages: "+strings.Join(packages, ", "))
		}
		lines = append(lines, fmt.Sprintf("Stdin-Open: %t", session.GetStdinOpen()))
		lines = append(lines, fmt.Sprintf("Next-Event-Index: %d", session.GetNextEventIndex()))
		if age, ok := sessionAgeSeconds(session); ok {
			lines = append(lines, fmt.Sprintf("Session-Age-Seconds: %d", age))
		}
		if session.GetHasExitCode() {
			lines = append(lines, fmt.Sprintf("Exit-Code: %d", session.GetExitCode()))
		}
		if reason := strings.TrimSpace(session.GetTerminationReason()); reason != "" {
			lines = append(lines, "Termination-Reason: "+singleLine(reason))
		}
	}
	return strings.Join(lines, "\n")
}

func normalizeListStateFilter(value string) (listStateFilter, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return listStateFilterAll, nil
	}
	filter := listStateFilter(value)
	switch filter {
	case listStateFilterAll,
		listStateFilterActive,
		listStateFilterTerminal,
		listStateFilterStarting,
		listStateFilterRunning,
		listStateFilterTerminating,
		listStateFilterExited,
		listStateFilterTerminated,
		listStateFilterFailed:
		return filter, nil
	default:
		return "", fmt.Errorf(
			"state must be one of: all, active, terminal, starting, running, terminating, exited, terminated, failed",
		)
	}
}

func normalizeListLimit(value *int) (int, error) {
	if value == nil {
		return defaultListLimit, nil
	}
	if *value < 0 {
		return 0, fmt.Errorf("limit must be >= 0")
	}
	if *value > maxListLimit {
		return 0, fmt.Errorf("limit must be <= %d", maxListLimit)
	}
	return *value, nil
}

func normalizeCommandChars(value *int) (int, error) {
	if value == nil {
		return defaultCommandChars, nil
	}
	if *value < 1 {
		return 0, fmt.Errorf("max_command_chars must be >= 1")
	}
	if *value > maxCommandCharsLimit {
		return 0, fmt.Errorf("max_command_chars must be <= %d", maxCommandCharsLimit)
	}
	return *value, nil
}

func filterSessions(sessions []*execpb.Session, filter listStateFilter) []*execpb.Session {
	if filter == "" {
		filter = listStateFilterAll
	}
	out := make([]*execpb.Session, 0, len(sessions))
	for _, session := range sessions {
		if sessionMatchesFilter(session, filter) {
			out = append(out, session)
		}
	}
	return out
}

func limitedSessions(sessions []*execpb.Session, limit int) []*execpb.Session {
	if limit < 0 || limit >= len(sessions) {
		return sessions
	}
	return sessions[:limit]
}

func sessionMatchesFilter(session *execpb.Session, filter listStateFilter) bool {
	if session == nil {
		return false
	}
	state := session.GetState()
	switch filter {
	case listStateFilterAll:
		return true
	case listStateFilterActive:
		return !isTerminalSession(session)
	case listStateFilterTerminal:
		return isTerminalSession(session)
	case listStateFilterStarting:
		return state == execpb.SessionState_SESSION_STATE_STARTING
	case listStateFilterRunning:
		return state == execpb.SessionState_SESSION_STATE_RUNNING
	case listStateFilterTerminating:
		return state == execpb.SessionState_SESSION_STATE_TERMINATING
	case listStateFilterExited:
		return state == execpb.SessionState_SESSION_STATE_EXITED
	case listStateFilterTerminated:
		return state == execpb.SessionState_SESSION_STATE_TERMINATED
	case listStateFilterFailed:
		return state == execpb.SessionState_SESSION_STATE_FAILED
	default:
		return true
	}
}

func formatSessionListSummary(
	sessions []*execpb.Session,
	shown int,
	filter listStateFilter,
	omitted int,
) string {
	total := len(sessions)
	if filter == "" {
		filter = listStateFilterAll
	}
	if total == 0 {
		if filter == listStateFilterAll {
			return "Sessions: 0 total, 0 shown"
		}
		return fmt.Sprintf("Sessions: 0 total, 0 shown (filter: %s)", filter)
	}
	stateParts := sessionStateSummaryParts(sessions)

	suffix := fmt.Sprintf("(%d total, %d shown", total, shown)
	if filter != listStateFilterAll {
		suffix += fmt.Sprintf(", filter: %s", filter)
	}
	if omitted > 0 {
		suffix += fmt.Sprintf(", %d omitted by limit", omitted)
	}
	suffix += ")"
	return "Sessions: " + strings.Join(stateParts, ", ") + " " + suffix
}

func sessionStateSummaryParts(sessions []*execpb.Session) []string {
	counts := map[execpb.SessionState]int{}
	for _, session := range sessions {
		if session == nil {
			continue
		}
		counts[session.GetState()]++
	}

	orderedStates := []execpb.SessionState{
		execpb.SessionState_SESSION_STATE_STARTING,
		execpb.SessionState_SESSION_STATE_RUNNING,
		execpb.SessionState_SESSION_STATE_TERMINATING,
		execpb.SessionState_SESSION_STATE_EXITED,
		execpb.SessionState_SESSION_STATE_TERMINATED,
		execpb.SessionState_SESSION_STATE_FAILED,
		execpb.SessionState_SESSION_STATE_UNSPECIFIED,
	}
	parts := make([]string, 0, len(orderedStates))
	for _, state := range orderedStates {
		if count := counts[state]; count > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", count, formatSessionState(state)))
		}
	}
	return parts
}

func truncateCommand(command string, maxChars int) string {
	if maxChars <= 0 || len(command) <= maxChars {
		return command
	}
	if maxChars <= 3 {
		return command[:maxChars]
	}
	return command[:maxChars-3] + "..."
}

func formatSessionToolResult(result sessionToolResult) string {
	lines := make([]string, 0, 12)
	lines = append(lines, "Session-ID: "+result.SessionID)
	if result.IncludeTimedOut {
		lines = append(lines, fmt.Sprintf("Timed-Out: %t", result.TimedOut))
	}
	if result.IncludeStillRunning {
		lines = append(lines, fmt.Sprintf("Still-Running: %t", result.StillRunning))
	}
	if result.IncludeBytesWritten {
		lines = append(lines, fmt.Sprintf("Bytes-Written: %d", result.BytesWritten))
	}
	if result.IncludeForce {
		lines = append(lines, fmt.Sprintf("Force: %t", result.Force))
	}
	if session := result.Session; session != nil {
		lines = append(lines, "State: "+formatSessionState(session.GetState()))
		lines = append(lines, fmt.Sprintf("Stdin-Open: %t", session.GetStdinOpen()))
		if age, ok := sessionAgeSeconds(session); ok {
			lines = append(lines, fmt.Sprintf("Session-Age-Seconds: %d", age))
		}
		if session.GetHasExitCode() {
			lines = append(lines, fmt.Sprintf("Exit-Code: %d", session.GetExitCode()))
		}
		if reason := strings.TrimSpace(session.GetTerminationReason()); reason != "" {
			lines = append(lines, "Termination-Reason: "+reason)
		}
	}
	if result.IncludeEventCursor {
		lines = append(lines, fmt.Sprintf("Next-Event-Index: %d", result.NextEventIndex))
	}
	if result.IncludeOutput {
		includeStdout := result.IncludeStdout
		includeStderr := result.IncludeStderr
		if !includeStdout && !includeStderr {
			includeStdout = true
			includeStderr = true
		}
		lines = append(lines, fmt.Sprintf("Output-Truncated: %t", result.OutputTruncated))
		if includeStdout {
			lines = append(lines, "--- STDOUT ---")
			lines = append(lines, result.Stdout)
		}
		if includeStderr && strings.TrimSpace(result.Stderr) != "" {
			lines = append(lines, "--- STDERR ---")
			lines = append(lines, result.Stderr)
		}
	}
	return strings.Join(lines, "\n")
}

func formatSessionState(state execpb.SessionState) string {
	value := strings.ToLower(state.String())
	return strings.TrimPrefix(value, "session_state_")
}

func sessionAgeSeconds(session *execpb.Session) (int64, bool) {
	if session == nil || session.GetStartedAt() == nil {
		return 0, false
	}
	startedAt := session.GetStartedAt().AsTime()
	if startedAt.IsZero() {
		return 0, false
	}
	age := time.Since(startedAt)
	if age < 0 {
		return 0, true
	}
	return int64(age.Seconds()), true
}

func singleLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}
