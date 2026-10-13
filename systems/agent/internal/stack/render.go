package stack

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/q15co/q15/systems/agent/internal/atomicfile"
)

// unitFileMode is the mode a rendered unit is left with. Unit files are
// configuration, not secrets: they name the images and paths of a deployment a
// person may want to read, and systemd's own user units are 0644.
const unitFileMode = 0o644

// Runner runs one command. It is the process seam: the CLI passes its own and a
// test passes a recorder, so neither rendering nor reloading needs podman or
// systemd to be exercised.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) error
}

// Result reports what a render changed. A person running the same command twice
// should be able to see that the second run did nothing, and the CLI's own
// output is built from this rather than from a guess.
type Result struct {
	// Dir is the directory the units were rendered into.
	Dir string
	// Written are the unit names whose contents changed, sorted. A unit that did
	// not exist before counts as written.
	Written []string
	// Unchanged are the unit names already on disk with these exact contents,
	// sorted.
	Unchanged []string
}

// Changed reports whether the render had anything to do.
func (r Result) Changed() bool { return len(r.Written) > 0 }

// Render writes the stack's units into dir, replacing only the files whose
// contents differ. Running it twice leaves the directory byte-identical and does
// not touch a file systemd or a person may be watching; a file that belongs to
// somebody else, or to another stack, is left alone rather than cleaned up.
func (p Profile) Render(dir string) (Result, error) {
	if dir == "" {
		return Result{}, errors.New("stack: a units directory is required")
	}
	units, err := p.Units()
	if err != nil {
		return Result{}, err
	}

	result := Result{Dir: dir}
	for _, unit := range units {
		path := filepath.Join(dir, unit.Name)
		changed, err := writeIfChanged(path, unit.Contents)
		if err != nil {
			return Result{}, fmt.Errorf("render %s: %w", unit.Name, err)
		}
		if changed {
			result.Written = append(result.Written, unit.Name)
			continue
		}
		result.Unchanged = append(result.Unchanged, unit.Name)
	}

	// A render that wrote nothing has nothing to sync: syncing anyway would
	// report a change the directory did not have.
	if result.Changed() {
		if err := atomicfile.SyncDirectory(dir); err != nil {
			return Result{}, fmt.Errorf("sync %s: %w", dir, err)
		}
	}
	return result, nil
}

// writeIfChanged writes contents to path unless the file already holds exactly
// that, and reports whether it wrote.
func writeIfChanged(path string, contents []byte) (bool, error) {
	existing, err := os.ReadFile(path)
	switch {
	case err == nil && bytes.Equal(existing, contents):
		return false, nil
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return false, fmt.Errorf("read %s: %w", path, err)
	}

	if err := atomicfile.WriteBytes(path, contents); err != nil {
		return false, err
	}
	// atomicfile writes through a temporary file, which lands 0600: right for a
	// credential, wrong for a unit file a person may read.
	if err := os.Chmod(path, unitFileMode); err != nil {
		return false, fmt.Errorf("chmod %s: %w", path, err)
	}
	return true, nil
}

// Reload asks the user's systemd to regenerate its units. The Quadlet generator
// runs during daemon-reload, so this is what turns rendered files into units
// systemd can start.
func Reload(ctx context.Context, runner Runner) error {
	if runner == nil {
		return errors.New("stack: a command runner is required")
	}
	if err := runner.Run(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
		return fmt.Errorf("reload the user's systemd: %w", err)
	}
	return nil
}
