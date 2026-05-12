package embed

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	stateFileVersion = 1
	stateLinePoint   = "point"
	stateLineSync    = "sync"
)

var pointNamespace = uuid.MustParse("c6c9fe34-45b8-4a54-8a1d-6c9707d43725")

// StateStore owns the JSONL dirty-tracking state for embedding sync.
type StateStore struct {
	path string

	mu          sync.Mutex
	points      map[string]stateRecord
	syncRuns    map[string]stateSyncRecord
	byIdentity  map[string]string
	byHash      map[string]map[string]struct{}
	bySource    map[string]map[string]struct{}
	needsFlush  bool
	closeCalled bool
}

type stateRecord struct {
	PointID     string    `json:"point_id"`
	SourceID    string    `json:"source_id"`
	Collection  string    `json:"collection"`
	Path        string    `json:"path"`
	Identity    string    `json:"identity"`
	ContentHash string    `json:"content_hash"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type stateSyncRecord struct {
	SourceID   string     `json:"source_id"`
	Collection string     `json:"collection"`
	LastSynced time.Time  `json:"last_synced"`
	Result     SyncResult `json:"result"`
}

type stateLine struct {
	Type       string           `json:"type"`
	Version    int              `json:"version"`
	Point      *stateRecord     `json:"point,omitempty"`
	SyncRecord *stateSyncRecord `json:"sync,omitempty"`
}

// OpenState opens the JSONL dirty-tracking state file.
func OpenState(ctx context.Context, settings Settings) (*StateStore, error) {
	path := defaultStatePath(settings)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create embed state dir: %w", err)
	}
	store := &StateStore{
		path:       path,
		points:     make(map[string]stateRecord),
		syncRuns:   make(map[string]stateSyncRecord),
		byIdentity: make(map[string]string),
		byHash:     make(map[string]map[string]struct{}),
		bySource:   make(map[string]map[string]struct{}),
	}
	if err := store.load(ctx); err != nil {
		return nil, err
	}
	return store, nil
}

// Close flushes pending JSONL state to disk.
func (s *StateStore) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeCalled = true
	return s.persistLocked()
}

func (s *StateStore) load(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := os.Open(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("open embed state: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		if err := ctx.Err(); err != nil {
			return err
		}
		lineBytes := bytes.TrimSpace(scanner.Bytes())
		if len(lineBytes) == 0 {
			continue
		}
		var line stateLine
		if err := json.Unmarshal(lineBytes, &line); err != nil {
			return fmt.Errorf("decode embed state %s:%d: %w", s.path, lineNumber, err)
		}
		if line.Version != stateFileVersion {
			return fmt.Errorf(
				"embed state %s:%d uses unsupported version %d",
				s.path,
				lineNumber,
				line.Version,
			)
		}
		switch line.Type {
		case stateLinePoint:
			if line.Point == nil || line.Point.PointID == "" {
				return fmt.Errorf("embed state %s:%d point_id is required", s.path, lineNumber)
			}
			s.putPointLocked(*line.Point)
		case stateLineSync:
			if line.SyncRecord == nil || line.SyncRecord.SourceID == "" {
				return fmt.Errorf("embed state %s:%d source_id is required", s.path, lineNumber)
			}
			s.syncRuns[line.SyncRecord.SourceID] = *line.SyncRecord
		default:
			return fmt.Errorf(
				"embed state %s:%d has unknown record type %q",
				s.path,
				lineNumber,
				line.Type,
			)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read embed state: %w", err)
	}
	s.needsFlush = false
	return nil
}

func deterministicPointID(collection, sourceID, identity string) string {
	return uuid.NewSHA1(
		pointNamespace,
		[]byte(collection+"\x00"+sourceID+"\x00"+identity),
	).String()
}

func (s *StateStore) findByIdentity(
	ctx context.Context,
	collection string,
	sourceID string,
	identity string,
) (stateRecord, bool, error) {
	if err := ctx.Err(); err != nil {
		return stateRecord{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.byIdentity[stateIdentityKey(collection, sourceID, identity)]
	if !ok {
		return stateRecord{}, false, nil
	}
	record, ok := s.points[id]
	return record, ok, nil
}

func (s *StateStore) findByContentHash(
	ctx context.Context,
	collection string,
	sourceID string,
	contentHash string,
) (stateRecord, bool, error) {
	if err := ctx.Err(); err != nil {
		return stateRecord{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := s.byHash[stateHashKey(collection, sourceID, contentHash)]
	var best stateRecord
	found := false
	for id := range ids {
		record, ok := s.points[id]
		if !ok {
			continue
		}
		if !found || record.UpdatedAt.After(best.UpdatedAt) {
			best = record
			found = true
		}
	}
	return best, found, nil
}

func (s *StateStore) upsertPoint(ctx context.Context, record stateRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if record.UpdatedAt.IsZero() {
		record.UpdatedAt = time.Now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closeCalled {
		return fmt.Errorf("embed state store is closed")
	}
	s.putPointLocked(record)
	s.needsFlush = true
	return nil
}

func (s *StateStore) recordsForSource(ctx context.Context, sourceID string) ([]stateRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := s.bySource[sourceID]
	records := make([]stateRecord, 0, len(ids))
	for id := range ids {
		if record, ok := s.points[id]; ok {
			records = append(records, record)
		}
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].Identity < records[j].Identity
	})
	return records, nil
}

func (s *StateStore) allSourceIDs(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := make(map[string]struct{}, len(s.bySource)+len(s.syncRuns))
	for id := range s.bySource {
		seen[id] = struct{}{}
	}
	for id := range s.syncRuns {
		seen[id] = struct{}{}
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

func (s *StateStore) deletePointIDs(ctx context.Context, ids []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closeCalled {
		return fmt.Errorf("embed state store is closed")
	}
	for _, id := range ids {
		s.removePointLocked(id)
	}
	s.needsFlush = true
	return nil
}

func (s *StateStore) deleteSource(ctx context.Context, sourceID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closeCalled {
		return fmt.Errorf("embed state store is closed")
	}
	ids := make([]string, 0, len(s.bySource[sourceID]))
	for id := range s.bySource[sourceID] {
		ids = append(ids, id)
	}
	for _, id := range ids {
		s.removePointLocked(id)
	}
	delete(s.syncRuns, sourceID)
	s.needsFlush = true
	return s.persistLocked()
}

func (s *StateStore) storeSyncResult(
	ctx context.Context,
	source Source,
	result SyncResult,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closeCalled {
		return fmt.Errorf("embed state store is closed")
	}
	s.syncRuns[source.ID] = stateSyncRecord{
		SourceID:   source.ID,
		Collection: source.Collection,
		LastSynced: time.Now().UTC(),
		Result:     result,
	}
	s.needsFlush = true
	return s.persistLocked()
}

func (s *StateStore) sourceStatuses(ctx context.Context) ([]SourceStatus, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	byKey := make(map[string]SourceStatus, len(s.bySource)+len(s.syncRuns))
	for _, record := range s.points {
		key := record.SourceID + "\x00" + record.Collection
		status := byKey[key]
		status.SourceID = record.SourceID
		status.Collection = record.Collection
		status.Points++
		byKey[key] = status
	}
	for _, syncRun := range s.syncRuns {
		key := syncRun.SourceID + "\x00" + syncRun.Collection
		status := byKey[key]
		status.SourceID = syncRun.SourceID
		status.Collection = syncRun.Collection
		status.LastSynced = syncRun.LastSynced
		byKey[key] = status
	}
	statuses := make([]SourceStatus, 0, len(byKey))
	for _, status := range byKey {
		statuses = append(statuses, status)
	}
	sort.Slice(statuses, func(i, j int) bool {
		if statuses[i].SourceID == statuses[j].SourceID {
			return statuses[i].Collection < statuses[j].Collection
		}
		return statuses[i].SourceID < statuses[j].SourceID
	})
	return statuses, nil
}

func (s *StateStore) putPointLocked(record stateRecord) {
	s.removePointLocked(record.PointID)
	s.points[record.PointID] = record
	s.byIdentity[stateIdentityKey(record.Collection, record.SourceID, record.Identity)] = record.PointID

	hashKey := stateHashKey(record.Collection, record.SourceID, record.ContentHash)
	if s.byHash[hashKey] == nil {
		s.byHash[hashKey] = make(map[string]struct{})
	}
	s.byHash[hashKey][record.PointID] = struct{}{}

	if s.bySource[record.SourceID] == nil {
		s.bySource[record.SourceID] = make(map[string]struct{})
	}
	s.bySource[record.SourceID][record.PointID] = struct{}{}
}

func (s *StateStore) removePointLocked(pointID string) {
	record, ok := s.points[pointID]
	if !ok {
		return
	}
	delete(s.points, pointID)
	delete(s.byIdentity, stateIdentityKey(record.Collection, record.SourceID, record.Identity))

	hashKey := stateHashKey(record.Collection, record.SourceID, record.ContentHash)
	delete(s.byHash[hashKey], pointID)
	if len(s.byHash[hashKey]) == 0 {
		delete(s.byHash, hashKey)
	}

	delete(s.bySource[record.SourceID], pointID)
	if len(s.bySource[record.SourceID]) == 0 {
		delete(s.bySource, record.SourceID)
	}
}

func (s *StateStore) persistLocked() error {
	if !s.needsFlush {
		return nil
	}
	tmpPath := s.path + ".tmp"
	file, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("open embed state temp file: %w", err)
	}
	encoder := json.NewEncoder(file)
	encoder.SetEscapeHTML(false)
	if err := s.encodeLinesLocked(encoder); err != nil {
		_ = file.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close embed state temp file: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("replace embed state: %w", err)
	}
	s.needsFlush = false
	return nil
}

func (s *StateStore) encodeLinesLocked(encoder *json.Encoder) error {
	pointIDs := make([]string, 0, len(s.points))
	for id := range s.points {
		pointIDs = append(pointIDs, id)
	}
	sort.Strings(pointIDs)
	for _, id := range pointIDs {
		record := s.points[id]
		if err := encoder.Encode(stateLine{
			Type:    stateLinePoint,
			Version: stateFileVersion,
			Point:   &record,
		}); err != nil {
			return fmt.Errorf("encode embed point state: %w", err)
		}
	}

	sourceIDs := make([]string, 0, len(s.syncRuns))
	for id := range s.syncRuns {
		sourceIDs = append(sourceIDs, id)
	}
	sort.Strings(sourceIDs)
	for _, id := range sourceIDs {
		record := s.syncRuns[id]
		if err := encoder.Encode(stateLine{
			Type:       stateLineSync,
			Version:    stateFileVersion,
			SyncRecord: &record,
		}); err != nil {
			return fmt.Errorf("encode embed sync state: %w", err)
		}
	}
	return nil
}

func stateIdentityKey(collection, sourceID, identity string) string {
	return collection + "\x00" + sourceID + "\x00" + identity
}

func stateHashKey(collection, sourceID, contentHash string) string {
	return collection + "\x00" + sourceID + "\x00" + contentHash
}
