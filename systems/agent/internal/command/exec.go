package command

import (
	"context"
	"errors"
	"io"
	"os/exec"
)

// ExecFunc runs one program with the streams a command hands it.
//
// A command that shells out is given this rather than calling os/exec itself, so
// a test supplies its own and no command needs podman, systemd or journalctl to
// be exercised. Nothing in the signature says whether the program ran and
// failed or was never started: the caller tells those apart with NotInstalled,
// which is the distinction an operator's error message depends on.
type ExecFunc func(
	ctx context.Context,
	program string,
	args []string,
	stdin io.Reader,
	stdout, stderr io.Writer,
) error

// DefaultExec runs the program through the operating system. It is the seam's
// production implementation and the only place this package starts a process.
func DefaultExec(
	ctx context.Context,
	program string,
	args []string,
	stdin io.Reader,
	stdout, stderr io.Writer,
) error {
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

// NotInstalled reports whether an ExecFunc error means the program was not
// found, rather than that it ran and failed. The distinction is the whole
// difference between "install podman" and "podman said no", and the standard
// library is the only layer that knows which happened.
func NotInstalled(err error) bool {
	return errors.Is(err, exec.ErrNotFound)
}
