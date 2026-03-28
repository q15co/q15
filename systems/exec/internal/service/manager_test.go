package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/q15co/q15/libs/exec-contract/execpb"
)

func TestManagerSupportsConcurrentSessions(t *testing.T) {
	t.Parallel()

	manager := newTestManager(t)
	ctx := context.Background()

	first, err := manager.StartSession(ctx, "printf first", []string{"ignored"}, "", false)
	if err != nil {
		t.Fatalf("StartSession(first) error = %v", err)
	}
	second, err := manager.StartSession(ctx, "printf second", []string{"ignored"}, "", false)
	if err != nil {
		t.Fatalf("StartSession(second) error = %v", err)
	}
	if first.GetSessionId() == second.GetSessionId() {
		t.Fatalf("expected distinct session ids")
	}
}

func TestManagerWatchSessionReplaysFromCursor(t *testing.T) {
	t.Parallel()

	manager := newTestManager(t)
	session, err := manager.StartSession(
		context.Background(),
		"printf alpha; printf beta >&2",
		[]string{"ignored"},
		"",
		false,
	)
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	waitForTerminalState(t, manager, session.GetSessionId())

	var events []*execpb.SessionEvent
	if err := manager.WatchSession(
		context.Background(),
		session.GetSessionId(),
		1,
		func(event *execpb.SessionEvent) error {
			events = append(events, event)
			return nil
		},
	); err != nil {
		t.Fatalf("WatchSession() error = %v", err)
	}

	if len(events) < 2 {
		t.Fatalf("expected replayed events after cursor, got %d", len(events))
	}
	if _, ok := events[0].GetEvent().(*execpb.SessionEvent_Started); ok {
		t.Fatalf("watch after cursor should not replay started event")
	}
	var sawStdout bool
	var sawStderr bool
	for _, event := range events {
		if event.GetStdout() != nil {
			sawStdout = true
		}
		if event.GetStderr() != nil {
			sawStderr = true
		}
	}
	if !sawStdout || !sawStderr {
		t.Fatalf("expected stdout/stderr events in replay, got %#v", events)
	}
}

func TestManagerSupportsInteractiveStdin(t *testing.T) {
	t.Parallel()

	manager := newTestManager(t)
	session, err := manager.StartSession(
		context.Background(),
		"cat",
		[]string{"ignored"},
		"",
		true,
	)
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	if _, _, err := manager.WriteSessionStdin(session.GetSessionId(), []byte("hello\n"), true); err != nil {
		t.Fatalf("WriteSessionStdin() error = %v", err)
	}

	var stdout strings.Builder
	if err := manager.WatchSession(
		context.Background(),
		session.GetSessionId(),
		0,
		func(event *execpb.SessionEvent) error {
			if chunk := event.GetStdout(); chunk != nil {
				stdout.Write(chunk.GetData())
			}
			return nil
		},
	); err != nil {
		t.Fatalf("WatchSession() error = %v", err)
	}

	if got := stdout.String(); !strings.Contains(got, "hello\n") {
		t.Fatalf("stdout = %q, want echoed stdin", got)
	}
}

func TestManagerTerminateSessionRecordsTermination(t *testing.T) {
	t.Parallel()

	manager := newTestManager(t)
	session, err := manager.StartSession(
		context.Background(),
		"sleep 10",
		[]string{"ignored"},
		"",
		false,
	)
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	if _, err := manager.TerminateSession(session.GetSessionId(), false); err != nil {
		t.Fatalf("TerminateSession() error = %v", err)
	}
	waitForTerminalState(t, manager, session.GetSessionId())

	snapshot, err := manager.GetSession(session.GetSessionId())
	if err != nil {
		t.Fatalf("GetSession() error = %v", err)
	}
	if got := snapshot.GetState(); got != execpb.SessionState_SESSION_STATE_TERMINATED {
		t.Fatalf("state = %v, want terminated", got)
	}
	if got := snapshot.GetTerminationReason(); got != "terminate requested" {
		t.Fatalf("termination reason = %q, want graceful termination", got)
	}
}

func TestManagerReturnsNotFoundForUnknownSession(t *testing.T) {
	t.Parallel()

	manager := newTestManager(t)
	if _, err := manager.GetSession("missing"); !errors.Is(err, errSessionNotFound) {
		t.Fatalf("GetSession() error = %v, want errSessionNotFound", err)
	}
}

func TestManagerInjectsDefaultEnvIntoSessions(t *testing.T) {
	t.Parallel()

	manager, err := NewManager(ManagerConfig{
		DefaultWorkingDir: t.TempDir(),
		DefaultEnv: []string{
			"GH_TOKEN=__Q15_PROXY_ENV_test__",
			"HTTP_PROXY=http://proxy:8080",
		},
		Executor: ShellExecutor{},
	})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	session, err := manager.StartSession(
		context.Background(),
		`printf "%s|%s" "$GH_TOKEN" "$HTTP_PROXY"`,
		[]string{"ignored"},
		"",
		false,
	)
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	var stdout strings.Builder
	if err := manager.WatchSession(
		context.Background(),
		session.GetSessionId(),
		0,
		func(event *execpb.SessionEvent) error {
			if chunk := event.GetStdout(); chunk != nil {
				stdout.Write(chunk.GetData())
			}
			return nil
		},
	); err != nil {
		t.Fatalf("WatchSession() error = %v", err)
	}

	if got := stdout.String(); got != "__Q15_PROXY_ENV_test__|http://proxy:8080" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestManagerRunsShellSessionsViaBash(t *testing.T) {
	t.Parallel()

	manager := newTestManager(t)
	session, err := manager.StartSession(
		context.Background(),
		`parts=(alpha beta); if [[ "${parts[1]}" == "beta" ]]; then printf "%s" "${parts[0]}-${parts[1]}"; fi`,
		[]string{"ignored"},
		"",
		false,
	)
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	var stdout strings.Builder
	if err := manager.WatchSession(
		context.Background(),
		session.GetSessionId(),
		0,
		func(event *execpb.SessionEvent) error {
			if chunk := event.GetStdout(); chunk != nil {
				stdout.Write(chunk.GetData())
			}
			return nil
		},
	); err != nil {
		t.Fatalf("WatchSession() error = %v", err)
	}

	if got := stdout.String(); got != "alpha-beta" {
		t.Fatalf("stdout = %q, want %q", got, "alpha-beta")
	}
}

func newTestManager(t *testing.T) *Manager {
	t.Helper()

	manager, err := NewManager(ManagerConfig{
		DefaultWorkingDir: t.TempDir(),
		Executor:          ShellExecutor{},
	})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	return manager
}

func waitForTerminalState(t *testing.T, manager *Manager, sessionID string) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		session, err := manager.GetSession(sessionID)
		if err != nil {
			t.Fatalf("GetSession() error = %v", err)
		}
		switch session.GetState() {
		case execpb.SessionState_SESSION_STATE_EXITED,
			execpb.SessionState_SESSION_STATE_TERMINATED,
			execpb.SessionState_SESSION_STATE_FAILED:
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("session %q did not reach terminal state", sessionID)
}
