package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"github.com/q15co/q15/systems/agent/internal/cognition"
	"github.com/q15co/q15/systems/agent/internal/command"
	"github.com/q15co/q15/systems/agent/internal/config"
	"github.com/q15co/q15/systems/agent/internal/memory"
	"github.com/q15co/q15/systems/agent/internal/memoryrepo"
)

// runMemory is the read-only memory surface: reports drawn from the agent's
// memory files on this host.
//
// Memory is files, so anything that can read the directory can report on it,
// with no running agent and no podman. Nothing here writes into the root: the
// verbs read through the store's load methods and never call Store.Init. The
// one exception is memory migrate --start, which writes only the migration
// cursor, the artifact the migration job resumes from.
//
// The reports judge file sizes against the configuration defaults rather than
// a loaded configuration. The rendered agent configuration's host path is an
// internal detail of stack rendering (the install slice that writes it has
// not landed), and resolving a loaded configuration would drag in the
// container's environment indirections, which an operator's shell does not
// carry. The fallback is the same defaults config resolution applies, and
// every sized line of the report prints the limit it was judged against, so a
// fallback is never silent.
func runMemory(ctx context.Context, env command.Env, args []string) error {
	if len(args) == 0 || asksForHelp(args[0]) {
		writeMemoryUsage(env.Stdout)
		return nil
	}

	switch args[0] {
	case "status":
		return runMemoryStatus(ctx, env, args[1:])
	case "migrate":
		return runMemoryMigrate(ctx, env, args[1:])
	default:
		return command.Usagef("unknown memory command %q", args[0])
	}
}

// writeMemoryUsage prints what the memory surface can do, and the one flag an
// operator has to name correctly before any of it is readable.
func writeMemoryUsage(w io.Writer) {
	fmt.Fprint(w, `q15 memory reports on the agent's memory files on this host.

Read-only surface: nothing here writes to the memory root, and no part of the
report needs the stack to run. The one exception is memory migrate --start,
which writes only the migration cursor.

usage:
  q15 memory status --memory-root PATH
                                head seq, checkpoint lag and age, cognition job
                                failures, file sizes against their budgets and
                                caps, and the conversion ledger
  q15 memory migrate --memory-root PATH (--start | --status)
                                the supervised semantic migration's cursor:
                                --start creates or resumes it, --status reports
                                byte offsets, sizes and progress per source file

--memory-root is required: the agent's /memory directory,
the stack's memory volume, on this host. --stack and
--state-dir name the agent the way the other verbs do; the report still comes
straight from the memory root.
`)
}

// memoryFlags are the flags a memory verb takes: the stack flags every verb
// shares, and the memory root the report reads.
type memoryFlags struct {
	stackFlags
	memoryRoot string
}

// newMemoryFlagSet returns the flag set for one memory verb. The memory root
// has no usable default: a stack's memory volume is not derivable from the
// stack's state directory, and guessing a podman-internal path is exactly the
// guess this verb refuses to make.
func newMemoryFlagSet(name string, env command.Env, flags *memoryFlags) *flag.FlagSet {
	fs := newStackFlagSet(name, env, &flags.stackFlags)
	fs.StringVar(
		&flags.memoryRoot,
		"memory-root",
		"",
		"the agent's /memory directory, the stack's memory volume, on this host (required)",
	)
	return fs
}

// defaultStatusLimits returns the byte budgets the report judges sizes
// against; see the note on runMemory for why they are the defaults.
func defaultStatusLimits() memory.StatusLimits {
	return memory.StatusLimits{
		WorkingMemoryBudgetBytes: config.DefaultMemoryWorkingBudgetBytes,
		OpenLoopsBudgetBytes:     config.DefaultMemoryOpenLoopsBudgetBytes,
	}
}

// runMemoryStatus reports one memory root: head, checkpoint lag, job failures,
// file sizes and the ledger. stdout carries the report and nothing else, and
// the process does not fail because a report says something is wrong: a
// stalled conversion is data, and whoever reads the report reacts to it.
func runMemoryStatus(ctx context.Context, env command.Env, args []string) error {
	flags := memoryFlags{}
	fs := newMemoryFlagSet("memory status", env, &flags)
	if err := fs.Parse(args); err != nil {
		return command.AsUsage(err)
	}
	if fs.NArg() > 0 {
		return command.Usagef("memory status takes no arguments, got %q", fs.Arg(0))
	}
	if flags.memoryRoot == "" {
		return command.Usagef(
			"memory status needs --memory-root: the agent's /memory directory, the stack's memory volume, on this host",
		)
	}

	info, err := os.Stat(flags.memoryRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf(
				"memory status: --memory-root %s does not exist: it is the agent's /memory directory, the stack's memory volume, on this host",
				flags.memoryRoot,
			)
		}
		return fmt.Errorf("memory status: read --memory-root %s: %w", flags.memoryRoot, err)
	}
	if !info.IsDir() {
		return fmt.Errorf(
			"memory status: --memory-root %s is a file, not the memory directory",
			flags.memoryRoot,
		)
	}

	profile, err := flags.profile(env.Release)
	if err != nil {
		return err
	}

	store := memory.NewStore(memoryrepo.New(flags.memoryRoot, nil), profile.AgentName)
	status, err := memory.CollectStatus(ctx, store, defaultStatusLimits(), time.Now())
	if err != nil {
		return fmt.Errorf("memory status: %w", err)
	}
	return memory.RenderStatus(env.Stdout, status)
}

// memoryMigrateFlags are the flags the migrate verb takes: the flags status
// takes, plus exactly one action flag and the force override.
type memoryMigrateFlags struct {
	memoryFlags
	start bool
	force bool
	view  bool
}

// runMemoryMigrate drives the supervised semantic migration's cursor from an
// operator's shell. --start creates the cursor covering every source file and
// is idempotent: starting again while a migration is running keeps its
// progress, and starting against a cursor whose files have all finished says
// so instead of redoing the work unless --force asks for a fresh start.
// --status reports the cursor without writing anything.
//
// The verb drives the migration job's own cursor through the artifact store
// and writes nothing else; the job remains the only other writer of the
// cursor.
func runMemoryMigrate(ctx context.Context, env command.Env, args []string) error {
	flags := memoryMigrateFlags{}
	fs := newMemoryFlagSet("memory migrate", env, &flags.memoryFlags)
	fs.BoolVar(&flags.start, "start", false, "create or resume the migration cursor")
	fs.BoolVar(
		&flags.force,
		"force",
		false,
		"with --start: restart every file from offset 0 even though the cursor says it already finished",
	)
	fs.BoolVar(&flags.view, "status", false, "report the migration cursor's progress")
	if err := fs.Parse(args); err != nil {
		return command.AsUsage(err)
	}
	if fs.NArg() > 0 {
		return command.Usagef("memory migrate takes no arguments, got %q", fs.Arg(0))
	}
	if flags.force && !flags.start {
		return command.Usagef("memory migrate: --force only makes sense with --start")
	}
	if flags.start == flags.view {
		return command.Usagef("memory migrate takes exactly one of --start or --status")
	}
	if flags.memoryRoot == "" {
		return command.Usagef(
			"memory migrate needs --memory-root: the agent's /memory directory, the stack's memory volume, on this host",
		)
	}

	info, err := os.Stat(flags.memoryRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf(
				"memory migrate: --memory-root %s does not exist: it is the agent's /memory directory, the stack's memory volume, on this host",
				flags.memoryRoot,
			)
		}
		return fmt.Errorf("memory migrate: read --memory-root %s: %w", flags.memoryRoot, err)
	}
	if !info.IsDir() {
		return fmt.Errorf(
			"memory migrate: --memory-root %s is a file, not the memory directory",
			flags.memoryRoot,
		)
	}

	profile, err := flags.profile(env.Release)
	if err != nil {
		return err
	}

	store := memory.NewStore(memoryrepo.New(flags.memoryRoot, nil), profile.AgentName)
	cursor, err := cognition.LoadSemanticMigrationCursor(ctx, store)
	if err != nil {
		return fmt.Errorf("memory migrate: %w", err)
	}
	if flags.start {
		return runMemoryMigrateStart(ctx, env, store, cursor, flags.force)
	}
	return renderMigrationCursor(ctx, env.Stdout, store, cursor)
}

// runMemoryMigrateStart creates the cursor when none exists, keeps an
// unfinished migration's progress when it is already running, and refuses to
// rewrite a finished migration unless force asks for a fresh start.
func runMemoryMigrateStart(
	ctx context.Context,
	env command.Env,
	store *memory.Store,
	cursor cognition.SemanticMigrationCursor,
	force bool,
) error {
	if len(cursor.Files) > 0 && cognition.SemanticMigrationCursorHasOpenFile(cursor) {
		fmt.Fprintln(env.Stdout, "semantic migration: already running, progress kept")
		return renderMigrationCursor(ctx, env.Stdout, store, cursor)
	}
	if len(cursor.Files) > 0 && !force {
		fmt.Fprintln(
			env.Stdout,
			"semantic migration: already finished; pass --force to start again from offset 0",
		)
		return renderMigrationCursor(ctx, env.Stdout, store, cursor)
	}

	cursor = cognition.SemanticMigrationCursor{
		Files: map[string]cognition.SemanticMigrationFileCursor{},
	}
	for _, path := range cognition.SemanticMigrationSourcePaths() {
		cursor.Files[path] = cognition.SemanticMigrationFileCursor{}
	}
	// A working/archive/ directory holding at least one .md copy is folded
	// into this migration: the residue of lines not already in working memory
	// is staged as the cursor's last source. After a completed fold the
	// archive is gone, so a second --start stages nothing new.
	working, err := store.LoadWorkingMemory(ctx)
	if err != nil {
		return fmt.Errorf("memory migrate: %w", err)
	}
	summary, staged, err := store.StageArchiveResidue(ctx, working.Content)
	if err != nil {
		return fmt.Errorf("memory migrate: %w", err)
	}
	if staged {
		cursor.Files[cognition.SemanticMigrationResidueSourcePath] =
			cognition.SemanticMigrationFileCursor{}
		fmt.Fprintf(
			env.Stdout,
			"semantic migration: staged archive fold residue (%d files folded, %d residue lines)\n",
			summary.FilesFolded,
			summary.ResidueLines,
		)
	}
	if err := cognition.SaveSemanticMigrationCursor(ctx, store, cursor); err != nil {
		return fmt.Errorf("memory migrate: %w", err)
	}
	fmt.Fprintln(env.Stdout, "semantic migration: started")
	return renderMigrationCursor(ctx, env.Stdout, store, cursor)
}

// renderMigrationCursor prints one cursor: per tracked source file the byte
// offset reached, the file's current size, the percentage that offset is, and
// whether the file is finished; then whether the migration as a whole is
// done. A missing cursor is a state a report describes, not an error.
func renderMigrationCursor(
	ctx context.Context,
	w io.Writer,
	store *memory.Store,
	cursor cognition.SemanticMigrationCursor,
) error {
	if len(cursor.Files) == 0 {
		fmt.Fprintln(w, "semantic migration: not running (no cursor)")
		return nil
	}

	sizes, err := store.LoadSemanticFileSizes(ctx)
	if err != nil {
		return fmt.Errorf("memory migrate: load source sizes: %w", err)
	}

	paths := make([]string, 0, len(cursor.Files))
	for path := range cursor.Files {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	finished := 0
	for _, path := range paths {
		if cursor.Files[path].Finished {
			finished++
		}
	}
	if finished == len(paths) {
		fmt.Fprintf(w, "semantic migration: finished, %d of %d files drained\n",
			finished,
			len(paths),
		)
	} else {
		fmt.Fprintf(w, "semantic migration: running, %d of %d files finished\n",
			finished,
			len(paths),
		)
	}
	for _, path := range paths {
		file := cursor.Files[path]
		size := int64(0)
		if stored, ok := sizes[path]; ok {
			size = stored
		}
		fmt.Fprintf(w, "  %s: offset %d/%d bytes, %s, %s\n",
			path,
			file.Offset,
			size,
			migrationPercent(file.Offset, size),
			migrationFinishedWord(file.Finished),
		)
	}
	if finished == len(paths) {
		fmt.Fprintln(w, "semantic migration: done")
		return nil
	}
	fmt.Fprintln(w, "semantic migration: not done")
	return nil
}

// migrationPercent renders the byte progress of one source file as a whole
// percentage, so an operator reads the cursor without doing arithmetic.
func migrationPercent(offset, size int64) string {
	if size <= 0 || offset <= 0 {
		return "0%"
	}
	if offset >= size {
		return "100%"
	}
	return fmt.Sprintf("%d%%", offset*100/size)
}

func migrationFinishedWord(finished bool) string {
	if finished {
		return "finished"
	}
	return "not finished"
}
