// Package selectionstore persists the session's runtime model selections
// (interactive agent + per-job cognition overrides) so a switch survives a
// service restart. It is the durable layer under the live modelcatalog
// selection; the config file is consulted only as a one-time bootstrap seed.
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
	SchemaVersion int                  `json:"schema_version"`
	Interactive   Selection            `json:"interactive"`
	Cognition     map[string]Selection `json:"cognition,omitempty"`
}

// Store is the in-memory cached selection store. It loads once at startup,
// serves cheap concurrent reads, and atomically persists on every write.
type Store struct {
	mu   sync.RWMutex
	path string
	now  func() time.Time

	interactive Selection
	cognition   map[string]Selection
}

// Open loads the selection store from path. A missing file returns an empty
// store (first run); a corrupt file returns an error.
func Open(path string) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("selection store path is required")
	}
	store := &Store{
		path:      filepath.Clean(path),
		now:       time.Now,
		cognition: map[string]Selection{},
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
	if doc.Cognition != nil {
		s.cognition = cloneCognitionMap(doc.Cognition)
	} else {
		s.cognition = map[string]Selection{}
	}
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

// SetInteractive persists the interactive selection. Persistence happens
// before the in-memory state is committed, so a write failure leaves the
// running selection unchanged.
func (s *Store) SetInteractive(provider, model string) error {
	if s == nil {
		return fmt.Errorf("selection store is not configured")
	}
	selection := Selection{
		Provider:  strings.TrimSpace(provider),
		Model:     strings.TrimSpace(model),
		UpdatedAt: s.now().UTC(),
	}
	if !selection.IsValid() {
		return fmt.Errorf("interactive selection requires provider and model")
	}

	s.mu.Lock()
	nextInteractive := selection
	nextCognition := s.cognition
	s.mu.Unlock()
	if err := s.persist(nextInteractive, nextCognition); err != nil {
		return err
	}

	s.mu.Lock()
	s.interactive = nextInteractive
	s.mu.Unlock()
	return nil
}

// Cognition returns the persisted cognition override for one job type, plus
// whether an explicit override exists.
func (s *Store) Cognition(jobType string) (Selection, bool) {
	if s == nil {
		return Selection{}, false
	}
	jobType = strings.TrimSpace(jobType)
	if jobType == "" {
		return Selection{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	selection, ok := s.cognition[jobType]
	return selection, ok
}

// CognitionSelections returns a snapshot copy of all cognition overrides.
func (s *Store) CognitionSelections() map[string]Selection {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneCognitionMap(s.cognition)
}

// SetCognition persists a cognition override for one job type. Persistence
// happens before the in-memory state is committed.
func (s *Store) SetCognition(jobType, provider, model string) error {
	if s == nil {
		return fmt.Errorf("selection store is not configured")
	}
	jobType = strings.TrimSpace(jobType)
	if jobType == "" {
		return fmt.Errorf("job_type is required")
	}
	selection := Selection{
		Provider:  strings.TrimSpace(provider),
		Model:     strings.TrimSpace(model),
		UpdatedAt: s.now().UTC(),
	}
	if !selection.IsValid() {
		return fmt.Errorf("cognition selection requires provider and model")
	}

	s.mu.Lock()
	nextInteractive := s.interactive
	nextCognition := cloneCognitionMap(s.cognition)
	s.mu.Unlock()
	if nextCognition == nil {
		nextCognition = map[string]Selection{}
	}
	nextCognition[jobType] = selection
	if err := s.persist(nextInteractive, nextCognition); err != nil {
		return err
	}

	s.mu.Lock()
	s.interactive = nextInteractive
	s.cognition = nextCognition
	s.mu.Unlock()
	return nil
}

// ClearCognition removes a cognition override so the job falls back to the
// interactive selection. Persistence happens before the in-memory state is
// committed.
func (s *Store) ClearCognition(jobType string) error {
	if s == nil {
		return fmt.Errorf("selection store is not configured")
	}
	jobType = strings.TrimSpace(jobType)
	if jobType == "" {
		return fmt.Errorf("job_type is required")
	}

	s.mu.Lock()
	nextInteractive := s.interactive
	nextCognition := cloneCognitionMap(s.cognition)
	s.mu.Unlock()
	if _, ok := nextCognition[jobType]; !ok {
		return nil
	}
	delete(nextCognition, jobType)
	if err := s.persist(nextInteractive, nextCognition); err != nil {
		return err
	}

	s.mu.Lock()
	s.interactive = nextInteractive
	s.cognition = nextCognition
	s.mu.Unlock()
	return nil
}

// persist writes a staged document (interactive + cognition) atomically. It is
// the single on-disk write path; callers stage the desired next state and only
// commit it to the Store fields after a successful return.
func (s *Store) persist(interactive Selection, cognition map[string]Selection) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("create selection store directory: %w", err)
	}

	doc := document{
		SchemaVersion: SchemaVersion,
		Interactive:   interactive,
		Cognition:     cloneCognitionMap(cognition),
	}
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

func cloneCognitionMap(in map[string]Selection) map[string]Selection {
	if len(in) == 0 {
		return map[string]Selection{}
	}
	out := make(map[string]Selection, len(in))
	for key, value := range in {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		out[key] = value
	}
	return out
}
