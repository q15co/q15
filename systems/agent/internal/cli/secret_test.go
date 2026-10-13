package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/command"
	"github.com/q15co/q15/systems/agent/internal/secret"
)

// The store the driver tests use.
const testStoreDirFlag = "--store-dir"

// secretEnv points every secret verb at throwaway homes, so no test writes a
// drop-in or a store into the real one, and supplies the standard input a value
// arrives on.
func secretEnv(
	t *testing.T,
	fake *fakeExec,
	stdin string,
) (command.Env, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	env, stdout, stderr := newEnv(fake)
	env.Stdin = strings.NewReader(stdin)
	// Nothing in a test is a terminal, and the environment of the test process
	// is not the one these verbs should read an identifier from.
	env.IsTerminal = func() bool { return false }
	env.LookupEnv = func(string) (string, bool) { return "", false }
	return env, stdout, stderr
}

// driverEnv is secretEnv with the file store selected, which is the kind a test
// can seal without a user manager.
func driverEnv(
	t *testing.T,
	fake *fakeExec,
	stdin, id string,
) (command.Env, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	env, stdout, stderr := secretEnv(t, fake, stdin)
	env.LookupEnv = func(name string) (string, bool) {
		if name == secret.SecretIDEnv && id != "" {
			return id, true
		}
		return "", false
	}
	return env, stdout, stderr
}

func driverArgs(dir, verb string) []string {
	return []string{"secret-driver", "--seal", string(secret.KindFile), testStoreDirFlag, dir, verb}
}

// TestSecretDriverIsReachableButUnlisted pins both halves of a hidden verb: it
// is in the registry, because podman has to reach it, and out of the command
// list, because nobody should be told to type it.
func TestSecretDriverIsReachableButUnlisted(t *testing.T) {
	var stdout bytes.Buffer
	env := command.Env{Stdout: &stdout, Release: "dev"}

	if err := Run(context.Background(), env, []string{"help"}); err != nil {
		t.Fatalf("Run(help) error = %v", err)
	}
	if strings.Contains(stdout.String(), "secret-driver") {
		t.Fatalf("help = %q, want the driver left unlisted", stdout.String())
	}
	if _, ok := lookup("secret-driver"); !ok {
		t.Fatal("lookup(secret-driver) = false, want it reachable")
	}
}

// TestSecretDriverKeepsStandardOutputPure is the contract, run as one test:
// podman parses this stream, so each command prints exactly what the protocol
// says and every human sentence goes to standard error.
func TestSecretDriverKeepsStandardOutputPure(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	env, stdout, stderr := driverEnv(t, &fakeExec{}, "sentinel-value", "abc123")
	if err := Run(ctx, env, driverArgs(dir, secret.DriverStore)); err != nil {
		t.Fatalf("store error = %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("store wrote %q to stdout, want nothing", stdout.String())
	}
	if !strings.Contains(stderr.String(), "warning:") {
		t.Fatalf("stderr = %q, want the plaintext fallback announced", stderr.String())
	}

	stdout.Reset()
	env, stdout, _ = driverEnv(t, &fakeExec{}, "", "abc123")
	if err := Run(ctx, env, driverArgs(dir, secret.DriverLookup)); err != nil {
		t.Fatalf("lookup error = %v", err)
	}
	if stdout.String() != "sentinel-value" {
		t.Fatalf("lookup wrote %q, want exactly the value", stdout.String())
	}

	stdout.Reset()
	env, stdout, _ = driverEnv(t, &fakeExec{}, "", "abc123")
	if err := Run(ctx, env, driverArgs(dir, secret.DriverList)); err != nil {
		t.Fatalf("list error = %v", err)
	}
	if stdout.String() != "abc123\n" {
		t.Fatalf("list wrote %q, want exactly one identifier and a newline", stdout.String())
	}

	stdout.Reset()
	env, stdout, _ = driverEnv(t, &fakeExec{}, "", "abc123")
	if err := Run(ctx, env, driverArgs(dir, secret.DriverDelete)); err != nil {
		t.Fatalf("delete error = %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("delete wrote %q to stdout, want nothing", stdout.String())
	}

	// Deleting what is already gone is a success, and printing the same
	// nothing: podman calls delete for a secret it is about to replace.
	stdout.Reset()
	env, stdout, _ = driverEnv(t, &fakeExec{}, "", "abc123")
	if err := Run(ctx, env, driverArgs(dir, secret.DriverDelete)); err != nil {
		t.Fatalf("second delete error = %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("second delete wrote %q to stdout, want nothing", stdout.String())
	}

	stdout.Reset()
	env, stdout, _ = driverEnv(t, &fakeExec{}, "", "abc123")
	if err := Run(ctx, env, driverArgs(dir, secret.DriverList)); err != nil {
		t.Fatalf("list error = %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("list wrote %q after the delete, want nothing", stdout.String())
	}
}

// TestSecretDriverNeedsAnIdentifier is the empty-identifier trap seen from the
// outside: without SECRET_ID there is nothing to act on, and the store refuses
// rather than resolving the empty name.
func TestSecretDriverNeedsAnIdentifier(t *testing.T) {
	dir := t.TempDir()
	for _, verb := range []string{secret.DriverLookup, secret.DriverStore, secret.DriverDelete} {
		t.Run(verb, func(t *testing.T) {
			env, stdout, _ := driverEnv(t, &fakeExec{}, "value", "")
			err := Run(context.Background(), env, driverArgs(dir, verb))
			if err == nil {
				t.Fatalf("%s error = nil, want a refusal", verb)
			}
			if !strings.Contains(err.Error(), secret.SecretIDEnv) {
				t.Fatalf("error = %q, want it to name the environment variable", err)
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want nothing", stdout.String())
			}
		})
	}
}

func TestSecretDriverRejectsAnUnknownCommand(t *testing.T) {
	dir := t.TempDir()
	env, _, _ := driverEnv(t, &fakeExec{}, "", "abc123")

	err := Run(context.Background(), env, driverArgs(dir, "inspect"))
	if err == nil {
		t.Fatal("error = nil, want a rejection")
	}
	if got := command.ExitCode(err); got != 2 {
		t.Fatalf("ExitCode(error) = %d, want 2", got)
	}
}

// TestSecretSetRegistersTheDriverAndHandsPodmanTheValue covers the whole write
// path: the drop-in appears, podman is handed the value on standard input, and
// the value never reaches an argument list where a process listing would show
// it.
func TestSecretSetRegistersTheDriverAndHandsPodmanTheValue(t *testing.T) {
	fake := &fakeExec{}
	env, stdout, stderr := secretEnv(t, fake, "sentinel-value\n")

	if err := Run(context.Background(), env, []string{"secret", "set", "--seal", "file", "my_api_key"}); err != nil {
		t.Fatalf("secret set error = %v", err)
	}

	var runs []execCall
	for _, call := range fake.calls {
		if call.program == "podman" {
			runs = append(runs, call)
		}
	}
	if len(runs) != 1 {
		t.Fatalf("podman was run %d times, want once: %v", len(runs), fake.calls)
	}
	if !strings.HasPrefix(
		strings.Join(runs[0].args, " "),
		"secret create --replace --driver=shell",
	) {
		t.Fatalf("podman args = %v, want a create against the shell driver", runs[0].args)
	}
	if runs[0].stdin != "sentinel-value" {
		t.Fatalf("podman stdin = %q, want the value with the newline dropped", runs[0].stdin)
	}
	for _, arg := range runs[0].args {
		if strings.Contains(arg, "sentinel-value") {
			t.Fatalf("podman args = %v, want the value nowhere in the argument list", runs[0].args)
		}
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty: setting a secret is not data", stdout.String())
	}
	if !strings.Contains(stderr.String(), "stored my_api_key") {
		t.Fatalf("stderr = %q, want it to say what happened", stderr.String())
	}
	if !strings.Contains(stderr.String(), "recreated") {
		t.Fatalf("stderr = %q, want it to say a container needs recreating", stderr.String())
	}

	path, err := secret.DefaultDropInPath()
	if err != nil {
		t.Fatalf("DefaultDropInPath() error = %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the drop-in: %v", err)
	}
	if !strings.Contains(string(contents), `driver = "shell"`) {
		t.Fatalf("drop-in = %q, want the shell driver", contents)
	}
	if !strings.Contains(string(contents), "--seal") {
		t.Fatalf("drop-in = %q, want it to carry the store the secret was written to", contents)
	}
}

// TestSecretSetReadsAValueFromAFile is the scripted route, and the one that does
// not need a terminal.
func TestSecretSetReadsAValueFromAFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "key.txt")
	if err := os.WriteFile(file, []byte("from-a-file\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	fake := &fakeExec{}
	env, _, _ := secretEnv(t, fake, "")
	if err := Run(context.Background(), env, []string{"secret", "set", "--seal", "file", "my_api_key", file}); err != nil {
		t.Fatalf("secret set error = %v", err)
	}

	for _, call := range fake.calls {
		if call.program == "podman" && call.stdin != "from-a-file" {
			t.Fatalf("podman stdin = %q, want the file's contents trimmed", call.stdin)
		}
	}
}

func TestSecretSetRefusesAnEmptyValue(t *testing.T) {
	fake := &fakeExec{}
	env, _, _ := secretEnv(t, fake, "\n\n")

	err := Run(context.Background(), env, []string{"secret", "set", "--seal", "file", "my_api_key"})
	if err == nil {
		t.Fatal("error = nil, want a refusal")
	}
	for _, call := range fake.calls {
		if call.program == "podman" {
			t.Fatalf("podman ran with an empty value: %v", fake.calls)
		}
	}
}

func TestSecretSetRejectsAMissingName(t *testing.T) {
	env, _, _ := secretEnv(t, &fakeExec{}, "value")

	err := Run(context.Background(), env, []string{"secret", "set"})
	if err == nil {
		t.Fatal("error = nil, want a rejection")
	}
	if got := command.ExitCode(err); got != 2 {
		t.Fatalf("ExitCode(error) = %d, want 2", got)
	}
}

// TestSecretListReportsTheStoreAndPassesPodmanThrough checks the two streams:
// podman's table is the answer and stays on stdout, and the store names itself
// where a person reads.
func TestSecretListReportsTheStoreAndPassesPodmanThrough(t *testing.T) {
	fake := &fakeExec{handler: func(program string, _ []string) (string, string, error) {
		if program == "podman" {
			return "ID        NAME\nabc123    my_api_key\n", "", nil
		}
		return "", "", nil
	}}
	env, stdout, stderr := secretEnv(t, fake, "")

	if err := Run(context.Background(), env, []string{"secret", "list", "--seal", "file"}); err != nil {
		t.Fatalf("secret list error = %v", err)
	}
	if !strings.Contains(stdout.String(), "my_api_key") {
		t.Fatalf("stdout = %q, want podman's table", stdout.String())
	}
	if !strings.Contains(stderr.String(), string(secret.KindFile)) {
		t.Fatalf("stderr = %q, want the store named", stderr.String())
	}
}

// TestSecretProvisionNeedsATerminal keeps a verb that prompts from hanging on a
// pipe it cannot answer, and names the route that works instead.
func TestSecretProvisionNeedsATerminal(t *testing.T) {
	env, _, _ := secretEnv(t, &fakeExec{}, "value")

	err := Run(context.Background(), env, []string{"secret", "provision", "my_api_key"})
	if err == nil {
		t.Fatal("error = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), "secret set") {
		t.Fatalf("error = %q, want it to name the scripted route", err)
	}
}

// TestSecretBarePrintsUsage keeps the group a request for help rather than a
// mistake, the way the other command groups behave.
func TestSecretBarePrintsUsage(t *testing.T) {
	env, stdout, _ := secretEnv(t, &fakeExec{}, "")

	if err := Run(context.Background(), env, []string{"secret"}); err != nil {
		t.Fatalf("error = %v", err)
	}
	if !strings.Contains(stdout.String(), "q15 secret set") {
		t.Fatalf("stdout = %q, want the secret usage", stdout.String())
	}
	if !strings.Contains(stdout.String(), "no verb that prints a stored value") {
		t.Fatalf("stdout = %q, want the write-only promise stated", stdout.String())
	}
}

func TestSecretRejectsAnUnknownCommand(t *testing.T) {
	env, _, _ := secretEnv(t, &fakeExec{}, "")

	err := Run(context.Background(), env, []string{"secret", "show", "my_api_key"})
	if err == nil {
		t.Fatal("error = nil, want a rejection")
	}
	if got := command.ExitCode(err); got != 2 {
		t.Fatalf("ExitCode(error) = %d, want 2", got)
	}
}
