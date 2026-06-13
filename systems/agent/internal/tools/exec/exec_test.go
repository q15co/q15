package exec

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/q15co/q15/libs/exec-contract/execpb"
	"github.com/q15co/q15/systems/agent/internal/execution"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type fakeExecClient struct {
	startResp       *execpb.StartSessionResponse
	getResp         *execpb.GetSessionResponse
	listResp        *execpb.ListSessionsResponse
	writeResp       *execpb.WriteSessionStdinResponse
	terminateResp   *execpb.TerminateSessionResponse
	watchEvents     []*execpb.WatchSessionResponse
	watchErr        error
	watchRecvErr    error
	startReq        *execpb.StartSessionRequest
	listReq         *execpb.ListSessionsRequest
	watchReq        *execpb.WatchSessionRequest
	writeReq        *execpb.WriteSessionStdinRequest
	terminateReq    *execpb.TerminateSessionRequest
	terminateCalled bool
}

func (f *fakeExecClient) Close() error { return nil }

func (f *fakeExecClient) GetRuntimeInfo(
	context.Context,
) (*execpb.GetRuntimeInfoResponse, error) {
	return &execpb.GetRuntimeInfoResponse{}, nil
}

func (f *fakeExecClient) StartSession(
	_ context.Context,
	req *execpb.StartSessionRequest,
) (*execpb.StartSessionResponse, error) {
	f.startReq = req
	if f.startResp != nil {
		return f.startResp, nil
	}
	return &execpb.StartSessionResponse{
		Session: &execpb.Session{SessionId: "sess-1"},
	}, nil
}

func (f *fakeExecClient) GetSession(
	context.Context,
	*execpb.GetSessionRequest,
) (*execpb.GetSessionResponse, error) {
	if f.getResp != nil {
		return f.getResp, nil
	}
	return &execpb.GetSessionResponse{
		Session: &execpb.Session{
			SessionId:   "sess-1",
			State:       execpb.SessionState_SESSION_STATE_EXITED,
			HasExitCode: true,
			ExitCode:    0,
		},
	}, nil
}

func (f *fakeExecClient) ListSessions(
	_ context.Context,
	req *execpb.ListSessionsRequest,
) (*execpb.ListSessionsResponse, error) {
	f.listReq = req
	if f.listResp != nil {
		return f.listResp, nil
	}
	return &execpb.ListSessionsResponse{}, nil
}

func (f *fakeExecClient) WatchSession(
	ctx context.Context,
	req *execpb.WatchSessionRequest,
) (execution.WatchStream, error) {
	f.watchReq = req
	if f.watchErr != nil {
		return nil, f.watchErr
	}
	return &fakeWatchStream{ctx: ctx, events: f.watchEvents, recvErr: f.watchRecvErr}, nil
}

func (f *fakeExecClient) WriteSessionStdin(
	_ context.Context,
	req *execpb.WriteSessionStdinRequest,
) (*execpb.WriteSessionStdinResponse, error) {
	f.writeReq = req
	if f.writeResp != nil {
		return f.writeResp, nil
	}
	return &execpb.WriteSessionStdinResponse{}, nil
}

func (f *fakeExecClient) TerminateSession(
	_ context.Context,
	req *execpb.TerminateSessionRequest,
) (*execpb.TerminateSessionResponse, error) {
	f.terminateReq = req
	f.terminateCalled = true
	if f.terminateResp != nil {
		return f.terminateResp, nil
	}
	return &execpb.TerminateSessionResponse{}, nil
}

type fakeWatchStream struct {
	ctx        context.Context
	events     []*execpb.WatchSessionResponse
	index      int
	blockUntil bool
	recvErr    error
}

func (f *fakeWatchStream) Recv() (*execpb.WatchSessionResponse, error) {
	if f.blockUntil {
		<-f.ctx.Done()
		return nil, f.ctx.Err()
	}
	if f.index >= len(f.events) {
		if f.recvErr != nil {
			return nil, f.recvErr
		}
		return nil, io.EOF
	}
	event := f.events[f.index]
	f.index++
	return event, nil
}

func TestExecRunFormatsStdoutStderrAndExitCode(t *testing.T) {
	t.Parallel()

	tool := NewExec(&fakeExecClient{
		watchEvents: []*execpb.WatchSessionResponse{
			{
				Event: &execpb.SessionEvent{
					Event: &execpb.SessionEvent_Stdout{
						Stdout: &execpb.SessionOutput{Data: []byte("alpha\n")},
					},
				},
			},
			{
				Event: &execpb.SessionEvent{
					Event: &execpb.SessionEvent_Stderr{
						Stderr: &execpb.SessionOutput{Data: []byte("beta\n")},
					},
				},
			},
			{
				Event: &execpb.SessionEvent{
					Event: &execpb.SessionEvent_Exited{
						Exited: &execpb.SessionExited{ExitCode: 0},
					},
				},
			},
		},
	})

	out, err := tool.Run(
		context.Background(),
		`{"command":"git status","packages":["nixpkgs#git"]}`,
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for _, want := range []string{
		"Exit-Code: 0",
		"--- STDOUT ---",
		"alpha",
		"--- STDERR ---",
		"beta",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestExecRunReturnsOutputOnNonZeroExit(t *testing.T) {
	t.Parallel()

	tool := NewExec(&fakeExecClient{
		watchEvents: []*execpb.WatchSessionResponse{
			{
				Event: &execpb.SessionEvent{
					Event: &execpb.SessionEvent_Exited{
						Exited: &execpb.SessionExited{ExitCode: 1},
					},
				},
			},
		},
		getResp: &execpb.GetSessionResponse{
			Session: &execpb.Session{
				SessionId:   "sess-1",
				State:       execpb.SessionState_SESSION_STATE_EXITED,
				HasExitCode: true,
				ExitCode:    1,
			},
		},
	})

	out, err := tool.Run(
		context.Background(),
		`{"command":"false","packages":["nixpkgs#bash"]}`,
	)
	if err != nil {
		t.Fatalf("Run() error = %v, want normal non-zero exit output", err)
	}
	for _, want := range []string{
		"Session-ID: sess-1",
		"Timed-Out: false",
		"Exit-Code: 1",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestExecRunTerminatesOnContextCancellation(t *testing.T) {
	t.Parallel()

	client := &fakeExecClient{
		watchEvents: nil,
	}
	stream := &fakeWatchStream{blockUntil: true}
	client.watchErr = nil

	tool := NewExec(&cancelAwareExecClient{
		delegate: client,
		stream:   stream,
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := tool.Run(ctx, `{"command":"sleep 10","packages":["nixpkgs#bash"]}`)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
	if !client.terminateCalled {
		t.Fatalf("expected TerminateSession to be called on cancellation")
	}
}

type cancelAwareExecClient struct {
	delegate *fakeExecClient
	stream   *fakeWatchStream
}

func (c *cancelAwareExecClient) Close() error { return nil }

func (c *cancelAwareExecClient) GetRuntimeInfo(
	ctx context.Context,
) (*execpb.GetRuntimeInfoResponse, error) {
	return c.delegate.GetRuntimeInfo(ctx)
}

func (c *cancelAwareExecClient) StartSession(
	ctx context.Context,
	req *execpb.StartSessionRequest,
) (*execpb.StartSessionResponse, error) {
	return c.delegate.StartSession(ctx, req)
}

func (c *cancelAwareExecClient) GetSession(
	ctx context.Context,
	req *execpb.GetSessionRequest,
) (*execpb.GetSessionResponse, error) {
	return c.delegate.GetSession(ctx, req)
}

func (c *cancelAwareExecClient) ListSessions(
	ctx context.Context,
	req *execpb.ListSessionsRequest,
) (*execpb.ListSessionsResponse, error) {
	return c.delegate.ListSessions(ctx, req)
}

func (c *cancelAwareExecClient) WatchSession(
	ctx context.Context,
	_ *execpb.WatchSessionRequest,
) (execution.WatchStream, error) {
	c.stream.ctx = ctx
	return c.stream, nil
}

func (c *cancelAwareExecClient) WriteSessionStdin(
	ctx context.Context,
	req *execpb.WriteSessionStdinRequest,
) (*execpb.WriteSessionStdinResponse, error) {
	return c.delegate.WriteSessionStdin(ctx, req)
}

func (c *cancelAwareExecClient) TerminateSession(
	ctx context.Context,
	req *execpb.TerminateSessionRequest,
) (*execpb.TerminateSessionResponse, error) {
	return c.delegate.TerminateSession(ctx, req)
}

func TestFormatExecSessionResultIncludesTerminationReason(t *testing.T) {
	t.Parallel()

	got := formatExecSessionResult(true, 137, "force kill requested", "alpha\n", "beta\n")
	for _, want := range []string{
		"Exit-Code: 137",
		"Termination-Reason: force kill requested",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatExecSessionResult() missing %q:\n%s", want, got)
		}
	}
}

func TestExecRunCancellationUsesBackgroundTerminate(t *testing.T) {
	t.Parallel()

	client := &fakeExecClient{}
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()

	tool := NewExec(&cancelAwareExecClient{
		delegate: client,
		stream: &fakeWatchStream{
			blockUntil: true,
		},
	})

	_, _ = tool.Run(ctx, `{"command":"sleep 10","packages":["nixpkgs#bash"]}`)
	if !client.terminateCalled {
		t.Fatalf("expected TerminateSession to be called")
	}
}

func TestExecRunReturnsSessionHandleAfterWaitTimeout(t *testing.T) {
	t.Parallel()

	session := &execpb.Session{
		SessionId: "sess-42",
		State:     execpb.SessionState_SESSION_STATE_RUNNING,
		StdinOpen: true,
	}
	client := &fakeExecClient{
		startResp: &execpb.StartSessionResponse{Session: session},
		getResp:   &execpb.GetSessionResponse{Session: session},
		watchEvents: []*execpb.WatchSessionResponse{
			eventResponse(startedEvent(1, "sess-42")),
			eventResponse(stdoutEvent(2, "ready\n")),
		},
		watchRecvErr: context.DeadlineExceeded,
	}

	out, err := NewExec(client).Run(
		context.Background(),
		`{"command":" sleep 60 ","packages":[" nixpkgs#bash "],"wait_seconds":1,"keep_stdin_open":true}`,
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := client.startReq.GetCommand(); got != "sleep 60" {
		t.Fatalf("StartSession command = %q, want sleep 60", got)
	}
	if got := client.startReq.GetPackages(); len(got) != 1 || got[0] != "nixpkgs#bash" {
		t.Fatalf("StartSession packages = %v, want [nixpkgs#bash]", got)
	}
	if !client.startReq.GetKeepStdinOpen() {
		t.Fatalf("StartSession keep_stdin_open = false, want true")
	}
	if got := client.watchReq.GetSessionId(); got != "sess-42" {
		t.Fatalf("WatchSession session_id = %q, want sess-42", got)
	}
	if got := client.watchReq.GetAfterEventIndex(); got != 0 {
		t.Fatalf("WatchSession after_event_index = %d, want 0", got)
	}
	for _, want := range []string{
		"Session-ID: sess-42",
		"Timed-Out: true",
		"State: running",
		"Stdin-Open: true",
		"Next-Event-Index: 2",
		"ready",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestExecRunWaitZeroReturnsImmediately(t *testing.T) {
	t.Parallel()

	session := &execpb.Session{
		SessionId: "sess-42",
		State:     execpb.SessionState_SESSION_STATE_RUNNING,
	}
	client := &fakeExecClient{
		startResp: &execpb.StartSessionResponse{Session: session},
		getResp:   &execpb.GetSessionResponse{Session: session},
	}

	out, err := NewExec(client).Run(
		context.Background(),
		`{"command":"sleep 60","packages":["nixpkgs#bash"],"wait_seconds":0}`,
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if client.watchReq != nil {
		t.Fatalf("WatchSession request = %#v, want no watch for wait_seconds 0", client.watchReq)
	}
	for _, want := range []string{
		"Session-ID: sess-42",
		"Timed-Out: true",
		"State: running",
		"Next-Event-Index: 0",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestExecListRunFormatsKnownSessions(t *testing.T) {
	t.Parallel()

	client := &fakeExecClient{
		listResp: &execpb.ListSessionsResponse{
			Sessions: []*execpb.Session{
				{
					SessionId:      "sess-1",
					Command:        "sleep 60",
					Packages:       []string{"nixpkgs#bash"},
					WorkingDir:     "/workspace",
					StdinOpen:      true,
					State:          execpb.SessionState_SESSION_STATE_RUNNING,
					NextEventIndex: 3,
					StartedAt:      timestamppb.New(time.Now().Add(-2 * time.Second)),
				},
				{
					SessionId:         "sess-2",
					Command:           "false",
					State:             execpb.SessionState_SESSION_STATE_EXITED,
					HasExitCode:       true,
					ExitCode:          1,
					TerminationReason: "non-zero exit",
				},
			},
		},
	}

	out, err := NewList(client).Run(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if client.listReq == nil {
		t.Fatalf("expected ListSessions request")
	}
	for _, want := range []string{
		"Sessions: 1 running, 1 exited (2 total, 2 shown)",
		"--- Session 1 ---",
		"Session-ID: sess-1",
		"State: running",
		"Command: sleep 60",
		"Working-Dir: /workspace",
		"Packages: nixpkgs#bash",
		"Stdin-Open: true",
		"Next-Event-Index: 3",
		"Session-Age-Seconds:",
		"--- Session 2 ---",
		"Session-ID: sess-2",
		"State: exited",
		"Exit-Code: 1",
		"Termination-Reason: non-zero exit",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestExecListRunFiltersByState(t *testing.T) {
	t.Parallel()

	client := &fakeExecClient{
		listResp: &execpb.ListSessionsResponse{
			Sessions: []*execpb.Session{
				{
					SessionId: "sess-running",
					Command:   "sleep 60",
					State:     execpb.SessionState_SESSION_STATE_RUNNING,
				},
				{
					SessionId:   "sess-exited",
					Command:     "true",
					State:       execpb.SessionState_SESSION_STATE_EXITED,
					HasExitCode: true,
					ExitCode:    0,
				},
				{
					SessionId:         "sess-killed",
					Command:           "sleep 600",
					State:             execpb.SessionState_SESSION_STATE_TERMINATED,
					TerminationReason: "terminate requested",
				},
			},
		},
	}

	out, err := NewList(client).Run(context.Background(), `{"state":"running"}`)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for _, want := range []string{
		"Sessions: 1 running, 1 exited, 1 terminated (3 total, 1 shown, filter: running)",
		"Session-ID: sess-running",
		"State: running",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{
		"Session-ID: sess-exited",
		"Session-ID: sess-killed",
	} {
		if strings.Contains(out, unwanted) {
			t.Fatalf("output contains %q despite running filter:\n%s", unwanted, out)
		}
	}
}

func TestExecListRunTruncatesLongCommandsByDefault(t *testing.T) {
	t.Parallel()

	command := "python -c " + strings.Repeat("x", defaultCommandChars+25)
	client := &fakeExecClient{
		listResp: &execpb.ListSessionsResponse{
			Sessions: []*execpb.Session{
				{
					SessionId: "sess-long",
					Command:   command,
					State:     execpb.SessionState_SESSION_STATE_RUNNING,
				},
			},
		},
	}

	out, err := NewList(client).Run(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(out, "Command: python -c ") {
		t.Fatalf("output missing command prefix:\n%s", out)
	}
	if !strings.Contains(out, "...") {
		t.Fatalf("output missing truncation suffix:\n%s", out)
	}
	if strings.Contains(out, strings.Repeat("x", defaultCommandChars+20)) {
		t.Fatalf("output contains untruncated command:\n%s", out)
	}
}

func TestExecListRunCanRaiseCommandLengthLimit(t *testing.T) {
	t.Parallel()

	command := "python -c " + strings.Repeat("x", defaultCommandChars+25)
	client := &fakeExecClient{
		listResp: &execpb.ListSessionsResponse{
			Sessions: []*execpb.Session{
				{
					SessionId: "sess-long",
					Command:   command,
					State:     execpb.SessionState_SESSION_STATE_RUNNING,
				},
			},
		},
	}

	out, err := NewList(client).Run(
		context.Background(),
		`{"max_command_chars":500}`,
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(out, "Command: "+command) {
		t.Fatalf("output missing full command:\n%s", out)
	}
}

func TestExecListRunAppliesLimit(t *testing.T) {
	t.Parallel()

	client := &fakeExecClient{
		listResp: &execpb.ListSessionsResponse{
			Sessions: []*execpb.Session{
				{SessionId: "sess-1", State: execpb.SessionState_SESSION_STATE_RUNNING},
				{SessionId: "sess-2", State: execpb.SessionState_SESSION_STATE_RUNNING},
				{SessionId: "sess-3", State: execpb.SessionState_SESSION_STATE_EXITED},
			},
		},
	}

	out, err := NewList(client).Run(context.Background(), `{"limit":2}`)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for _, want := range []string{
		"Sessions: 2 running, 1 exited (3 total, 2 shown, 1 omitted by limit)",
		"Session-ID: sess-1",
		"Session-ID: sess-2",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Session-ID: sess-3") {
		t.Fatalf("output contains limited session:\n%s", out)
	}
}

func TestExecListRunRejectsInvalidOptions(t *testing.T) {
	t.Parallel()

	tool := NewList(&fakeExecClient{})
	for _, args := range []string{
		`{"state":"alive"}`,
		`{"limit":-1}`,
		`{"max_command_chars":0}`,
		`{"unknown":true}`,
	} {
		_, err := tool.Run(context.Background(), args)
		if err == nil {
			t.Fatalf("Run(%s) error = nil, want validation error", args)
		}
	}
}

func TestExecListRunHandlesNoSessions(t *testing.T) {
	t.Parallel()

	out, err := NewList(&fakeExecClient{}).Run(context.Background(), "")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if out != "Sessions: 0 total, 0 shown" {
		t.Fatalf("output = %q, want Sessions: 0 total, 0 shown", out)
	}
}

func TestExecReadRunPollsFromCursorAndFormatsOutput(t *testing.T) {
	t.Parallel()

	client := &fakeExecClient{
		getResp: &execpb.GetSessionResponse{
			Session: &execpb.Session{
				SessionId: "sess-42",
				State:     execpb.SessionState_SESSION_STATE_RUNNING,
				StdinOpen: true,
				StartedAt: timestamppb.New(time.Now().Add(-10 * time.Second)),
			},
		},
		watchEvents: []*execpb.WatchSessionResponse{
			eventResponse(stdoutEvent(3, "delta\n")),
			eventResponse(stderrEvent(4, "warn\n")),
		},
	}

	out, err := NewRead(client).Run(
		context.Background(),
		`{"session_id":"sess-42","after_event_index":2}`,
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := client.watchReq.GetSessionId(); got != "sess-42" {
		t.Fatalf("WatchSession session_id = %q, want sess-42", got)
	}
	if got := client.watchReq.GetAfterEventIndex(); got != 2 {
		t.Fatalf("WatchSession after_event_index = %d, want 2", got)
	}
	for _, want := range []string{
		"Session-ID: sess-42",
		"Still-Running: true",
		"State: running",
		"Session-Age-Seconds:",
		"Next-Event-Index: 4",
		"delta",
		"--- STDERR ---",
		"warn",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestExecReadRunTreatsWatchDeadlineAsNoNewOutput(t *testing.T) {
	t.Parallel()

	client := &fakeExecClient{
		getResp: &execpb.GetSessionResponse{
			Session: &execpb.Session{
				SessionId: "sess-42",
				State:     execpb.SessionState_SESSION_STATE_RUNNING,
			},
		},
		watchRecvErr: context.DeadlineExceeded,
	}

	out, err := NewRead(client).Run(
		context.Background(),
		`{"session_id":"sess-42","after_event_index":5}`,
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if strings.Contains(out, "Timed-Out:") {
		t.Fatalf("exec_read output should not include Timed-Out:\n%s", out)
	}
	for _, want := range []string{
		"Session-ID: sess-42",
		"Still-Running: true",
		"State: running",
		"Next-Event-Index: 5",
		"--- STDOUT ---",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestExecReadRunCanFilterStreams(t *testing.T) {
	t.Parallel()

	client := &fakeExecClient{
		getResp: &execpb.GetSessionResponse{
			Session: &execpb.Session{
				SessionId: "sess-42",
				State:     execpb.SessionState_SESSION_STATE_RUNNING,
			},
		},
		watchEvents: []*execpb.WatchSessionResponse{
			eventResponse(stdoutEvent(3, "ignore me\n")),
			eventResponse(stderrEvent(4, "keep me\n")),
		},
	}

	out, err := NewRead(client).Run(
		context.Background(),
		`{"session_id":"sess-42","after_event_index":2,"streams":"stderr"}`,
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for _, want := range []string{
		"--- STDERR ---",
		"keep me",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{
		"--- STDOUT ---",
		"ignore me",
	} {
		if strings.Contains(out, unwanted) {
			t.Fatalf("output contains %q despite stderr filter:\n%s", unwanted, out)
		}
	}
}

func TestExecReadRunBoundsOutput(t *testing.T) {
	t.Parallel()

	client := &fakeExecClient{
		getResp: &execpb.GetSessionResponse{
			Session: &execpb.Session{
				SessionId: "sess-42",
				State:     execpb.SessionState_SESSION_STATE_RUNNING,
			},
		},
		watchEvents: []*execpb.WatchSessionResponse{
			eventResponse(stdoutEvent(3, "abcdef")),
		},
	}

	out, err := NewRead(client).Run(
		context.Background(),
		`{"session_id":"sess-42","after_event_index":2,"max_output_chars":3}`,
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for _, want := range []string{
		"Output-Truncated: true",
		"abc",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "def") {
		t.Fatalf("output should be truncated before def:\n%s", out)
	}
}

func TestExecWriteRunForwardsStdinAndReportsBytesWritten(t *testing.T) {
	t.Parallel()

	client := &fakeExecClient{
		writeResp: &execpb.WriteSessionStdinResponse{
			Session: &execpb.Session{
				SessionId: "sess-42",
				State:     execpb.SessionState_SESSION_STATE_RUNNING,
			},
			BytesWritten: 6,
		},
	}

	out, err := NewWrite(client).Run(
		context.Background(),
		`{"session_id":"sess-42","data":"hello","close_stdin":true}`,
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := client.writeReq.GetSessionId(); got != "sess-42" {
		t.Fatalf("WriteSessionStdin session_id = %q, want sess-42", got)
	}
	if got := string(client.writeReq.GetData()); got != "hello\n" {
		t.Fatalf("WriteSessionStdin data = %q, want appended newline", got)
	}
	if !client.writeReq.GetCloseStdin() {
		t.Fatalf("WriteSessionStdin close_stdin = false, want true")
	}
	for _, want := range []string{
		"Session-ID: sess-42",
		"Bytes-Written: 6",
		"State: running",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestExecWriteRunCanDisableAppendNewline(t *testing.T) {
	t.Parallel()

	client := &fakeExecClient{
		writeResp: &execpb.WriteSessionStdinResponse{
			Session: &execpb.Session{
				SessionId: "sess-42",
				State:     execpb.SessionState_SESSION_STATE_RUNNING,
			},
			BytesWritten: 5,
		},
	}

	_, err := NewWrite(client).Run(
		context.Background(),
		`{"session_id":"sess-42","data":"hello","append_newline":false}`,
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := string(client.writeReq.GetData()); got != "hello" {
		t.Fatalf("WriteSessionStdin data = %q, want raw data", got)
	}
}

func TestExecKillRunForwardsForceAndReportsState(t *testing.T) {
	t.Parallel()

	client := &fakeExecClient{
		terminateResp: &execpb.TerminateSessionResponse{
			Session: &execpb.Session{
				SessionId:         "sess-42",
				State:             execpb.SessionState_SESSION_STATE_TERMINATING,
				TerminationReason: "force kill requested",
			},
		},
	}

	out, err := NewKill(client).Run(
		context.Background(),
		`{"session_id":"sess-42","force":true}`,
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := client.terminateReq.GetSessionId(); got != "sess-42" {
		t.Fatalf("TerminateSession session_id = %q, want sess-42", got)
	}
	if !client.terminateReq.GetForce() {
		t.Fatalf("TerminateSession force = false, want true")
	}
	for _, want := range []string{
		"Session-ID: sess-42",
		"Force: true",
		"State: terminating",
		"Termination-Reason: force kill requested",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func eventResponse(event *execpb.SessionEvent) *execpb.WatchSessionResponse {
	return &execpb.WatchSessionResponse{Event: event}
}

func startedEvent(index int64, sessionID string) *execpb.SessionEvent {
	return &execpb.SessionEvent{
		EventIndex: index,
		Event: &execpb.SessionEvent_Started{
			Started: &execpb.SessionStarted{SessionId: sessionID},
		},
	}
}

func stdoutEvent(index int64, data string) *execpb.SessionEvent {
	return &execpb.SessionEvent{
		EventIndex: index,
		Event: &execpb.SessionEvent_Stdout{
			Stdout: &execpb.SessionOutput{Data: []byte(data)},
		},
	}
}

func stderrEvent(index int64, data string) *execpb.SessionEvent {
	return &execpb.SessionEvent{
		EventIndex: index,
		Event: &execpb.SessionEvent_Stderr{
			Stderr: &execpb.SessionOutput{Data: []byte(data)},
		},
	}
}
