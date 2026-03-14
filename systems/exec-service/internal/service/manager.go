package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/q15co/q15/libs/exec-contract/execpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var errSessionNotFound = errors.New("session not found")

// ManagerConfig configures one in-memory session manager.
type ManagerConfig struct {
	DefaultWorkingDir string
	Executor          Executor
}

// Manager owns concurrent session state for the gRPC service.
type Manager struct {
	executor          Executor
	defaultWorkingDir string

	mu       sync.RWMutex
	sessions map[string]*managedSession
	nextID   atomic.Uint64
}

type managedSession struct {
	id         string
	command    string
	packages   []string
	workingDir string

	process *RunningCommand

	mu                sync.Mutex
	cond              *sync.Cond
	stdinMu           sync.Mutex
	streams           sync.WaitGroup
	stdinOpen         bool
	state             execpb.SessionState
	startedAt         time.Time
	finishedAt        time.Time
	exitCode          int32
	hasExitCode       bool
	terminationReason string
	terminateForce    bool
	events            []*execpb.SessionEvent
	nextEventIndex    int64
}

// NewManager constructs a session manager.
func NewManager(cfg ManagerConfig) (*Manager, error) {
	if cfg.Executor == nil {
		return nil, fmt.Errorf("executor is required")
	}
	cfg.DefaultWorkingDir = strings.TrimSpace(cfg.DefaultWorkingDir)
	if cfg.DefaultWorkingDir == "" {
		return nil, fmt.Errorf("default working dir is required")
	}
	return &Manager{
		executor:          cfg.Executor,
		defaultWorkingDir: cfg.DefaultWorkingDir,
		sessions:          make(map[string]*managedSession),
	}, nil
}

// StartSession launches a new tracked process.
func (m *Manager) StartSession(
	ctx context.Context,
	command string,
	packages []string,
	workingDir string,
	keepStdinOpen bool,
) (*execpb.Session, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil, fmt.Errorf("command is required")
	}
	packages = normalizePackages(packages)
	if len(packages) == 0 {
		return nil, fmt.Errorf("packages are required")
	}
	workingDir = strings.TrimSpace(workingDir)
	if workingDir == "" {
		workingDir = m.defaultWorkingDir
	}

	process, err := m.executor.Start(ctx, CommandRequest{
		Command:    command,
		Packages:   packages,
		WorkingDir: workingDir,
	})
	if err != nil {
		return nil, err
	}

	id := fmt.Sprintf("sess-%d", m.nextID.Add(1))
	now := time.Now().UTC()
	session := &managedSession{
		id:         id,
		command:    command,
		packages:   append([]string(nil), packages...),
		workingDir: workingDir,
		process:    process,
		stdinOpen:  keepStdinOpen && process.Stdin != nil,
		state:      execpb.SessionState_SESSION_STATE_RUNNING,
		startedAt:  now,
	}
	session.cond = sync.NewCond(&session.mu)
	session.appendEventLocked(newStartedEvent(id, 1, now))

	if !keepStdinOpen && process.Stdin != nil {
		if err := process.Stdin.Close(); err != nil {
			return nil, fmt.Errorf("close session stdin: %w", err)
		}
		session.stdinOpen = false
		session.appendEventLocked(newStdinClosedEvent(session.nextEventIndex, time.Now().UTC()))
	}

	m.mu.Lock()
	m.sessions[id] = session
	m.mu.Unlock()

	session.streams.Add(2)
	go m.captureStream(session, process.Stdout, true)
	go m.captureStream(session, process.Stderr, false)
	go m.waitForExit(session)

	return session.snapshot(), nil
}

// GetSession returns the current session snapshot.
func (m *Manager) GetSession(sessionID string) (*execpb.Session, error) {
	session, err := m.lookup(sessionID)
	if err != nil {
		return nil, err
	}
	return session.snapshot(), nil
}

// WatchSession returns all events after the provided cursor and blocks until completion.
func (m *Manager) WatchSession(
	ctx context.Context,
	sessionID string,
	afterEventIndex int64,
	onEvent func(*execpb.SessionEvent) error,
) error {
	if afterEventIndex < 0 {
		return fmt.Errorf("after_event_index must be >= 0")
	}
	session, err := m.lookup(sessionID)
	if err != nil {
		return err
	}

	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			session.mu.Lock()
			session.cond.Broadcast()
			session.mu.Unlock()
		case <-done:
		}
	}()
	defer close(done)

	next := afterEventIndex
	for {
		events, terminal := session.eventsAfter(next)
		for _, event := range events {
			if err := onEvent(event); err != nil {
				return err
			}
			next = event.GetEventIndex()
			if isTerminalEvent(event) {
				return nil
			}
		}
		if terminal {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		session.waitForUpdate(ctx, next)
	}
}

// WriteSessionStdin writes bytes or closes stdin for one running session.
func (m *Manager) WriteSessionStdin(
	sessionID string,
	data []byte,
	closeStdin bool,
) (*execpb.Session, int64, error) {
	session, err := m.lookup(sessionID)
	if err != nil {
		return nil, 0, err
	}

	var bytesWritten int64
	if len(data) == 0 && !closeStdin {
		return session.snapshot(), 0, nil
	}

	session.stdinMu.Lock()
	defer session.stdinMu.Unlock()

	session.mu.Lock()
	if !session.stdinOpen || session.process == nil || session.process.Stdin == nil {
		session.mu.Unlock()
		return nil, 0, fmt.Errorf("session stdin is closed")
	}
	stdin := session.process.Stdin
	session.mu.Unlock()

	if len(data) > 0 {
		n, err := stdin.Write(data)
		bytesWritten = int64(n)
		if err != nil {
			return nil, bytesWritten, fmt.Errorf("write stdin: %w", err)
		}
	}
	if closeStdin {
		if err := stdin.Close(); err != nil {
			return nil, bytesWritten, fmt.Errorf("close stdin: %w", err)
		}
		session.markStdinClosed()
	}

	return session.snapshot(), bytesWritten, nil
}

// TerminateSession stops one running process.
func (m *Manager) TerminateSession(sessionID string, force bool) (*execpb.Session, error) {
	session, err := m.lookup(sessionID)
	if err != nil {
		return nil, err
	}

	session.mu.Lock()
	switch session.state {
	case execpb.SessionState_SESSION_STATE_EXITED,
		execpb.SessionState_SESSION_STATE_TERMINATED,
		execpb.SessionState_SESSION_STATE_FAILED:
		snapshot := session.snapshotLocked()
		session.mu.Unlock()
		return snapshot, nil
	}
	session.state = execpb.SessionState_SESSION_STATE_TERMINATING
	session.terminationReason = terminationReason(force)
	session.terminateForce = force
	process := session.process
	session.mu.Unlock()

	if process != nil && process.Terminate != nil {
		if err := process.Terminate(force); err != nil {
			return nil, fmt.Errorf("terminate session: %w", err)
		}
	}
	return session.snapshot(), nil
}

func (m *Manager) captureStream(session *managedSession, r io.ReadCloser, stdout bool) {
	defer session.streams.Done()
	defer func() {
		if r != nil {
			_ = r.Close()
		}
	}()
	if r == nil {
		return
	}

	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			payload := append([]byte(nil), buf[:n]...)
			session.appendOutput(payload, stdout)
		}
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			session.appendOutput([]byte(fmt.Sprintf("stream read error: %v\n", err)), false)
			return
		}
	}
}

func (m *Manager) waitForExit(session *managedSession) {
	if session.process == nil || session.process.Wait == nil {
		return
	}
	err := session.process.Wait()
	session.streams.Wait()
	exitCode, hasExitCode := exitCodeFromWait(err)

	session.mu.Lock()
	defer session.mu.Unlock()

	session.finishedAt = time.Now().UTC()
	session.exitCode = exitCode
	session.hasExitCode = hasExitCode

	if session.state == execpb.SessionState_SESSION_STATE_TERMINATING {
		session.state = execpb.SessionState_SESSION_STATE_TERMINATED
		session.appendEventLocked(
			newTerminatedEvent(
				session.nextEventIndex,
				session.finishedAt,
				session.terminationReason,
				session.terminateForce,
			),
		)
		return
	}

	if err != nil && !hasExitCode {
		session.state = execpb.SessionState_SESSION_STATE_FAILED
		session.terminationReason = err.Error()
		session.appendEventLocked(
			newTerminatedEvent(session.nextEventIndex, session.finishedAt, err.Error(), false),
		)
		return
	}

	session.state = execpb.SessionState_SESSION_STATE_EXITED
	session.appendEventLocked(newExitedEvent(session.nextEventIndex, session.finishedAt, exitCode))
}

func (m *Manager) lookup(sessionID string) (*managedSession, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("session_id is required")
	}

	m.mu.RLock()
	session := m.sessions[sessionID]
	m.mu.RUnlock()
	if session == nil {
		return nil, errSessionNotFound
	}
	return session, nil
}

func (s *managedSession) waitForUpdate(ctx context.Context, afterEventIndex int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		if ctx.Err() != nil {
			return
		}
		if len(s.events) > 0 && s.events[len(s.events)-1].GetEventIndex() > afterEventIndex {
			return
		}
		if s.isTerminalLocked() {
			return
		}
		s.cond.Wait()
	}
}

func (s *managedSession) markStdinClosed() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.stdinOpen {
		return
	}
	s.stdinOpen = false
	s.appendEventLocked(newStdinClosedEvent(s.nextEventIndex, time.Now().UTC()))
}

func (s *managedSession) appendOutput(data []byte, stdout bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if stdout {
		s.appendEventLocked(newStdoutEvent(s.nextEventIndex, time.Now().UTC(), data))
		return
	}
	s.appendEventLocked(newStderrEvent(s.nextEventIndex, time.Now().UTC(), data))
}

func (s *managedSession) appendEventLocked(event *execpb.SessionEvent) {
	s.events = append(s.events, event)
	s.nextEventIndex = event.GetEventIndex() + 1
	s.cond.Broadcast()
}

func (s *managedSession) snapshot() *execpb.Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked()
}

func (s *managedSession) snapshotLocked() *execpb.Session {
	session := &execpb.Session{
		SessionId:         s.id,
		Command:           s.command,
		Packages:          append([]string(nil), s.packages...),
		WorkingDir:        s.workingDir,
		StdinOpen:         s.stdinOpen,
		State:             s.state,
		NextEventIndex:    s.nextEventIndex,
		HasExitCode:       s.hasExitCode,
		ExitCode:          s.exitCode,
		TerminationReason: s.terminationReason,
	}
	if !s.startedAt.IsZero() {
		session.StartedAt = timestamppb.New(s.startedAt)
	}
	if !s.finishedAt.IsZero() {
		session.FinishedAt = timestamppb.New(s.finishedAt)
	}
	return session
}

func (s *managedSession) eventsAfter(afterEventIndex int64) ([]*execpb.SessionEvent, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	events := make([]*execpb.SessionEvent, 0)
	for _, event := range s.events {
		if event.GetEventIndex() <= afterEventIndex {
			continue
		}
		events = append(events, cloneEvent(event))
	}
	return events, s.isTerminalLocked()
}

func (s *managedSession) isTerminalLocked() bool {
	switch s.state {
	case execpb.SessionState_SESSION_STATE_EXITED,
		execpb.SessionState_SESSION_STATE_TERMINATED,
		execpb.SessionState_SESSION_STATE_FAILED:
		return true
	default:
		return false
	}
}

func newStartedEvent(sessionID string, eventIndex int64, at time.Time) *execpb.SessionEvent {
	return &execpb.SessionEvent{
		EventIndex: eventIndex,
		OccurredAt: timestamppb.New(at),
		Event: &execpb.SessionEvent_Started{
			Started: &execpb.SessionStarted{SessionId: sessionID},
		},
	}
}

func newStdoutEvent(eventIndex int64, at time.Time, data []byte) *execpb.SessionEvent {
	return &execpb.SessionEvent{
		EventIndex: eventIndex,
		OccurredAt: timestamppb.New(at),
		Event: &execpb.SessionEvent_Stdout{
			Stdout: &execpb.SessionOutput{Data: append([]byte(nil), data...)},
		},
	}
}

func newStderrEvent(eventIndex int64, at time.Time, data []byte) *execpb.SessionEvent {
	return &execpb.SessionEvent{
		EventIndex: eventIndex,
		OccurredAt: timestamppb.New(at),
		Event: &execpb.SessionEvent_Stderr{
			Stderr: &execpb.SessionOutput{Data: append([]byte(nil), data...)},
		},
	}
}

func newStdinClosedEvent(eventIndex int64, at time.Time) *execpb.SessionEvent {
	return &execpb.SessionEvent{
		EventIndex: eventIndex,
		OccurredAt: timestamppb.New(at),
		Event: &execpb.SessionEvent_StdinClosed{
			StdinClosed: &execpb.SessionStdinClosed{},
		},
	}
}

func newExitedEvent(eventIndex int64, at time.Time, exitCode int32) *execpb.SessionEvent {
	return &execpb.SessionEvent{
		EventIndex: eventIndex,
		OccurredAt: timestamppb.New(at),
		Event: &execpb.SessionEvent_Exited{
			Exited: &execpb.SessionExited{ExitCode: exitCode},
		},
	}
}

func newTerminatedEvent(
	eventIndex int64,
	at time.Time,
	reason string,
	force bool,
) *execpb.SessionEvent {
	return &execpb.SessionEvent{
		EventIndex: eventIndex,
		OccurredAt: timestamppb.New(at),
		Event: &execpb.SessionEvent_Terminated{
			Terminated: &execpb.SessionTerminated{
				Reason: reason,
				Force:  force,
			},
		},
	}
}

func terminationReason(force bool) string {
	if force {
		return "force kill requested"
	}
	return "terminate requested"
}

func exitCodeFromWait(err error) (int32, bool) {
	if err == nil {
		return 0, true
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return int32(exitErr.ExitCode()), true
	}
	return 0, false
}

func cloneEvent(event *execpb.SessionEvent) *execpb.SessionEvent {
	if event == nil {
		return nil
	}
	clone := &execpb.SessionEvent{
		EventIndex: event.GetEventIndex(),
		OccurredAt: timestamppb.New(event.GetOccurredAt().AsTime()),
	}
	switch payload := event.GetEvent().(type) {
	case *execpb.SessionEvent_Started:
		clone.Event = &execpb.SessionEvent_Started{
			Started: &execpb.SessionStarted{SessionId: payload.Started.GetSessionId()},
		}
	case *execpb.SessionEvent_Stdout:
		clone.Event = &execpb.SessionEvent_Stdout{
			Stdout: &execpb.SessionOutput{Data: append([]byte(nil), payload.Stdout.GetData()...)},
		}
	case *execpb.SessionEvent_Stderr:
		clone.Event = &execpb.SessionEvent_Stderr{
			Stderr: &execpb.SessionOutput{Data: append([]byte(nil), payload.Stderr.GetData()...)},
		}
	case *execpb.SessionEvent_StdinClosed:
		clone.Event = &execpb.SessionEvent_StdinClosed{
			StdinClosed: &execpb.SessionStdinClosed{},
		}
	case *execpb.SessionEvent_Exited:
		clone.Event = &execpb.SessionEvent_Exited{
			Exited: &execpb.SessionExited{ExitCode: payload.Exited.GetExitCode()},
		}
	case *execpb.SessionEvent_Terminated:
		clone.Event = &execpb.SessionEvent_Terminated{
			Terminated: &execpb.SessionTerminated{
				Reason: payload.Terminated.GetReason(),
				Force:  payload.Terminated.GetForce(),
			},
		}
	}
	return clone
}

func isTerminalEvent(event *execpb.SessionEvent) bool {
	switch event.GetEvent().(type) {
	case *execpb.SessionEvent_Exited, *execpb.SessionEvent_Terminated:
		return true
	default:
		return false
	}
}
