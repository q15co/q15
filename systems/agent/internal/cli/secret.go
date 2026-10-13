package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/command"
	"github.com/q15co/q15/systems/agent/internal/secret"
)

// runSecret is the operator-facing secret surface: it adds a secret, lists what
// podman knows about, and re-provisions a store on a host that has none.
//
// The surface is deliberately write-only. No verb prints a stored value on any
// route: the only code that reads one back is the driver command podman calls,
// which is registered with podman rather than offered to a person.
func runSecret(ctx context.Context, env command.Env, args []string) error {
	env = env.WithDefaults()
	if len(args) == 0 || asksForHelp(args[0]) {
		writeSecretUsage(env.Stdout)
		return nil
	}

	switch args[0] {
	case "set":
		return runSecretSet(ctx, env, args[1:])
	case "list":
		return runSecretList(ctx, env, args[1:])
	case "provision":
		return runSecretProvision(ctx, env, args[1:])
	default:
		return command.Usagef("unknown secret command %q", args[0])
	}
}

// runSecretDriver is the process surface podman's shell secret driver calls. It
// is not in the help, because it is registered with podman rather than offered
// to a person, and what it writes to standard output is parsed rather than
// read.
func runSecretDriver(ctx context.Context, env command.Env, args []string) error {
	env = env.WithDefaults()

	flags := secretFlags{}
	fs := newSecretFlagSet("secret-driver", env, &flags)
	if err := fs.Parse(args); err != nil {
		return command.AsUsage(err)
	}

	store, err := flags.store(env)
	if err != nil {
		return err
	}
	return secret.RunDriver(ctx, store, env, fs.Args())
}

// writeSecretUsage prints what the secret surface can do, and says the one
// thing about it an operator has to know before relying on it.
func writeSecretUsage(w io.Writer) {
	fmt.Fprint(w, `q15 secret adds a stack secret, lists what podman knows about, and
re-provisions a store on a host that has none.

usage:
  q15 secret set NAME [FILE]    store or rotate one secret. The value comes from
                                FILE, from standard input, or from a hidden prompt
  q15 secret list               what podman knows: names, driver and timestamps
  q15 secret provision [NAME]   prompt for every stored secret again on a new host

There is no verb that prints a stored value, on any route. A secret that is set
or rotated reaches a running container when that container is recreated, and the
store is bound to the machine that sealed it: there is no backup, and recovery is
issuing the credential again at its source.
`)
}

// secretFlags is where a secret verb finds its store: which stack, where the
// payloads live, and how they are sealed.
type secretFlags struct {
	stackFlags
	storeDir string
	seal     string
}

// newSecretFlagSet returns the flag set for one secret verb.
func newSecretFlagSet(name string, env command.Env, flags *secretFlags) *flag.FlagSet {
	fs := newStackFlagSet(name, env, &flags.stackFlags)
	flags.seal = string(secret.KindSystemdCreds)
	fs.StringVar(
		&flags.storeDir,
		"store-dir",
		"",
		"where the sealed payloads live (default: inside the stack's state directory)",
	)
	fs.StringVar(
		&flags.seal,
		"seal",
		string(secret.KindSystemdCreds),
		"how payloads are sealed: "+string(secret.KindSystemdCreds)+" or "+string(secret.KindFile),
	)
	return fs
}

// store opens the store the flags describe, and says so out loud when it is the
// plaintext fallback. A fallback nobody notices they are on is worse than no
// fallback, so the warning is unconditional rather than once.
func (f secretFlags) store(env command.Env) (*secret.Store, error) {
	profile, err := f.stackFlags.profile(env.Release)
	if err != nil {
		return nil, err
	}
	dir := f.storeDir
	if dir == "" {
		dir = filepath.Join(profile.StateDir, "secrets")
	}
	kind := secret.Kind(f.seal)
	if kind == secret.KindFile {
		fmt.Fprintf(
			env.Stderr,
			"warning: sealing with plain files: %s holds a stack's credentials unencrypted at mode 0600\n",
			dir,
		)
	}
	return secret.Open(secret.Options{Dir: dir, Kind: kind, Exec: env.Exec})
}

// driverArgs are the flags the registered driver command has to carry, so that
// the command podman runs opens the same store the operator wrote to rather
// than whatever the defaults happen to be at the time.
func (f secretFlags) driverArgs() []string {
	args := []string{"--stack", f.agentName}
	if f.stateDir != "" {
		args = append(args, "--state-dir", f.stateDir)
	}
	if f.storeDir != "" {
		args = append(args, "--store-dir", f.storeDir)
	}
	if f.seal != "" {
		args = append(args, "--seal", f.seal)
	}
	return args
}

// registerDriver writes the drop-in that makes podman route its secret store
// through this binary. It is written on every write, because the registered
// command carries the store's location and kind: a command that described a
// different store than the one just written to would seal the next payload
// somewhere nobody is looking.
func registerDriver(env command.Env, flags secretFlags) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("secret: locate the running binary: %w", err)
	}
	contents, err := secret.DriverDropIn(
		[]string{self, "secret-driver"},
		flags.driverArgs()...,
	)
	if err != nil {
		return err
	}
	path, err := secret.DefaultDropInPath()
	if err != nil {
		return err
	}
	wrote, err := secret.WriteDropIn(path, contents)
	if err != nil {
		return err
	}
	if wrote {
		fmt.Fprintf(env.Stderr, "secret: registered the secret driver in %s\n", path)
	}
	return nil
}

// runSecretSet stores or rotates one secret.
//
// The payload reaches the store through podman: podman records the name and the
// identifier, and hands the value to the driver command, which seals it. That
// is what keeps a container able to read the value at `/run/secrets/<name>`
// without the stack owning a second copy of the mapping.
func runSecretSet(ctx context.Context, env command.Env, args []string) error {
	flags := secretFlags{}
	fs := newSecretFlagSet("set", env, &flags)
	if err := fs.Parse(args); err != nil {
		return command.AsUsage(err)
	}

	words := fs.Args()
	if len(words) == 0 || len(words) > 2 {
		return command.Usagef(
			"secret set takes a name and an optional file, got %d arguments",
			len(words),
		)
	}
	name := strings.TrimSpace(words[0])
	if name == "" {
		return command.Usagef("secret set needs a name")
	}
	path := ""
	if len(words) == 2 {
		path = words[1]
	}

	store, err := flags.store(env)
	if err != nil {
		return err
	}
	if err := registerDriver(env, flags); err != nil {
		return err
	}

	value, err := readSecretValue(ctx, env, name, path)
	if err != nil {
		return err
	}
	if err := createPodmanSecret(ctx, env, driverOpts(store, flags), name, value); err != nil {
		return err
	}
	fmt.Fprintf(
		env.Stderr,
		"secret: stored %s; it reaches a running container when that container is recreated\n",
		name,
	)
	return nil
}

// runSecretList reports what podman knows about the stack's secrets, which is
// where names and timestamps live: the store itself holds only identifiers.
func runSecretList(ctx context.Context, env command.Env, args []string) error {
	flags := secretFlags{}
	fs := newSecretFlagSet("list", env, &flags)
	if err := fs.Parse(args); err != nil {
		return command.AsUsage(err)
	}
	if fs.NArg() > 0 {
		return command.Usagef("secret list takes no arguments, got %q", fs.Arg(0))
	}

	store, err := flags.store(env)
	if err != nil {
		return err
	}
	// The store names itself on the diagnostic stream, so a host on the
	// plaintext fallback never has to wonder which one it is on.
	fmt.Fprintf(env.Stderr, "store: %s in %s\n", store.Kind(), store.Dir())

	if err := env.Exec(ctx, "podman", []string{"secret", "ls"}, nil, env.Stdout, env.Stderr); err != nil {
		if command.NotInstalled(err) {
			return fmt.Errorf(
				"podman is not installed, and it is what maps a secret's name to its identifier: %w",
				err,
			)
		}
		return fmt.Errorf("list the secrets: %w", err)
	}
	return nil
}

// runSecretProvision re-prompts for every stored secret, which is what a new
// host needs after a reinstall: the store is bound to the machine that sealed
// it, so nothing it held comes across, while podman's own record of the names
// may have been restored.
func runSecretProvision(ctx context.Context, env command.Env, args []string) error {
	flags := secretFlags{}
	fs := newSecretFlagSet("provision", env, &flags)
	if err := fs.Parse(args); err != nil {
		return command.AsUsage(err)
	}

	names := fs.Args()
	store, err := flags.store(env)
	if err != nil {
		return err
	}
	if err := registerDriver(env, flags); err != nil {
		return err
	}

	if len(names) == 0 {
		names, err = podmanSecretNames(ctx, env)
		if err != nil {
			return err
		}
		if len(names) == 0 {
			return errors.New(
				"secret: nothing to provision: name the secrets, or create them through podman first",
			)
		}
	}

	// Provisioning re-prompts for every value, and a single pipe cannot say
	// which value belongs to which name, so this one verb wants a terminal.
	if !env.IsTerminal() {
		return command.Usagef(
			"provision prompts for each value and needs a terminal; use 'q15 secret set NAME [FILE]' to give one value from a pipe or a file",
		)
	}

	opts := driverOpts(store, flags)
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		value, err := readSecretValue(ctx, env, name, "")
		if err != nil {
			return err
		}
		if err := createPodmanSecret(ctx, env, opts, name, value); err != nil {
			return err
		}
		fmt.Fprintf(env.Stderr, "secret: stored %s\n", name)
	}
	return nil
}

// readSecretValue reads one value: from the file the operator named, from
// standard input, or from a hidden prompt on a terminal. The value never enters
// an argument list, so it never appears in a process listing.
//
// Trailing newlines are dropped, because the three routes disagree about them
// otherwise: a prompt ends with one, `echo token |` adds one, and a file may or
// may not. A credential that genuinely ends in a newline has to be stored from
// a file that does, and this is the one place the store is opinionated.
func readSecretValue(ctx context.Context, env command.Env, name, path string) ([]byte, error) {
	if path != "" {
		value, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("secret: read %s: %w", path, err)
		}
		return trimmedValue(name, value)
	}

	if !env.IsTerminal() {
		value, err := io.ReadAll(env.Stdin)
		if err != nil {
			return nil, fmt.Errorf("secret: read the value: %w", err)
		}
		return trimmedValue(name, value)
	}

	fmt.Fprintf(env.Stderr, "value for %s (input hidden, end with a newline): ", name)
	if err := setEcho(ctx, env, false); err != nil {
		return nil, err
	}
	line, readErr := bufio.NewReader(env.Stdin).ReadString('\n')
	echoErr := setEcho(ctx, env, true)
	fmt.Fprintln(env.Stderr)
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return nil, fmt.Errorf("secret: read the value: %w", readErr)
	}
	if echoErr != nil {
		return nil, echoErr
	}
	return trimmedValue(name, []byte(line))
}

// setEcho turns terminal echo off for a hidden prompt, and back on afterwards.
// stty acts on the terminal it is handed on standard input, which is the one
// the prompt read from.
func setEcho(ctx context.Context, env command.Env, on bool) error {
	arg := "-echo"
	if on {
		arg = "echo"
	}
	if err := env.Exec(ctx, "stty", []string{arg}, env.Stdin, io.Discard, env.Stderr); err != nil {
		return fmt.Errorf("secret: turn the terminal echo back %s: %w", arg, err)
	}
	return nil
}

// trimmedValue refuses an empty value and drops the newlines the three routes
// disagree about.
func trimmedValue(name string, value []byte) ([]byte, error) {
	trimmed := bytes.TrimRight(value, "\r\n")
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("secret: refusing to store an empty value for %s", name)
	}
	return trimmed, nil
}

// driverOpts is what podman is told on this command line, so a secret created
// here reaches this store whether or not podman picked up the drop-in.
func driverOpts(store *secret.Store, flags secretFlags) string {
	prefix := append([]string{selfPath(), "secret-driver"}, driverArgsFor(store, flags)...)
	parts := make([]string, 0, 4)
	for _, verb := range []string{secret.DriverList, secret.DriverLookup, secret.DriverStore, secret.DriverDelete} {
		parts = append(
			parts,
			verb+"="+strings.Join(append(append([]string(nil), prefix...), verb), " "),
		)
	}
	return strings.Join(parts, ",")
}

// driverArgsFor is the flags the driver command needs to open this store,
// written the way a shell has to see them.
func driverArgsFor(store *secret.Store, flags secretFlags) []string {
	args := []string{"--stack", flags.agentName, "--store-dir", store.Dir()}
	if store.Kind() != secret.KindSystemdCreds {
		args = append(args, "--seal", string(store.Kind()))
	}
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, shellWord(arg))
	}
	return quoted
}

// selfPath is the running binary's own path, which is what podman has to be
// told to run.
func selfPath() string {
	self, err := os.Executable()
	if err != nil {
		return "q15"
	}
	return self
}

// shellWord quotes one word for the shell podman runs a driver command
// through.
func shellWord(word string) string {
	return "'" + strings.ReplaceAll(word, "'", `'\''`) + "'"
}

// createPodmanSecret hands the value to podman, which records the name and
// passes the payload to the driver this command line names.
func createPodmanSecret(
	ctx context.Context,
	env command.Env,
	opts, name string,
	value []byte,
) error {
	var message strings.Builder
	err := env.Exec(ctx, "podman", []string{
		"secret", "create",
		"--replace",
		"--driver=shell",
		"--driver-opts", opts,
		name, "-",
	}, bytes.NewReader(value), io.Discard, &message)
	if err != nil {
		if command.NotInstalled(err) {
			return fmt.Errorf(
				"podman is not installed, and a secret is created through it: %w",
				err,
			)
		}
		if detail := strings.TrimSpace(message.String()); detail != "" {
			return fmt.Errorf("create the secret %s: %s", name, detail)
		}
		return fmt.Errorf("create the secret %s: %w", name, err)
	}
	return nil
}

// podmanSecretNames reads the names podman has records for, which is the list a
// restored host has to re-provision.
func podmanSecretNames(ctx context.Context, env command.Env) ([]string, error) {
	var out strings.Builder
	err := env.Exec(
		ctx,
		"podman",
		[]string{"secret", "ls", "--format", "{{.Name}}"},
		nil,
		&out,
		io.Discard,
	)
	if err != nil {
		if command.NotInstalled(err) {
			return nil, fmt.Errorf("podman is not installed: %w", err)
		}
		return nil, fmt.Errorf("list the secret names: %w", err)
	}

	names := make([]string, 0, 8)
	for _, line := range strings.Split(out.String(), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			names = append(names, line)
		}
	}
	return names, nil
}
