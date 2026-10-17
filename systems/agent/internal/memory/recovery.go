package memory

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/q15co/q15/systems/agent/internal/atomicfile"
	"github.com/q15co/q15/systems/agent/internal/cognition"
)

const memoryRecoveryPath = ".git/q15-memory-recovery.json"

var ledgerArchivePathPattern = regexp.MustCompile(`^working/ledger/[0-9]{4}-Q[1-4]\.md$`)

type memoryRecovery struct {
	Version           int                               `json:"version"`
	ID                string                            `json:"id"`
	Snapshots         map[string]cognition.FileSnapshot `json:"snapshots"`
	TopicDir          string                            `json:"topic_dir,omitempty"`
	RepairTarget      string                            `json:"repair_target,omitempty"`
	LedgerArchivePath string                            `json:"ledger_archive_path,omitempty"`
}

// BeginMemoryRecovery persists a rollback journal before tools can change the
// writable set. A pending journal is never replaced by a later run's baseline.
func (s *Store) BeginMemoryRecovery(
	ctx context.Context,
	snapshots map[string]cognition.FileSnapshot,
	topicDir string,
) (string, error) {
	return s.beginMemoryRecovery(ctx, snapshots, topicDir, "")
}

// BeginMemoryRepairRecovery adds exactly one surface-repair target to the
// conversion writable set. Other unnamed content files remain outside the journal.
func (s *Store) BeginMemoryRepairRecovery(
	ctx context.Context,
	snapshots map[string]cognition.FileSnapshot,
	target string,
) (string, error) {
	if !validRepairRecoveryTarget(target) {
		return "", fmt.Errorf("invalid memory repair recovery target %q", target)
	}
	return s.beginMemoryRecovery(ctx, snapshots, semanticTopicsRelativePath, target)
}

func (s *Store) beginMemoryRecovery(
	ctx context.Context,
	snapshots map[string]cognition.FileSnapshot,
	topicDir, repairTarget string,
) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	release := s.repository.Acquire()
	defer release()

	if _, exists, err := s.loadMemoryRecoveryLocked(); err != nil {
		return "", err
	} else if exists {
		return "", fmt.Errorf("memory recovery is pending; recover it before another run")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", fmt.Errorf("allocate memory recovery id: %w", err)
	}
	journal := memoryRecovery{
		Version: 1, ID: hex.EncodeToString(nonce[:]),
		Snapshots:    make(map[string]cognition.FileSnapshot, len(snapshots)),
		TopicDir:     topicDir,
		RepairTarget: repairTarget,
	}
	for runtimePath, snapshot := range snapshots {
		relative := strings.TrimPrefix(runtimePath, "/memory/")
		journal.Snapshots[relative] = snapshot
	}
	if err := journal.validate(); err != nil {
		return "", err
	}
	if err := s.validateRecoveryBaseline(journal); err != nil {
		return "", err
	}
	if err := s.writeMemoryRecoveryLocked(journal); err != nil {
		return "", err
	}
	return journal.ID, nil
}

// CompleteMemoryRecovery commits validated files before durably retiring their
// rollback journal. A failure leaves the journal available for recovery.
func (s *Store) CompleteMemoryRecovery(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	release := s.repository.Acquire()
	defer release()

	journal, exists, err := s.loadMemoryRecoveryLocked()
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("memory recovery journal is missing")
	}
	if journal.ID != id || id == "" {
		return fmt.Errorf("memory recovery id does not match the pending journal")
	}
	if err := s.rotateValidatedLedger(&journal, time.Now().UTC()); err != nil {
		return fmt.Errorf("rotate validated memory ledger: %w", err)
	}
	paths, err := s.memoryRecoveryPaths(journal)
	if err != nil {
		return err
	}
	if err := s.repository.Commit(ctx, "memory: apply validated cognition run", paths...); err != nil {
		return fmt.Errorf("commit validated memory run: %w", err)
	}
	return s.removeMemoryRecoveryLocked()
}

// RecoverMemory retries an interrupted run's recovery. An empty id recovers
// whichever journal is pending; startup uses it before loading any baseline.
func (s *Store) RecoverMemory(ctx context.Context, id string) error {
	release := s.repository.Acquire()
	defer release()
	return s.recoverMemoryLocked(ctx, id)
}

func (s *Store) recoverMemoryLocked(ctx context.Context, id string) error {
	journal, exists, err := s.loadMemoryRecoveryLocked()
	if err != nil || !exists {
		return err
	}
	if id != "" && journal.ID != id {
		return fmt.Errorf("memory recovery id does not match the pending journal")
	}
	paths, listErr := s.memoryRecoveryPaths(journal)
	var failures []error
	if listErr != nil {
		failures = append(failures, listErr)
	}
	var discovered []string
	for _, relative := range paths {
		if _, snapshotted := journal.Snapshots[relative]; !snapshotted {
			journal.Snapshots[relative] = cognition.FileSnapshot{}
			discovered = append(discovered, relative)
		}
	}
	if len(discovered) > 0 {
		// Remember new topics before deleting them: a Git failure must remain
		// retryable even after the directory entry has disappeared.
		if err := s.writeMemoryRecoveryLocked(journal); err != nil {
			failures = append(failures, err)
			for _, relative := range discovered {
				delete(journal.Snapshots, relative)
			}
			paths = sortedSnapshotPaths(journal.Snapshots)
		}
	}
	// Keep fixed restoration independent of topic enumeration: even an unreadable
	// directory must not prevent restoring working memory and its ledger.
	for _, relative := range sortedSnapshotPaths(journal.Snapshots) {
		snapshot := journal.Snapshots[relative]
		file := filepath.Join(s.root(), filepath.FromSlash(relative))
		var restoreErr error
		if snapshot.Existed {
			restoreErr = atomicfile.WriteBytes(file, snapshot.Content)
		} else {
			restoreErr = removeRecoveryFile(file)
		}
		if restoreErr != nil {
			failures = append(failures, fmt.Errorf("restore %s: %w", relative, restoreErr))
		}
	}
	// Reset a partially staged deletion before retrying its commit. CommitPaths
	// ignores unchanged, absent paths while still committing tracked deletions.
	if err := s.repository.ResetIndex(ctx, paths...); err != nil {
		failures = append(failures, fmt.Errorf("reset recovered memory index: %w", err))
	}
	if err := s.repository.Commit(ctx, "memory: recover interrupted cognition run", paths...); err != nil {
		failures = append(failures, fmt.Errorf("commit recovered memory: %w", err))
	}
	if err := errors.Join(failures...); err != nil {
		return err
	}
	return s.removeMemoryRecoveryLocked()
}

func (s *Store) memoryRecoveryFile() string {
	return filepath.Join(s.root(), filepath.FromSlash(memoryRecoveryPath))
}

func (s *Store) loadMemoryRecoveryLocked() (memoryRecovery, bool, error) {
	data, err := os.ReadFile(s.memoryRecoveryFile())
	if os.IsNotExist(err) {
		return memoryRecovery{}, false, nil
	}
	if err != nil {
		return memoryRecovery{}, false, fmt.Errorf("read memory recovery journal: %w", err)
	}
	var journal memoryRecovery
	if err := json.Unmarshal(data, &journal); err != nil {
		return memoryRecovery{}, false, fmt.Errorf("decode memory recovery journal: %w", err)
	}
	if err := journal.validate(); err != nil {
		return memoryRecovery{}, false, err
	}
	return journal, true, nil
}

func (journal memoryRecovery) validate() error {
	if journal.Version != 1 || journal.ID == "" || len(journal.Snapshots) == 0 {
		return fmt.Errorf("invalid memory recovery journal header")
	}
	if journal.TopicDir != "" && journal.TopicDir != semanticTopicsRelativePath {
		return fmt.Errorf("invalid memory recovery topic directory %q", journal.TopicDir)
	}
	if journal.RepairTarget != "" &&
		(journal.TopicDir != semanticTopicsRelativePath || !validRepairRecoveryTarget(journal.RepairTarget)) {
		return fmt.Errorf("invalid memory repair recovery target %q", journal.RepairTarget)
	}
	required := []string{"working/WORKING_MEMORY.md", "working/OPEN_LOOPS.md", "working/LEDGER.md"}
	if journal.LedgerArchivePath != "" {
		if !ledgerArchivePathPattern.MatchString(journal.LedgerArchivePath) {
			return fmt.Errorf(
				"invalid memory recovery ledger archive %q",
				journal.LedgerArchivePath,
			)
		}
		required = append(required, journal.LedgerArchivePath)
	}
	if journal.RepairTarget != "" {
		required = append(required, journal.RepairTarget)
	}
	if journal.TopicDir != "" {
		required = append(
			required,
			semanticFactsRelativePath,
			semanticPreferencesRelativePath,
			semanticProjectsRelativePath,
		)
	}
	for _, relative := range required {
		if _, present := journal.Snapshots[relative]; !present {
			return fmt.Errorf("memory recovery snapshot is missing %s", relative)
		}
	}
	for relative := range journal.Snapshots {
		clean, err := resolveMemoryFilePath(relative)
		if err != nil || clean != relative || !journal.ownsPath(relative) {
			return fmt.Errorf("invalid memory recovery path %q", relative)
		}
	}
	return nil
}

func validRepairRecoveryTarget(relative string) bool {
	clean, err := resolveMemoryFilePath(relative)
	if err != nil || clean != relative || relative == workingLedgerRelativePath ||
		strings.HasPrefix(relative, "working/ledger/") {
		return false
	}
	return strings.HasPrefix(relative, "semantic/") || strings.HasPrefix(relative, "working/")
}

func (journal memoryRecovery) ownsPath(relative string) bool {
	return relative == journal.RepairTarget || relative == journal.LedgerArchivePath ||
		recoverableMemoryPath(relative, journal.TopicDir)
}

func recoverableMemoryPath(relative, topicDir string) bool {
	switch relative {
	case workingDirPath + "/" + workingMemoryFileName,
		workingDirPath + "/" + openLoopsFileName,
		workingLedgerRelativePath:
		return true
	case "semantic/facts.md", "semantic/preferences.md", "semantic/projects.md":
		return topicDir == semanticTopicsRelativePath
	default:
		return topicDir != "" && path.Dir(relative) == topicDir
	}
}

func sortedSnapshotPaths(snapshots map[string]cognition.FileSnapshot) []string {
	paths := make([]string, 0, len(snapshots))
	for relative := range snapshots {
		paths = append(paths, relative)
	}
	sort.Strings(paths)
	return paths
}

func (s *Store) memoryRecoveryPaths(journal memoryRecovery) ([]string, error) {
	paths := sortedSnapshotPaths(journal.Snapshots)
	if journal.TopicDir == "" {
		return paths, nil
	}
	entries, err := os.ReadDir(filepath.Join(s.root(), filepath.FromSlash(journal.TopicDir)))
	if os.IsNotExist(err) {
		return paths, nil
	}
	if err != nil {
		return paths, fmt.Errorf("list memory recovery topics: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		relative := journal.TopicDir + "/" + entry.Name()
		if _, snapshotted := journal.Snapshots[relative]; !snapshotted {
			paths = append(paths, relative)
		}
	}
	return paths, nil
}

func removeRecoveryFile(file string) error {
	if err := atomicfile.Remove(file); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (s *Store) removeMemoryRecoveryLocked() error {
	if err := atomicfile.Remove(s.memoryRecoveryFile()); err != nil {
		return fmt.Errorf("retire memory recovery journal: %w", err)
	}
	return nil
}

func (s *Store) validateRecoveryBaseline(journal memoryRecovery) error {
	if journal.TopicDir != "" {
		if err := s.validateRecoveryPathAliases(journal.TopicDir); err != nil {
			return err
		}
	}
	for relative := range journal.Snapshots {
		if err := s.validateRecoveryPathAliases(relative); err != nil {
			return err
		}
	}
	paths, err := s.memoryRecoveryPaths(journal)
	if err != nil {
		return err
	}
	for _, relative := range paths {
		snapshot, present := journal.Snapshots[relative]
		if !present {
			return fmt.Errorf("memory recovery snapshot is missing existing topic %s", relative)
		}
		data, err := os.ReadFile(filepath.Join(s.root(), filepath.FromSlash(relative)))
		if os.IsNotExist(err) && !snapshot.Existed {
			continue
		}
		if err != nil {
			return fmt.Errorf("validate memory recovery baseline %s: %w", relative, err)
		}
		if !snapshot.Existed || !bytes.Equal(data, snapshot.Content) {
			return fmt.Errorf(
				"memory recovery baseline changed before journal creation: %s",
				relative,
			)
		}
	}
	return nil
}

func (s *Store) validateRecoveryPathAliases(relative string) error {
	current := s.root()
	for _, component := range strings.Split(relative, "/") {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect memory recovery path %s: %w", relative, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("memory recovery path %s contains a symlink", relative)
		}
	}
	return nil
}

func (s *Store) writeMemoryRecoveryLocked(journal memoryRecovery) error {
	data, err := json.Marshal(journal)
	if err != nil {
		return fmt.Errorf("encode memory recovery journal: %w", err)
	}
	if err := atomicfile.WriteBytes(s.memoryRecoveryFile(), append(data, '\n')); err != nil {
		return fmt.Errorf("persist memory recovery journal: %w", err)
	}
	return nil
}
