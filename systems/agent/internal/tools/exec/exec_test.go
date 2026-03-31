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
)

type fakeExecClient struct {
	startResp       *execpb.StartSessionResponse
	getResp         *execpb.GetSessionResponse
	watchEvents     []*execpb.WatchSessionResponse
	watchErr        error
	terminateCalled bool
}

func (f *fakeExecClient) Close() error { return nil }

func (f *fakeExecClient) GetRuntimeInfo(
	context.Context,
) (*execpb.GetRuntimeInfoResponse, error) {
	return &execpb.GetRuntimeInfoResponse{}, nil
}

func (f *fakeExecClient) StartSession(
	context.Context,
	*execpb.StartSessionRequest,
) (*execpb.StartSessionResponse, error) {
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

func (f *fakeExecClient) WatchSession(
	ctx context.Context,
	_ *execpb.WatchSessionRequest,
) (execution.WatchStream, error) {
	if f.watchErr != nil {
		return nil, f.watchErr
	}
	return &fakeWatchStream{ctx: ctx, events: f.watchEvents}, nil
}

func (f *fakeExecClient) WriteSessionStdin(
	_ context.Context,
	_ *execpb.WriteSessionStdinRequest,
) (*execpb.WriteSessionStdinResponse, error) {
	return &execpb.WriteSessionStdinResponse{}, nil
}

func (f *fakeExecClient) TerminateSession(
	_ context.Context,
	_ *execpb.TerminateSessionRequest,
) (*execpb.TerminateSessionResponse, error) {
	f.terminateCalled = true
	return &execpb.TerminateSessionResponse{}, nil
}

type fakeWatchStream struct {
	ctx        context.Context
	events     []*execpb.WatchSessionResponse
	index      int
	blockUntil bool
}

func (f *fakeWatchStream) Recv() (*execpb.WatchSessionResponse, error) {
	if f.blockUntil {
		<-f.ctx.Done()
		return nil, f.ctx.Err()
	}
	if f.index >= len(f.events) {
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

func TestExecRunReturnsErrorOnNonZeroExit(t *testing.T) {
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

	_, err := tool.Run(
		context.Background(),
		`{"command":"false","packages":["nixpkgs#bash"]}`,
	)
	if err == nil || !strings.Contains(err.Error(), "Exit-Code: 1") {
		t.Fatalf("Run() error = %v, want formatted non-zero exit", err)
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
