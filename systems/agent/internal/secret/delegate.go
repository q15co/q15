package secret

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/command"
)

// Delegation, and why it exists.
//
// Rootless podman re-executes itself inside a user namespace, and the shell
// secret driver runs its commands in that namespace. There the effective uid is
// 0 and the namespace's uid map points somewhere else entirely, which is
// exactly the case systemd-creds branches on when it decides whether to ask the
// user manager or to do the cryptography in-process against
// /var/lib/systemd/credential.secret. That file belongs to the host's root and
// is not mapped inside the namespace, so the in-process branch dies with
// "Failed to determine local credential host secret: Permission denied".
//
// The work therefore goes to the user manager over the session bus, with
// systemd-run carrying it there. What is handed over is systemd-creds itself,
// not this program: the value still arrives on standard input and leaves on
// standard output, so nothing about the driver contract changes.
const (
	uidMapPath = "/proc/self/uid_map"
	// nullKeyOption is the one key option that needs no key material at all and
	// therefore has nothing to delegate.
	nullKeyOption = "--with-key=null"
)

// needsDelegation reports whether this process has to hand the cryptography to
// the user manager.
func (s *Store) needsDelegation() bool {
	euid, uidMap := delegationFacts()
	return needsDelegationFor(s.kind, s.keyArgs, euid, uidMap)
}

// delegationFacts is where the decision reads the process, and a test replaces
// it. It is a variable rather than a direct call because the context it
// describes only occurs inside a container, and a test should not have to be
// run in one to pin the behaviour - or, worse, be run in one by accident and
// then depend on it. Tests are not parallel, so replacing it is safe.
var delegationFacts = func() (euid int, uidMap string) {
	return os.Geteuid(), readUIDMap()
}

// needsDelegationFor is the whole decision, written as a function of the facts
// so that it can be tested without being run inside a container: the kind, the
// key options, the effective uid, and this process's uid map.
func needsDelegationFor(kind Kind, keyArgs []string, euid int, uidMap string) bool {
	if kind != KindSystemdCreds {
		return false
	}
	for _, arg := range keyArgs {
		if arg == nullKeyOption {
			return false
		}
	}
	if euid != 0 {
		return false
	}
	inside, outside, ok := firstIDMapRange(uidMap)
	return ok && inside == 0 && outside != 0
}

// readUIDMap reads this process's user namespace mapping, or reports an empty
// string when the kernel does not offer one.
func readUIDMap() string {
	data, err := os.ReadFile(uidMapPath)
	if err != nil {
		return ""
	}
	return string(data)
}

// firstIDMapRange reads the first line of a uid map, which is written as
// "<inside> <outside> <count>". An unmapped first range that starts at 0 inside
// and points somewhere else outside is what says "we are root inside somebody
// else's namespace".
func firstIDMapRange(uidMap string) (inside, outside int, ok bool) {
	fields := strings.Fields(uidMap)
	if len(fields) < 2 {
		return 0, 0, false
	}
	insideValue, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, 0, false
	}
	outsideValue, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, 0, false
	}
	return insideValue, outsideValue, true
}

// delegate hands one systemd-creds operation to the user manager. The arguments
// are systemd-creds' own, so the work is the same operation in a context that
// can reach the key.
func (s *Store) delegate(
	ctx context.Context,
	verb string,
	args []string,
	stdin io.Reader,
	stdout, stderr io.Writer,
) error {
	program, err := s.resolveCreds(ctx)
	if err != nil {
		return err
	}

	runArgs := make([]string, 0, len(args)+6)
	runArgs = append(runArgs, "--user", "--wait", "--pipe", "--quiet", "--collect", program)
	runArgs = append(runArgs, args...)

	if err := s.exec(ctx, "systemd-run", runArgs, stdin, stdout, stderr); err != nil {
		if command.NotInstalled(err) {
			return fmt.Errorf(
				"secret: inside a user namespace %s cannot reach its key, and systemd-run is not installed to hand the %s to the user manager: %w",
				s.credsProgram(),
				verb,
				err,
			)
		}
		return fmt.Errorf("secret: hand the %s to the user manager: %w", verb, err)
	}
	return nil
}

// resolveCreds finds the systemd-creds to run. Delegated work is executed by
// the user manager, whose environment is not this process's, so a program that
// was only ever found on PATH has to be named by its absolute path before it is
// handed over.
func (s *Store) resolveCreds(ctx context.Context) (string, error) {
	configured := strings.TrimSpace(s.creds)
	if filepath.IsAbs(configured) {
		return configured, nil
	}
	program := s.credsProgram()
	if path, ok := s.pathOf(ctx, program); ok {
		return path, nil
	}
	return "", fmt.Errorf(
		"secret: cannot resolve %s to an absolute path, which handing the work to the user manager needs",
		program,
	)
}

// pathOf asks the shell where a program is. The shell is asked rather than PATH
// being walked here, because the driver commands are themselves run as
// `<shell> -c <command>`, so the shell's answer is the one that decides.
func (s *Store) pathOf(ctx context.Context, program string) (string, bool) {
	var out strings.Builder
	err := s.exec(
		ctx,
		"sh",
		[]string{"-c", "command -v " + ShellWord(program)},
		nil,
		&out,
		io.Discard,
	)
	if err != nil {
		return "", false
	}
	path := strings.TrimSpace(out.String())
	if !filepath.IsAbs(path) {
		return "", false
	}
	return path, true
}

// ShellWord wraps a word in single quotes, which is how a name survives a trip
// through the shell unchanged.
func ShellWord(word string) string {
	return "'" + strings.ReplaceAll(word, "'", `'\''`) + "'"
}
