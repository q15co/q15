package command

import (
	"errors"
	"fmt"
)

// UsageError reports a command line that is wrong in shape: a command that does
// not exist, an argument a command does not take, a flag it does not know.
//
// The class is kept separate from every other failure because it maps to a
// different process exit status, which is what lets a script tell "you called
// me wrong" from "I could not do it".
type UsageError struct {
	err error
}

// Error implements error.
func (e *UsageError) Error() string { return e.err.Error() }

// Unwrap exposes the wrapped error to errors.Is and errors.As.
func (e *UsageError) Unwrap() error { return e.err }

// Usagef builds a UsageError from a format string, like fmt.Errorf.
func Usagef(format string, args ...any) *UsageError {
	return &UsageError{err: fmt.Errorf(format, args...)}
}

// AsUsage marks an error an inner layer returned as a shape mistake. The flag
// package reports an unknown flag as an ordinary error, so a command that hands
// one back wraps it here rather than letting the exit status call it a failure.
// err must not be nil.
func AsUsage(err error) *UsageError {
	return &UsageError{err: err}
}

// ExitCode maps an error to the process exit status, and main is its only
// caller: 0 for success, 2 for a command line that is wrong in shape, and 1 for
// everything else. The mapping lives here rather than in main so it is testable
// without a subprocess.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}

	var usage *UsageError
	if errors.As(err, &usage) {
		return 2
	}
	return 1
}
