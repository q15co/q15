package command

import (
	"os"
)

// DefaultLookupEnv reads an environment variable from the process, and is what
// WithDefaults supplies when a caller has not. It exists so a command reads the
// environment through the seam like everything else: the driver commands are
// handed their identifier in SECRET_ID, and a test has to be able to set it
// without touching the process's own environment.
func DefaultLookupEnv(name string) (string, bool) {
	return os.LookupEnv(name)
}

// DefaultUnsetEnv changes only this process's environment, never its parent's.
func DefaultUnsetEnv(name string) error {
	return os.Unsetenv(name)
}

// DefaultIsTerminal reports whether the process's standard input is a
// terminal. It is the default for Env.IsTerminal.
//
// The test is a character device, which is what the standard library offers
// without another dependency. It is deliberately not a definition of "a person
// is watching": a pty and a real terminal both answer yes, and that is enough
// for the one question it is asked, which is whether to prompt.
func DefaultIsTerminal() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
