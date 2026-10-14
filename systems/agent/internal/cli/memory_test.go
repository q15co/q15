package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/command"
	"github.com/q15co/q15/systems/agent/internal/memoryrepo"
)

// TestMemoryWithoutCommandPrintsUsage checks the bare group: like the secret
// verbs, asking for the memory surface without a subcommand is a request for
// its usage, and that usage names the one flag every memory report needs.
func TestMemoryWithoutCommandPrintsUsage(t *testing.T) {
	env, stdout, _ := newEnv(&fakeExec{})

	if err := Run(context.Background(), env, []string{"memory"}); err != nil {
		t.Fatalf("Run(memory) error = %v", err)
	}
	if !strings.Contains(stdout.String(), "q15 memory status") {
		t.Fatalf("stdout = %q, want the memory usage", stdout.String())
	}
}

// TestMemoryUnknownCommandIsAUsageError pins the shape error for a word the
// memory surface does not handle.
func TestMemoryUnknownCommandIsAUsageError(t *testing.T) {
	env, stdout, _ := newEnv(&fakeExec{})

	err := Run(context.Background(), env, []string{"memory", "bogus"})
	if err == nil {
		t.Fatal("Run(memory bogus) error = nil, want a usage error")
	}
	if got := command.ExitCode(err); got != 2 {
		t.Fatalf("ExitCode(error) = %d, want 2", got)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}

// TestMemoryStatusRequiresMemoryRoot is the one thing an operator must get
// right: the verb refuses to guess a podman-internal path, and its usage error
// says what --memory-root names.
func TestMemoryStatusRequiresMemoryRoot(t *testing.T) {
	env, stdout, _ := newEnv(&fakeExec{})

	err := Run(context.Background(), env, []string{"memory", "status"})
	if err == nil {
		t.Fatal("Run(memory status) error = nil, want a usage error")
	}
	if got := command.ExitCode(err); got != 2 {
		t.Fatalf("ExitCode(error) = %d, want 2", got)
	}
	if !strings.Contains(err.Error(), "--memory-root") {
		t.Fatalf("error = %q, want it to name the flag", err.Error())
	}
	if !strings.Contains(err.Error(), "memory volume") {
		t.Fatalf("error = %q, want it to say what the directory is", err.Error())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}

// TestMemoryStatusUnknownFlagIsAUsageError covers the flag parser: an unknown
// flag is a shape mistake like everywhere else in this command line.
func TestMemoryStatusUnknownFlagIsAUsageError(t *testing.T) {
	env, _, stderr := newEnv(&fakeExec{})

	err := Run(context.Background(), env, []string{"memory", "status", "--bogus"})
	if err == nil {
		t.Fatal("Run(memory status --bogus) error = nil, want a usage error")
	}
	if got := command.ExitCode(err); got != 2 {
		t.Fatalf("ExitCode(error) = %d, want 2", got)
	}
	if !strings.Contains(stderr.String(), "bogus") {
		t.Fatalf("stderr = %q, want the flag named", stderr.String())
	}
}

// TestMemoryStatusRejectsArguments pins the no-arguments shape of the verb:
// everything it reports comes from --memory-root and the flags.
func TestMemoryStatusRejectsArguments(t *testing.T) {
	env, _, _ := newEnv(&fakeExec{})

	err := Run(
		context.Background(),
		env,
		[]string{"memory", "status", "extra"},
	)
	if err == nil {
		t.Fatal("Run(memory status extra) error = nil, want a usage error")
	}
	if got := command.ExitCode(err); got != 2 {
		t.Fatalf("ExitCode(error) = %d, want 2", got)
	}
	if !strings.Contains(err.Error(), "takes no arguments") {
		t.Fatalf("error = %q, want it to reject the argument", err.Error())
	}
}

// TestMemoryStatusNamesAMissingRoot is the common typo: a path that is not
// there must read as a wrong --memory-root, not as a stack trace.
func TestMemoryStatusNamesAMissingRoot(t *testing.T) {
	env, stdout, _ := newEnv(&fakeExec{})
	root := filepath.Join(t.TempDir(), "absent")

	err := Run(
		context.Background(),
		env,
		[]string{"memory", "status", "--memory-root", root},
	)
	if err == nil {
		t.Fatal("Run(memory status --memory-root absent) error = nil, want a failure")
	}
	if got := command.ExitCode(err); got != 1 {
		t.Fatalf("ExitCode(error) = %d, want 1", got)
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("error = %q, want it to name the missing root", err.Error())
	}
	if !strings.Contains(err.Error(), root) {
		t.Fatalf("error = %q, want it to echo the path", err.Error())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}

// TestMemoryStatusReportsABareRoot is the thin success case: stdout carries a
// report drawn from the memory root, stderr stays quiet, and the verb runs
// nothing outside the process — no podman, no systemctl, no git.
func TestMemoryStatusReportsABareRoot(t *testing.T) {
	// A default state directory is resolved even though the report never reads
	// it, so point it at the test's own tree rather than the host's.
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	fake := &fakeExec{}
	env, stdout, stderr := newEnv(fake)

	stateDir := filepath.Join("history", "state")
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, stateDir), 0o755); err != nil {
		t.Fatalf("create head state dir: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(root, stateDir, "head.json"),
		[]byte(`{"last_seq":12,"updated_at":"2026-10-13T09:10:33Z"}`),
		0o644,
	); err != nil {
		t.Fatalf("write head state: %v", err)
	}

	if err := Run(
		context.Background(),
		env,
		[]string{"memory", "status", "--memory-root", root},
	); err != nil {
		t.Fatalf("Run(memory status) error = %v", err)
	}
	if !strings.Contains(stdout.String(), "head seq 12") {
		t.Fatalf("stdout = %q, want the head reported", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	if len(fake.calls) != 0 {
		t.Fatalf("calls = %v, want the verb to run nothing", fake.calls)
	}
}

// TestMemoryMigrateNeedsExactlyOneAction pins the verb's shape: --start or
// --status, never neither and never both, and --force only ever alongside
// --start.
func TestMemoryMigrateNeedsExactlyOneAction(t *testing.T) {
	env, _, _ := newEnv(&fakeExec{})

	cases := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "neither action",
			args: []string{"memory", "migrate", "--memory-root", t.TempDir()},
			want: "exactly one of --start or --status",
		},
		{
			name: "both actions",
			args: []string{
				"memory", "migrate", "--memory-root", t.TempDir(),
				"--start", "--status",
			},
			want: "exactly one of --start or --status",
		},
		{
			name: "force alone",
			args: []string{
				"memory", "migrate", "--memory-root", t.TempDir(),
				"--force",
			},
			want: "--force only makes sense with --start",
		},
		{
			name: "no action, arguments",
			args: []string{"memory", "migrate", "extra"},
			want: "takes no arguments",
		},
	}
	for _, tc := range cases {
		err := Run(context.Background(), env, tc.args)
		if err == nil {
			t.Fatalf("Run(%v) error = nil, want a usage error", tc.args)
		}
		if got := command.ExitCode(err); got != 2 {
			t.Fatalf("ExitCode(%q error) = %d, want 2", tc.name, got)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("error = %q, want it to say %q", err.Error(), tc.want)
		}
	}
}

// newMigrateRoot prepares a memory root the migrate tests can drive: a git
// memory repository with the three semantic sources on disk and their byte
// sizes as the test names them.
func newMigrateRoot(t *testing.T, sizes map[string]int64) string {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	root := t.TempDir()
	if err := memoryrepo.NewGitCommitter().EnsureRepo(
		context.Background(),
		root,
	); err != nil {
		t.Fatalf("EnsureRepo(memory root) error = %v", err)
	}
	for name, size := range sizes {
		if size < 0 {
			t.Fatalf("size for %s is negative", name)
		}
		if err := os.MkdirAll(
			filepath.Join(root, filepath.Dir(name)),
			0o755,
		); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", name, err)
		}
		if err := os.WriteFile(
			filepath.Join(root, filepath.FromSlash(name)),
			bytes.Repeat([]byte("x"), int(size)),
			0o644,
		); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", name, err)
		}
	}
	return root
}

// readMigrateCursor reads back the cursor artifact the verb persisted.
func readMigrateCursor(t *testing.T, root string) map[string]map[string]any {
	t.Helper()
	path := filepath.Join(
		root,
		"cognition", "state", "semantic_migration.json",
	)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(cursor artifact) error = %v", err)
	}
	var parsed struct {
		Version int
		Files   map[string]map[string]any
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("Unmarshal(cursor artifact) error = %v", err)
	}
	if parsed.Version != 1 {
		t.Fatalf("cursor version = %d, want 1", parsed.Version)
	}
	return parsed.Files
}

// writeMigrateCursor writes a cursor artifact with the per-file progress the
// report tests read back.
func writeMigrateCursor(t *testing.T, root string, content string) {
	t.Helper()
	path := filepath.Join(
		root,
		"cognition", "state", "semantic_migration.json",
	)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(cognition state) error = %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(cursor artifact) error = %v", err)
	}
}

// TestMemoryMigrateStatusWithoutCursorSaysSo is the state, not an error: with
// no cursor there is no running migration, and the verb says exactly that and
// exits zero.
func TestMemoryMigrateStatusWithoutCursorSaysSo(t *testing.T) {
	root := t.TempDir()
	env, stdout, stderr := newEnv(&fakeExec{})

	if err := Run(
		context.Background(),
		env,
		[]string{"memory", "migrate", "--memory-root", root, "--status"},
	); err != nil {
		t.Fatalf("Run(migrate --status) error = %v", err)
	}
	if got, want := stdout.String(), "semantic migration: not running (no cursor)\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

// TestMemoryMigrateStatusReportsFileProgress is the golden report: per
// tracked source file the offset reached, the file's current size, the
// percentage that is, and whether the file finished; then whether the
// migration as a whole is done.
func TestMemoryMigrateStatusReportsFileProgress(t *testing.T) {
	root := newMigrateRoot(t, map[string]int64{
		"semantic/facts.md":       2050,
		"semantic/preferences.md": 24,
		"semantic/projects.md":    10,
	})
	writeMigrateCursor(t, root, `{
  "version": 1,
  "files": {
    "semantic/facts.md":       {"offset": 2050, "finished": true},
    "semantic/preferences.md": {"offset": 12, "hash": "ab"},
    "semantic/projects.md":    {"offset": 0}
  }
}`)
	env, stdout, stderr := newEnv(&fakeExec{})

	if err := Run(
		context.Background(),
		env,
		[]string{"memory", "migrate", "--memory-root", root, "--status"},
	); err != nil {
		t.Fatalf("Run(migrate --status) error = %v", err)
	}
	want := "semantic migration: running, 1 of 3 files finished\n" +
		"  semantic/facts.md: offset 2050/2050 bytes, 100%, finished\n" +
		"  semantic/preferences.md: offset 12/24 bytes, 50%, not finished\n" +
		"  semantic/projects.md: offset 0/10 bytes, 0%, not finished\n" +
		"semantic migration: not done\n"
	if got := stdout.String(); got != want {
		t.Fatalf("stdout =\n%q\nwant\n%q", got, want)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

// TestMemoryMigrateStartIsIdempotent covers the start rule end to end: a
// start on an empty root creates the cursor over all three sources at offset
// zero with an empty hash, and a start on a running migration keeps its
// progress instead of resetting it.
func TestMemoryMigrateStartIsIdempotent(t *testing.T) {
	root := newMigrateRoot(t, map[string]int64{
		"semantic/facts.md":       40,
		"semantic/preferences.md": 30,
		"semantic/projects.md":    20,
	})
	env, stdout, _ := newEnv(&fakeExec{})

	if err := Run(
		context.Background(),
		env,
		[]string{"memory", "migrate", "--memory-root", root, "--start"},
	); err != nil {
		t.Fatalf("Run(migrate --start) error = %v", err)
	}
	want := "semantic migration: started\n" +
		"semantic migration: running, 0 of 3 files finished\n" +
		"  semantic/facts.md: offset 0/40 bytes, 0%, not finished\n" +
		"  semantic/preferences.md: offset 0/30 bytes, 0%, not finished\n" +
		"  semantic/projects.md: offset 0/20 bytes, 0%, not finished\n" +
		"semantic migration: not done\n"
	if got := stdout.String(); got != want {
		t.Fatalf("stdout =\n%q\nwant\n%q", got, want)
	}
	files := readMigrateCursor(t, root)
	if len(files) != 3 {
		t.Fatalf("cursor tracks %d files: %v", len(files), files)
	}
	for _, name := range []string{
		"semantic/facts.md",
		"semantic/preferences.md",
		"semantic/projects.md",
	} {
		file := files[name]
		if file["offset"] != float64(0) {
			t.Fatalf("%s offset = %v, want 0", name, file["offset"])
		}
		if hash, ok := file["hash"]; ok && hash != "" {
			t.Fatalf("%s hash = %q, want an empty hash", name, hash)
		}
	}

	// An operator start on a running migration keeps every offset as it is.
	writeMigrateCursor(t, root, `{
  "version": 1,
  "files": {
    "semantic/facts.md":       {"offset": 12, "hash": "ab"},
    "semantic/preferences.md": {"offset": 0},
    "semantic/projects.md":    {"offset": 0}
  }
}`)
	env, stdout, _ = newEnv(&fakeExec{})
	if err := Run(
		context.Background(),
		env,
		[]string{"memory", "migrate", "--memory-root", root, "--start"},
	); err != nil {
		t.Fatalf("Run(second migrate --start) error = %v", err)
	}
	want = "semantic migration: already running, progress kept\n" +
		"semantic migration: running, 0 of 3 files finished\n" +
		"  semantic/facts.md: offset 12/40 bytes, 30%, not finished\n" +
		"  semantic/preferences.md: offset 0/30 bytes, 0%, not finished\n" +
		"  semantic/projects.md: offset 0/20 bytes, 0%, not finished\n" +
		"semantic migration: not done\n"
	if got := stdout.String(); got != want {
		t.Fatalf("stdout =\n%q\nwant\n%q", got, want)
	}
	if file := readMigrateCursor(t, root)["semantic/facts.md"]; file["offset"] != float64(12) {
		t.Fatalf("facts offset after restart = %v, want 12 kept", file["offset"])
	}
}

// TestMemoryMigrateStartOnFinishedMigration pins the finished rule: a plain
// start against a cursor whose files all say finished reports that and does
// not redo the work; only --force restarts every file from offset zero.
func TestMemoryMigrateStartOnFinishedMigration(t *testing.T) {
	root := newMigrateRoot(t, map[string]int64{
		"semantic/facts.md":       30,
		"semantic/preferences.md": 30,
		"semantic/projects.md":    30,
	})
	writeMigrateCursor(t, root, `{
  "version": 1,
  "files": {
    "semantic/facts.md":       {"offset": 30, "finished": true},
    "semantic/preferences.md": {"offset": 30, "finished": true},
    "semantic/projects.md":    {"offset": 30, "finished": true}
  }
}`)
	env, stdout, _ := newEnv(&fakeExec{})

	if err := Run(
		context.Background(),
		env,
		[]string{"memory", "migrate", "--memory-root", root, "--start"},
	); err != nil {
		t.Fatalf("Run(migrate --start on finished) error = %v", err)
	}
	want := "semantic migration: already finished; pass --force to start again from offset 0\n" +
		"semantic migration: finished, 3 of 3 files drained\n" +
		"  semantic/facts.md: offset 30/30 bytes, 100%, finished\n" +
		"  semantic/preferences.md: offset 30/30 bytes, 100%, finished\n" +
		"  semantic/projects.md: offset 30/30 bytes, 100%, finished\n" +
		"semantic migration: done\n"
	if got := stdout.String(); got != want {
		t.Fatalf("stdout =\n%q\nwant\n%q", got, want)
	}
	if len(readMigrateCursor(t, root)) != 3 {
		t.Fatalf("finished cursor was touched, want it intact")
	}

	// --force says so and starts from zero.
	env, stdout, _ = newEnv(&fakeExec{})
	if err := Run(
		context.Background(),
		env,
		[]string{
			"memory", "migrate", "--memory-root", root, "--start", "--force",
		},
	); err != nil {
		t.Fatalf("Run(migrate --start --force) error = %v", err)
	}
	want = "semantic migration: started\n" +
		"semantic migration: running, 0 of 3 files finished\n" +
		"  semantic/facts.md: offset 0/30 bytes, 0%, not finished\n" +
		"  semantic/preferences.md: offset 0/30 bytes, 0%, not finished\n" +
		"  semantic/projects.md: offset 0/30 bytes, 0%, not finished\n" +
		"semantic migration: not done\n"
	if got := stdout.String(); got != want {
		t.Fatalf("stdout =\n%q\nwant\n%q", got, want)
	}
	files := readMigrateCursor(t, root)
	for name, file := range files {
		if file["offset"] != float64(0) || file["finished"] == true {
			t.Fatalf("%s entry = %v after --force, want a fresh offset-0 start", name, file)
		}
	}
}
