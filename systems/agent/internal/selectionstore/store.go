// Package selectionstore persists interactive and decision model selections.
package selectionstore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// SchemaVersion is the persisted selection store schema version.
	SchemaVersion = 1

	// DefaultRelativePath is the default store path relative to the workspace
	// local root. It mirrors the embed state convention under /workspace/.q15.
	DefaultRelativePath = ".q15/agent/selection.json"
)

// Selection is one persisted provider/model pair.
type Selection struct {
	Provider  string    `json:"provider"`
	Model     string    `json:"model"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

// IsValid reports whether the selection has a non-empty provider and model.
func (s Selection) IsValid() bool {
	return strings.TrimSpace(s.Provider) != "" && strings.TrimSpace(s.Model) != ""
}

// document is the on-disk store shape.
type document struct {
	SchemaVersion int       `json:"schema_version"`
	Interactive   Selection `json:"interactive"`
	Decision      Selection `json:"decision,omitempty"`
}

// Store is the in-memory cached selection store. It loads once at startup,
// serves cheap concurrent reads, and atomically persists on every write.
type Store struct {
	mu      sync.RWMutex
	writeMu sync.Mutex // serializes stage-persist-commit of one write
	path    string
	now     func() time.Time

	interactive Selection
	decision    Selection
}

// Open loads the selection store from path. A missing file returns an empty
// store (first run); a corrupt file returns an error.
func Open(path string) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("selection store path is required")
	}
	store := &Store{
		path: filepath.Clean(path),
		now:  time.Now,
	}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

// DefaultPath returns the conventional store path under the workspace root.
func DefaultPath(workspaceLocalDir string) string {
	return filepath.Join(workspaceLocalDir, filepath.FromSlash(DefaultRelativePath))
}

func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read selection store %q: %w", s.path, err)
	}

	var doc document
	if err := json.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("decode selection store %q: %w", s.path, err)
	}
	if doc.SchemaVersion == 0 {
		doc.SchemaVersion = SchemaVersion
	}
	if doc.SchemaVersion > SchemaVersion {
		return fmt.Errorf(
			"selection store %q schema_version %d is newer than supported %d",
			s.path,
			doc.SchemaVersion,
			SchemaVersion,
		)
	}

	s.mu.Lock()
	s.interactive = doc.Interactive
	s.decision = doc.Decision
	s.mu.Unlock()
	return nil
}

// Interactive returns the persisted interactive selection.
func (s *Store) Interactive() Selection {
	if s == nil {
		return Selection{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.interactive
}

// HasInteractive reports whether a valid interactive selection is persisted.
func (s *Store) HasInteractive() bool {
	return s.Interactive().IsValid()
}

// Decision returns the persisted decision selection.
func (s *Store) Decision() Selection {
	if s == nil {
		return Selection{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.decision
}

// SetInteractive persists the interactive selection. Persistence happens
// before the in-memory state is committed, so a write failure leaves the
// running selection unchanged.
func (s *Store) SetInteractive(provider, model string) error {
	return s.setSelection("interactive", provider, model)
}

// SetDecision persists the decision selection with the same durability rule
// as SetInteractive.
func (s *Store) SetDecision(provider, model string) error {
	return s.setSelection("decision", provider, model)
}

// setSelection stages one slot's next value, persists the whole document
// atomically, and only then commits the in-memory state.
func (s *Store) setSelection(slot, provider, model string) error {
	if s == nil {
		return fmt.Errorf("selection store is not configured")
	}
	selection := Selection{
		Provider:  strings.TrimSpace(provider),
		Model:     strings.TrimSpace(model),
		UpdatedAt: s.now().UTC(),
	}
	if !selection.IsValid() {
		return fmt.Errorf("%s selection requires provider and model", slot)
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.mu.Lock()
	next := s.documentLocked()
	s.mu.Unlock()
	switch slot {
	case "interactive":
		next.Interactive = selection
	case "decision":
		next.Decision = selection
	default:
		return fmt.Errorf("unknown selection slot %q", slot)
	}
	if err := s.persist(next); err != nil {
		return err
	}

	s.mu.Lock()
	switch slot {
	case "interactive":
		s.interactive = selection
	case "decision":
		s.decision = selection
	}
	s.mu.Unlock()
	return nil
}

// documentLocked snapshots the in-memory state as a staged document. The
// caller holds no lock; it calls this under s.mu.
func (s *Store) documentLocked() document {
	return document{
		SchemaVersion: SchemaVersion,
		Interactive:   s.interactive,
		Decision:      s.decision,
	}
}

// persist writes a staged document atomically. It is the single on-disk write
// path; callers stage the desired next state and only commit it to the Store
// fields after a successful return.
func (s *Store) persist(doc document) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("create selection store directory: %w", err)
	}

	doc.SchemaVersion = SchemaVersion
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encode selection store: %w", err)
	}
	data = append(data, '\n')

	tmpPath := s.path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o600); err != nil {
		return fmt.Errorf("write selection store temp file: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("replace selection store: %w", err)
	}
	return nil
}
