package stack

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRenderWritesEveryUnit is the rendering contract: one file per unit, in the
// directory it was given, readable and complete.
func TestRenderWritesEveryUnit(t *testing.T) {
	dir := t.TempDir()
	profile := testProfile()

	result, err := profile.Render(dir)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if !result.Changed() {
		t.Fatal("first render reported no change, want every unit written")
	}
	if result.Dir != dir {
		t.Fatalf("result.Dir = %q, want %q", result.Dir, dir)
	}
	if len(result.Written) != len(profile.UnitNames()) {
		t.Fatalf("wrote %v, want all of %v", result.Written, profile.UnitNames())
	}
	if len(result.Unchanged) != 0 {
		t.Fatalf("unchanged = %v, want none on a first render", result.Unchanged)
	}

	units, err := profile.Units()
	if err != nil {
		t.Fatalf("Units() error = %v", err)
	}
	for _, unit := range units {
		path := filepath.Join(dir, unit.Name)

		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", unit.Name, err)
		}
		if got := info.Mode().Perm(); got != unitFileMode {
			t.Fatalf("%s mode = %v, want %v", unit.Name, got, unitFileMode)
		}

		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", unit.Name, err)
		}
		if !bytes.Equal(raw, unit.Contents) {
			t.Fatalf("%s on disk differs from the rendered unit", unit.Name)
		}
	}
}

// TestRenderIsIdempotent is what makes `up` safe to run twice: a second render
// writes nothing, and does not even touch the files' timestamps.
func TestRenderIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	profile := testProfile()

	if _, err := profile.Render(dir); err != nil {
		t.Fatalf("first Render() error = %v", err)
	}

	before := map[string]time.Time{}
	for _, name := range profile.UnitNames() {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		before[name] = info.ModTime()
	}

	// Long enough that a rewrite would be visible in the timestamps: without
	// this the comparison could pass on coarse clock resolution alone.
	time.Sleep(10 * time.Millisecond)

	result, err := profile.Render(dir)
	if err != nil {
		t.Fatalf("second Render() error = %v", err)
	}
	if result.Changed() {
		t.Fatalf("second render wrote %v, want nothing", result.Written)
	}
	if len(result.Unchanged) != len(profile.UnitNames()) {
		t.Fatalf("unchanged = %v, want every unit", result.Unchanged)
	}
	for name, want := range before {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if !info.ModTime().Equal(want) {
			t.Fatalf("%s was touched by a render that changed nothing", name)
		}
	}
}

// TestRenderRewritesOnlyWhatChanged keeps a render from churning the other
// stack's units or a file somebody else put in the shared directory.
func TestRenderRewritesOnlyWhatChanged(t *testing.T) {
	dir := t.TempDir()
	profile := testProfile()
	if _, err := profile.Render(dir); err != nil {
		t.Fatalf("Render() error = %v", err)
	}

	stale := filepath.Join(dir, "q15-web.container")
	if err := os.WriteFile(
		stale,
		[]byte("[Container]\nImage=something-else\n"),
		0o644,
	); err != nil {
		t.Fatalf("write the stale unit: %v", err)
	}
	foreign := filepath.Join(dir, "my-other-stack.container")
	if err := os.WriteFile(foreign, []byte("# not mine\n"), 0o644); err != nil {
		t.Fatalf("write the foreign unit: %v", err)
	}

	result, err := profile.Render(dir)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(result.Written) != 1 || result.Written[0] != "q15-web.container" {
		t.Fatalf("wrote %v, want only the unit that changed", result.Written)
	}

	raw, err := os.ReadFile(stale)
	if err != nil {
		t.Fatalf("read the restored unit: %v", err)
	}
	if !strings.Contains(string(raw), "ghcr.io/q15co/q15-web:"+testRelease) {
		t.Fatalf("the stale unit was not restored: %q", raw)
	}

	kept, err := os.ReadFile(foreign)
	if err != nil {
		t.Fatalf("read the foreign unit: %v", err)
	}
	if string(kept) != "# not mine\n" {
		t.Fatalf("a foreign unit was rewritten: %q", kept)
	}
}

// TestRenderRejectsAnEmptyDirectory: the caller decides where units go, and a
// missing answer is a mistake rather than a default.
func TestRenderRejectsAnEmptyDirectory(t *testing.T) {
	if _, err := testProfile().Render(""); err == nil {
		t.Fatal("Render(\"\") error = nil, want a rejection")
	}
}

// TestReloadAsksSystemdToRegenerate is the only step of a render that touches
// the host: the Quadlet generator runs during daemon-reload, and nothing else
// here needs podman or systemd.
func TestReloadAsksSystemdToRegenerate(t *testing.T) {
	runner := &recordingRunner{}

	if err := Reload(context.Background(), runner); err != nil {
		t.Fatalf("Reload() error = %v", err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("ran %v, want exactly one command", runner.calls)
	}
	if got := runner.calls[0].String(); got != "systemctl --user daemon-reload" {
		t.Fatalf("ran %q, want %q", got, "systemctl --user daemon-reload")
	}
}

// TestReloadRefusesWithoutARunner keeps a nil seam from reaching the process.
func TestReloadRefusesWithoutARunner(t *testing.T) {
	if err := Reload(context.Background(), nil); err == nil {
		t.Fatal("Reload(nil) error = nil, want a rejection")
	}
}

// TestReloadReportsAFailedReload carries the reason up: "the units were written
// but systemd never saw them" is the failure an operator has to be told.
func TestReloadReportsAFailedReload(t *testing.T) {
	cause := errors.New("Failed to connect to bus")
	err := Reload(context.Background(), &recordingRunner{err: cause})
	if err == nil {
		t.Fatal("Reload() error = nil, want the failure reported")
	}
	if !errors.Is(err, cause) {
		t.Fatalf("Reload() error = %v, want it to wrap %v", err, cause)
	}
}

// recordingRunner stands in for the process, so no test here needs systemd.
type recordingRunner struct {
	calls []runnerCall
	err   error
}

// runnerCall is one recorded command.
type runnerCall struct {
	name string
	args []string
}

// String renders the call the way an operator would read it back.
func (c runnerCall) String() string {
	return strings.TrimSpace(c.name + " " + strings.Join(c.args, " "))
}

func (r *recordingRunner) Run(_ context.Context, name string, args ...string) error {
	r.calls = append(r.calls, runnerCall{name: name, args: args})
	return r.err
}
