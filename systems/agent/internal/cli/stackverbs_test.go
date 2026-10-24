package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/command"
)

// wantUnits pins the seven services Quadlet generates for the default profile. It is written out
// rather than derived from the profile on purpose: the CLI's words for the stack
// are a contract an operator types, and a rename should fail a test rather than
// quietly change what every verb acts on.
func wantUnits() []string {
	return []string{
		"q15-agent.service",
		"q15-exec.service",
		"q15-net-network.service",
		"q15-ollama.service",
		"q15-postgres.service",
		"q15-proxy.service",
		"q15-qdrant.service",
		"q15-web.service",
	}
}

type execCall struct {
	program string
	args    []string
	// stdin is what the program was handed to read. It is recorded because the
	// secret verbs promise that a value travels only this way.
	stdin string
}

// fakeExec records what a verb ran and answers with whatever the test says. It
// is the process seam, so no test starts podman, systemd or journalctl.
type fakeExec struct {
	calls   []execCall
	handler func(program string, args []string) (stdout, stderr string, err error)
}

func (f *fakeExec) run(
	_ context.Context,
	program string,
	args []string,
	stdin io.Reader,
	stdout, stderr io.Writer,
) error {
	call := execCall{program: program, args: append([]string(nil), args...)}
	if stdin != nil {
		read, err := io.ReadAll(stdin)
		if err != nil {
			return err
		}
		call.stdin = string(read)
	}
	f.calls = append(f.calls, call)
	if f.handler == nil {
		return nil
	}
	out, errOut, err := f.handler(program, args)
	if stdout != nil {
		_, _ = io.WriteString(stdout, out)
	}
	if stderr != nil {
		_, _ = io.WriteString(stderr, errOut)
	}
	return err
}

// hasCall reports whether a verb ran exactly this program with exactly these
// arguments.
func (f *fakeExec) hasCall(program string, args ...string) bool {
	for _, call := range f.calls {
		if call.program != program || len(call.args) != len(args) {
			continue
		}
		if slicesEqual(call.args, args) {
			return true
		}
	}
	return false
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// collapse turns the report's column alignment into single spaces, so a test
// asserts on the columns rather than on the width the tab writer chose for them.
func collapse(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// withUnits appends the stack's units to the arguments a verb was given.
func withUnits(args ...string) []string {
	return append(append([]string(nil), args...), wantUnits()...)
}

func newEnv(fake *fakeExec) (command.Env, *bytes.Buffer, *bytes.Buffer) {
	var stdout, stderr bytes.Buffer
	env := command.Env{
		Stdout:  &stdout,
		Stderr:  &stderr,
		Release: "v1.2.3",
		Exec:    fake.run,
	}
	return env, &stdout, &stderr
}

// TestUpReloadsThenStarts pins the two steps and their order: the Quadlet
// generator runs during daemon-reload, so starting before reloading would start
// the units systemd had a moment ago.
func TestUpReloadsThenStarts(t *testing.T) {
	fake := &fakeExec{}
	env, stdout, _ := newEnv(fake)

	if err := Run(context.Background(), env, []string{"up"}); err != nil {
		t.Fatalf("Run(up) error = %v", err)
	}

	want := []execCall{
		{program: "systemctl", args: []string{"--user", "daemon-reload"}},
		{program: "systemctl", args: withUnits("--user", "start")},
	}
	if len(fake.calls) != len(want) {
		t.Fatalf("calls = %v, want %v", fake.calls, want)
	}
	for i, call := range want {
		if !slicesEqual(fake.calls[i].args, call.args) || fake.calls[i].program != call.program {
			t.Fatalf("call %d = %v, want %v", i, fake.calls[i], call)
		}
	}
	// Everything up prints is a diagnostic, so nothing may land on stdout.
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}

// TestUpAndDownAreIdempotentInShape is the cheap half of "safe to run twice":
// the second run asks systemd for exactly what the first did, and systemd is
// what makes that a no-op.
func TestUpAndDownAreIdempotentInShape(t *testing.T) {
	for _, args := range [][]string{{"up"}, {"up"}, {"down"}, {"down"}} {
		fake := &fakeExec{}
		env, _, _ := newEnv(fake)
		if err := Run(context.Background(), env, args); err != nil {
			t.Fatalf("Run(%q) error = %v", args, err)
		}
		action := "start"
		if args[0] == "down" {
			action = "stop"
		}
		if !fake.hasCall("systemctl", withUnits("--user", action)...) {
			t.Fatalf("Run(%q) calls = %v, want systemctl %s", args, fake.calls, action)
		}
	}
}

// TestDownDoesNotReload is the difference between up and down stated as a test:
// stopping what runs is not a moment to change what is declared.
func TestDownDoesNotReload(t *testing.T) {
	fake := &fakeExec{}
	env, _, _ := newEnv(fake)

	if err := Run(context.Background(), env, []string{"down"}); err != nil {
		t.Fatalf("Run(down) error = %v", err)
	}
	if fake.hasCall("systemctl", "--user", "daemon-reload") {
		t.Fatalf("calls = %v, want no daemon-reload", fake.calls)
	}
}

func TestStackVerbsRejectArguments(t *testing.T) {
	for _, name := range []string{"up", "down", "status"} {
		t.Run(name, func(t *testing.T) {
			fake := &fakeExec{}
			env, stdout, _ := newEnv(fake)

			err := Run(context.Background(), env, []string{name, "extra"})
			if err == nil {
				t.Fatalf("Run(%s extra) error = nil, want a rejection", name)
			}
			if got := command.ExitCode(err); got != 2 {
				t.Fatalf("ExitCode(error) = %d, want 2", got)
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
			if len(fake.calls) != 0 {
				t.Fatalf("calls = %v, want nothing run", fake.calls)
			}
		})
	}
}

func TestUnknownFlagIsAUsageError(t *testing.T) {
	fake := &fakeExec{}
	env, _, stderr := newEnv(fake)

	err := Run(context.Background(), env, []string{"up", "--bogus"})
	if err == nil {
		t.Fatal("Run(up --bogus) error = nil, want a rejection")
	}
	if got := command.ExitCode(err); got != 2 {
		t.Fatalf("ExitCode(error) = %d, want 2", got)
	}
	if !strings.Contains(stderr.String(), "bogus") {
		t.Fatalf("stderr = %q, want the flag named", stderr.String())
	}
}

// containerList is what podman ps --format json answers with.
func containerList(states map[string]string) string {
	type entry struct {
		Names []string `json:"Names"`
		State string   `json:"State"`
	}
	entries := make([]entry, 0, len(states))
	for name, state := range states {
		entries = append(entries, entry{Names: []string{name}, State: state})
	}
	encoded, err := json.Marshal(entries)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

// runningContainers is every container of the stack, running.
func runningContainers() map[string]string {
	return map[string]string{
		"q15-agent":    "running",
		"q15-exec":     "running",
		"q15-ollama":   "running",
		"q15-postgres": "running",
		"q15-proxy":    "running",
		"q15-qdrant":   "running",
		"q15-web":      "running",
	}
}

// statusExec answers systemctl and podman for a stack whose systemd state is
// given per unit.
func statusExec(unitStates map[string]string, containers map[string]string) *fakeExec {
	return &fakeExec{handler: func(program string, args []string) (string, string, error) {
		switch program {
		case "systemctl":
			unit := args[len(args)-1]
			state, ok := unitStates[unit]
			if !ok {
				state = "inactive"
			}
			// systemd reports state through its exit status as well as its
			// output: "inactive" is an answer, and it arrives with a failure.
			var err error
			if state != "active" {
				err = fmt.Errorf("exit status 3")
			}
			return state + "\n", "", err
		case "podman":
			return containerList(containers), "", nil
		}
		return "", "", nil
	}}
}

// TestStatusReportsEveryUnitAndFailsWhenPartlyUp pins both halves of the
// acceptance criteria at once: a per-unit report rather than one verdict, and a
// non-zero exit that names what is down instead of printing a stack trace.
func TestStatusReportsEveryUnitAndFailsWhenPartlyUp(t *testing.T) {
	states := map[string]string{}
	for _, unit := range wantUnits() {
		states[unit] = "active"
	}
	states["q15-web.service"] = "inactive"

	env, stdout, _ := newEnv(statusExec(states, runningContainers()))
	err := Run(context.Background(), env, []string{"status"})
	if err == nil {
		t.Fatal("Run(status) error = nil, want a non-zero report")
	}
	if got := command.ExitCode(err); got != 1 {
		t.Fatalf("ExitCode(error) = %d, want 1", got)
	}
	if !strings.Contains(err.Error(), "q15-web.service is inactive") {
		t.Fatalf("error = %q, want it to name the stopped unit", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "UNIT") || !strings.Contains(out, "CONTAINER") {
		t.Fatalf("stdout = %q, want a header", out)
	}
	for _, unit := range wantUnits() {
		if !strings.Contains(out, unit) {
			t.Fatalf("stdout = %q, want a row for %s", out, unit)
		}
	}
	if !strings.Contains(collapse(out), "q15-web.service inactive running") {
		t.Fatalf("stdout = %q, want the stopped unit reported beside its running container", out)
	}
	// The network unit owns no container, and a dash is what says so.
	if !strings.Contains(collapse(out), "q15-net-network.service active -") {
		t.Fatalf("stdout = %q, want the network row to carry no container", out)
	}
}

// TestStatusReportsAMissingContainer covers the failure that is not a systemd
// one: the unit is active and the container it owns is not there.
func TestStatusReportsAMissingContainer(t *testing.T) {
	states := map[string]string{}
	for _, unit := range wantUnits() {
		states[unit] = "active"
	}
	containers := runningContainers()
	delete(containers, "q15-qdrant")

	env, stdout, _ := newEnv(statusExec(states, containers))
	err := Run(context.Background(), env, []string{"status"})
	if err == nil {
		t.Fatal("Run(status) error = nil, want a non-zero report")
	}
	if !strings.Contains(err.Error(), "q15-qdrant has no container") {
		t.Fatalf("error = %q, want it to name the missing container", err)
	}
	if !strings.Contains(collapse(stdout.String()), "q15-qdrant.service active absent") {
		t.Fatalf("stdout = %q, want the absent container reported", stdout.String())
	}
}

func TestStatusWhenEverythingRuns(t *testing.T) {
	states := map[string]string{}
	for _, unit := range wantUnits() {
		states[unit] = "active"
	}

	env, stdout, _ := newEnv(statusExec(states, runningContainers()))
	if err := Run(context.Background(), env, []string{"status"}); err != nil {
		t.Fatalf("Run(status) error = %v", err)
	}
	if !strings.Contains(collapse(stdout.String()), "q15-agent.service active running") {
		t.Fatalf("stdout = %q, want the running stack reported", stdout.String())
	}
}

// TestStatusWithoutPodmanSaysSo is the "clear message, not a stack trace" case:
// a missing binary is a different sentence to an operator than a failed command.
func TestStatusWithoutPodmanSaysSo(t *testing.T) {
	states := map[string]string{}
	for _, unit := range wantUnits() {
		states[unit] = "active"
	}

	fake := statusExec(states, runningContainers())
	fake.handler = func(program string, args []string) (string, string, error) {
		if program == "podman" {
			return "", "", &exec.Error{Name: "podman", Err: exec.ErrNotFound}
		}
		return states[args[len(args)-1]] + "\n", "", nil
	}

	env, _, _ := newEnv(fake)
	err := Run(context.Background(), env, []string{"status"})
	if err == nil {
		t.Fatal("Run(status) error = nil, want a failure")
	}
	if !strings.Contains(err.Error(), "podman is not installed") {
		t.Fatalf("error = %q, want it to name the missing program", err)
	}
	if got := command.ExitCode(err); got != 1 {
		t.Fatalf("ExitCode(error) = %d, want 1", got)
	}
}

// TestLogsDefaultsToEveryUnit is what an operator wants when they do not yet
// know which part is unhappy.
func TestLogsDefaultsToEveryUnit(t *testing.T) {
	fake := &fakeExec{handler: func(string, []string) (string, string, error) {
		return "a journal line\n", "", nil
	}}
	env, stdout, _ := newEnv(fake)

	if err := Run(context.Background(), env, []string{"logs"}); err != nil {
		t.Fatalf("Run(logs) error = %v", err)
	}
	if len(fake.calls) != 1 || fake.calls[0].program != "journalctl" {
		t.Fatalf("calls = %v, want one journalctl call", fake.calls)
	}
	args := fake.calls[0].args
	if len(args) != 1+len(wantUnits())*2 {
		t.Fatalf("args = %v, want --user and -u with each unit", args)
	}
	for i, unit := range wantUnits() {
		if args[1+i*2] != "-u" || args[2+i*2] != unit {
			t.Fatalf("args = %v, want -u %s at %d", args, unit, 1+i*2)
		}
	}
	if stdout.String() != "a journal line\n" {
		t.Fatalf("stdout = %q, want the journal", stdout.String())
	}
}

// TestLogsSelectsOneService covers both spellings an operator might use, and the
// flag written after the service, which the standard library leaves behind.
func TestLogsSelectsOneService(t *testing.T) {
	for _, args := range [][]string{
		{"logs", "agent"},
		{"logs", "q15-agent"},
		{"logs", "q15-agent.service"},
		{"logs", "q15-agent.container"},
		{"logs", "q15-agent", "-f"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			fake := &fakeExec{}
			env, _, _ := newEnv(fake)

			if err := Run(context.Background(), env, args); err != nil {
				t.Fatalf("Run(%q) error = %v", args, err)
			}
			following := args[len(args)-1] == "-f"
			want := []string{"--user", "-u", "q15-agent.service"}
			if following {
				want = append(want, "--follow")
			}
			if !fake.hasCall("journalctl", want...) {
				t.Fatalf("Run(%q) calls = %v, want journalctl %v", args, fake.calls, want)
			}
		})
	}
}

func TestLogsUnknownServiceIsAUsageError(t *testing.T) {
	fake := &fakeExec{}
	env, stdout, _ := newEnv(fake)

	err := Run(context.Background(), env, []string{"logs", "bogus"})
	if err == nil {
		t.Fatal("Run(logs bogus) error = nil, want a rejection")
	}
	if got := command.ExitCode(err); got != 2 {
		t.Fatalf("ExitCode(error) = %d, want 2", got)
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("error = %q, want it to name the service", err)
	}
	// The message lists what is available, so the operator does not have to
	// guess a second time.
	if !strings.Contains(err.Error(), "agent") {
		t.Fatalf("error = %q, want it to list the services", err)
	}
	if stdout.Len() != 0 || len(fake.calls) != 0 {
		t.Fatalf("stdout = %q calls = %v, want nothing run", stdout.String(), fake.calls)
	}
}

// TestStackFlagNamesAnotherStack keeps the one reason the flags exist: a second
// stack is a different set of units, and every verb has to act on the one it was
// asked for.
func TestStackFlagNamesAnotherStack(t *testing.T) {
	fake := &fakeExec{}
	env, _, _ := newEnv(fake)

	if err := Run(context.Background(), env, []string{"up", "--stack", "Demo Two"}); err != nil {
		t.Fatalf("Run(up --stack) error = %v", err)
	}
	if len(fake.calls) != 2 {
		t.Fatalf("calls = %v, want a reload and a start", fake.calls)
	}
	start := fake.calls[1].args
	if !strings.Contains(strings.Join(start, " "), "demo-two-agent.service") {
		t.Fatalf("args = %v, want the second stack's units", start)
	}
	for _, arg := range start {
		if strings.HasPrefix(arg, "q15-") {
			t.Fatalf("args = %v, want no unit of the default stack", start)
		}
	}
}
