package service

import (
	"context"
	"io"
)

// CommandRequest describes one session-backed command launch.
type CommandRequest struct {
	Command    string
	Packages   []string
	WorkingDir string
	Env        []string
}

// RunningCommand is one active executor-owned process handle.
type RunningCommand struct {
	Stdin     io.WriteCloser
	Stdout    io.ReadCloser
	Stderr    io.ReadCloser
	Wait      func() error
	Terminate func(force bool) error
}

// Executor starts session-backed commands.
type Executor interface {
	Type() string
	Start(ctx context.Context, req CommandRequest) (*RunningCommand, error)
}
