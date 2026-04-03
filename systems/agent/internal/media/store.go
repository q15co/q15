package media

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

const (
	defaultScopeName = "default"
	refPrefix        = "media://sha256/"
)

// Meta describes one stored media object.
type Meta struct {
	Filename    string `json:"filename,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Source      string `json:"source,omitempty"`
}

// Store persists media objects under a runtime-owned root and resolves stable refs.
type Store interface {
	Store(localPath string, meta Meta, scope string) (ref string, err error)
	Resolve(ref string) (localPath string, meta Meta, err error)
	ReleaseAll(scope string) error
}

// FileStore is a file-backed Store rooted at one runtime-visible media directory.
type FileStore struct {
	mu      sync.Mutex
	rootDir string
}

type refRecord struct {
	Ref        string `json:"ref"`
	ObjectPath string `json:"object_path"`
	Meta       Meta   `json:"meta"`
}

type scopeRecord struct {
	Scope string   `json:"scope"`
	Refs  []string `json:"refs"`
}

// NewFileStore constructs a file-backed media store rooted at rootDir.
func NewFileStore(rootDir string) (*FileStore, error) {
	rootDir = filepath.Clean(strings.TrimSpace(rootDir))
	if rootDir == "" {
		return nil, fmt.Errorf("media root dir is required")
	}
	if !filepath.IsAbs(rootDir) {
		return nil, fmt.Errorf("media root dir must be absolute")
	}

	store := &FileStore{rootDir: rootDir}
	if err := store.ensureLayout(); err != nil {
		return nil, err
	}
	return store, nil
}

// Store copies one local file into the media root and records it under the scope.
func (s *FileStore) Store(localPath string, meta Meta, scope string) (string, error) {
	if s == nil {
		return "", fmt.Errorf("media store is required")
	}

	localPath = filepath.Clean(strings.TrimSpace(localPath))
	if localPath == "" {
		return "", fmt.Errorf("media local path is required")
	}

	info, err := os.Stat(localPath)
	if err != nil {
		return "", fmt.Errorf("stat media file %q: %w", localPath, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("media path %q must be a file", localPath)
	}

	hashHex, err := hashFile(localPath)
	if err != nil {
		return "", fmt.Errorf("hash media file %q: %w", localPath, err)
	}

	ref := refPrefix + hashHex
	objectPath := s.objectPath(hashHex)
	meta = normalizeMeta(meta, localPath)
	scope = normalizeScope(scope)

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureLayout(); err != nil {
		return "", err
	}
	if err := copyFileIntoStore(localPath, objectPath); err != nil {
		return "", err
	}

	record := refRecord{
		Ref:        ref,
		ObjectPath: objectPath,
		Meta:       mergeMeta(s.readExistingMeta(ref), meta),
	}
	if err := writeJSONFileAtomic(s.refPath(hashHex), record); err != nil {
		return "", fmt.Errorf("write media ref %q: %w", ref, err)
	}

	scopeData, err := s.readScope(scope)
	if err != nil {
		return "", err
	}
	if !slices.Contains(scopeData.Refs, ref) {
		scopeData.Refs = append(scopeData.Refs, ref)
	}
	if err := writeJSONFileAtomic(s.scopePath(scope), scopeData); err != nil {
		return "", fmt.Errorf("write media scope %q: %w", scope, err)
	}

	return ref, nil
}

// Resolve returns the stored object path and metadata for one media ref.
func (s *FileStore) Resolve(ref string) (string, Meta, error) {
	if s == nil {
		return "", Meta{}, fmt.Errorf("media store is required")
	}

	hashHex, err := parseRef(ref)
	if err != nil {
		return "", Meta{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	record, err := s.readRefRecord(hashHex)
	if err != nil {
		return "", Meta{}, err
	}
	if _, err := os.Stat(record.ObjectPath); err != nil {
		return "", Meta{}, fmt.Errorf("stat media object %q: %w", record.ObjectPath, err)
	}
	return record.ObjectPath, record.Meta, nil
}

// ReleaseAll removes all refs registered to scope and deletes unreferenced objects.
func (s *FileStore) ReleaseAll(scope string) error {
	if s == nil {
		return fmt.Errorf("media store is required")
	}

	scope = normalizeScope(scope)

	s.mu.Lock()
	defer s.mu.Unlock()

	record, err := s.readScope(scope)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if err := os.Remove(s.scopePath(scope)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove media scope %q: %w", scope, err)
	}

	remainingRefs, err := s.loadRemainingRefs()
	if err != nil {
		return err
	}

	for _, ref := range record.Refs {
		if _, inUse := remainingRefs[ref]; inUse {
			continue
		}

		hashHex, err := parseRef(ref)
		if err != nil {
			continue
		}
		refRecord, err := s.readRefRecord(hashHex)
		if err == nil {
			if err := os.Remove(refRecord.ObjectPath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove media object %q: %w", refRecord.ObjectPath, err)
			}
			_ = os.Remove(filepath.Dir(refRecord.ObjectPath))
			_ = os.Remove(filepath.Dir(filepath.Dir(refRecord.ObjectPath)))
		}
		if err := os.Remove(s.refPath(hashHex)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove media ref %q: %w", ref, err)
		}
	}

	return nil
}

func (s *FileStore) ensureLayout() error {
	for _, dir := range []string{
		s.objectsDir(),
		s.refsDir(),
		s.scopesDir(),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create media dir %q: %w", dir, err)
		}
	}
	return nil
}

func (s *FileStore) objectsDir() string {
	return filepath.Join(s.rootDir, "objects")
}

func (s *FileStore) refsDir() string {
	return filepath.Join(s.rootDir, "refs", "sha256")
}

func (s *FileStore) scopesDir() string {
	return filepath.Join(s.rootDir, "scopes")
}

func (s *FileStore) objectPath(hashHex string) string {
	return filepath.Join(s.objectsDir(), hashHex[:2], hashHex[2:4], hashHex)
}

func (s *FileStore) refPath(hashHex string) string {
	return filepath.Join(s.refsDir(), hashHex+".json")
}

func (s *FileStore) scopePath(scope string) string {
	sum := sha256.Sum256([]byte(scope))
	return filepath.Join(s.scopesDir(), hex.EncodeToString(sum[:])+".json")
}

func (s *FileStore) readExistingMeta(ref string) Meta {
	hashHex, err := parseRef(ref)
	if err != nil {
		return Meta{}
	}
	record, err := s.readRefRecord(hashHex)
	if err != nil {
		return Meta{}
	}
	return record.Meta
}

func (s *FileStore) readRefRecord(hashHex string) (refRecord, error) {
	data, err := os.ReadFile(s.refPath(hashHex))
	if err != nil {
		return refRecord{}, fmt.Errorf("read media ref %q: %w", hashHex, err)
	}

	var record refRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return refRecord{}, fmt.Errorf("decode media ref %q: %w", hashHex, err)
	}
	return record, nil
}

func (s *FileStore) readScope(scope string) (scopeRecord, error) {
	path := s.scopePath(scope)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return scopeRecord{Scope: scope}, nil
		}
		return scopeRecord{}, fmt.Errorf("read media scope %q: %w", scope, err)
	}

	var record scopeRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return scopeRecord{}, fmt.Errorf("decode media scope %q: %w", scope, err)
	}
	if strings.TrimSpace(record.Scope) == "" {
		record.Scope = scope
	}
	return record, nil
}

func (s *FileStore) loadRemainingRefs() (map[string]struct{}, error) {
	entries, err := os.ReadDir(s.scopesDir())
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]struct{}{}, nil
		}
		return nil, fmt.Errorf("read media scopes: %w", err)
	}

	out := make(map[string]struct{})
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}

		data, err := os.ReadFile(filepath.Join(s.scopesDir(), entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read media scope %q: %w", entry.Name(), err)
		}

		var record scopeRecord
		if err := json.Unmarshal(data, &record); err != nil {
			return nil, fmt.Errorf("decode media scope %q: %w", entry.Name(), err)
		}
		for _, ref := range record.Refs {
			ref = strings.TrimSpace(ref)
			if ref == "" {
				continue
			}
			out[ref] = struct{}{}
		}
	}
	return out, nil
}

func normalizeMeta(meta Meta, localPath string) Meta {
	meta.Filename = strings.TrimSpace(meta.Filename)
	if meta.Filename == "" {
		meta.Filename = filepath.Base(localPath)
	}
	meta.ContentType = strings.TrimSpace(meta.ContentType)
	meta.Source = strings.TrimSpace(meta.Source)
	return meta
}

func mergeMeta(existing, next Meta) Meta {
	if strings.TrimSpace(existing.Filename) != "" {
		next.Filename = existing.Filename
	}
	if strings.TrimSpace(existing.ContentType) != "" {
		next.ContentType = existing.ContentType
	}
	if strings.TrimSpace(existing.Source) != "" {
		next.Source = existing.Source
	}
	return next
}

func normalizeScope(scope string) string {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return defaultScopeName
	}
	return scope
}

func parseRef(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if !strings.HasPrefix(ref, refPrefix) {
		return "", fmt.Errorf("unsupported media ref %q", ref)
	}
	hashHex := strings.TrimSpace(strings.TrimPrefix(ref, refPrefix))
	if len(hashHex) != 64 {
		return "", fmt.Errorf("invalid media ref %q", ref)
	}
	if _, err := hex.DecodeString(hashHex); err != nil {
		return "", fmt.Errorf("invalid media ref %q: %w", ref, err)
	}
	return hashHex, nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

func copyFileIntoStore(sourcePath, targetPath string) error {
	if _, err := os.Stat(targetPath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat media object %q: %w", targetPath, err)
	}

	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return fmt.Errorf("create media object dir %q: %w", filepath.Dir(targetPath), err)
	}

	source, err := os.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("open media source %q: %w", sourcePath, err)
	}
	defer source.Close()

	tmp, err := os.CreateTemp(filepath.Dir(targetPath), ".tmp-*")
	if err != nil {
		return fmt.Errorf("create media temp file %q: %w", targetPath, err)
	}
	defer func() {
		_ = os.Remove(tmp.Name())
	}()

	if _, err := io.Copy(tmp, source); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("copy media object to %q: %w", targetPath, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync media temp file %q: %w", targetPath, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close media temp file %q: %w", targetPath, err)
	}
	if err := os.Rename(tmp.Name(), targetPath); err != nil {
		if os.IsExist(err) {
			return nil
		}
		return fmt.Errorf("rename media temp file to %q: %w", targetPath, err)
	}
	return nil
}

func writeJSONFileAtomic(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal media JSON for %q: %w", path, err)
	}
	data = append(data, '\n')
	return writeBytesFileAtomic(path, data)
}

func writeBytesFileAtomic(path string, data []byte) (err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create media parent dir %q: %w", filepath.Dir(path), err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file for %q: %w", path, err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp file for %q: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp file for %q: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file for %q: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("rename temp file for %q: %w", path, err)
	}
	return nil
}
