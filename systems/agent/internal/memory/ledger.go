package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/q15co/q15/systems/agent/internal/atomicfile"
)

const (
	ledgerDirPath               = workingDirPath + "/ledger"
	ledgerFileName              = "LEDGER.md"
	workingLedgerRelativePath   = workingDirPath + "/" + ledgerFileName
	ledgerSeedPath              = seedDirPath + "/" + ledgerFileName
	ledgerRotationThresholdByte = 256 * 1024
)

// ensureWorkingLedger creates the empty conversion ledger with its one-line
// format header. It is a no-op when the ledger already exists.
func (s *Store) ensureWorkingLedger() error {
	if err := s.ensureSeedFile(workingLedgerRelativePath, ledgerSeedPath); err != nil {
		return fmt.Errorf("initialize ledger seed %q: %w", ledgerSeedPath, err)
	}
	return nil
}

// ledgerRotationTarget returns the archive path the current ledger rotates to:
// working/ledger/YYYY-Q.md for the quarter the ledger is rotated in.
//
// It is separated from rotateWorkingLedger so the naming rule is testable
// without a fixture on disk.
func (s *Store) ledgerRotationTarget(now time.Time) string {
	quarter := (int(now.UTC().Month())-1)/3 + 1
	name := fmt.Sprintf("%d-Q%d.md", now.UTC().Year(), quarter)
	return filepath.Join(s.root(), ledgerDirPath, name)
}

// rotateWorkingLedger moves the current ledger aside when it has grown past
// the rotation threshold and starts a fresh file in its place.
//
// Threshold check only: the ledger survives an append-only commit discipline,
// so a size test is all the trigger needs. The ledger keeps its header after a
// rotation, so a rotated-and-recreated file is identical to a freshly
// initialized one.
func (s *Store) rotateWorkingLedger(now time.Time) error {
	current := filepath.Join(s.root(), filepath.FromSlash(workingLedgerRelativePath))
	info, err := os.Stat(current)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat ledger file %q: %w", current, err)
	}
	if info.Size() <= ledgerRotationThresholdByte {
		return nil
	}

	target := s.ledgerRotationTarget(now)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("create ledger archive dir: %w", err)
	}

	// Read before truncating: writeSeedLedger replaces the current file, so the
	// raw bytes must be captured first. Renaming would be cheaper, but the
	// archive path is quarter-based and a second rotation in the same quarter
	// must not clobber the first archive.
	raw, err := os.ReadFile(current)
	if err != nil {
		return fmt.Errorf("read ledger file %q: %w", current, err)
	}
	existing, err := os.ReadFile(target)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read ledger archive %q: %w", target, err)
	}
	combined := append(raw, existing...)
	if err := atomicfile.WriteBytes(target, combined); err != nil {
		return fmt.Errorf("write ledger archive %q: %w", target, err)
	}

	return s.writeSeedLedger(current)
}

// writeSeedLedger writes the seeded ledger header to path, replacing whatever
// is there.
func (s *Store) writeSeedLedger(path string) error {
	content, err := readEmbeddedSeed(ledgerSeedPath)
	if err != nil {
		return fmt.Errorf("read embedded seed %q: %w", ledgerSeedPath, err)
	}
	if err := writeTextFileAtomic(path, content+"\n"); err != nil {
		return fmt.Errorf("write ledger file %q: %w", path, err)
	}
	return nil
}

// ReadWorkingLedger returns the current ledger text, for callers that want it
// (the rotation tests, and later the status command) without re-deriving the
// path.
func (s *Store) ReadWorkingLedger() (string, error) {
	path := filepath.Join(s.root(), filepath.FromSlash(workingLedgerRelativePath))
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read ledger file %q: %w", path, err)
	}
	return strings.TrimSpace(string(raw)), nil
}
