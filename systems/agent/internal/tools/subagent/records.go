package subagent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/q15co/q15/systems/agent/internal/atomicfile"
)

const (
	recordSchemaVersion = 1
	maxRecordBytes      = 1 << 20
	maxRecordTailEvents = 16
	maxRecordEventText  = 1024
	maxRecordErrorText  = 2048
)

var recordIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,159}$`)

type sessionRecord struct {
	SchemaVersion  int       `json:"schema_version"`
	ID             string    `json:"id"`
	RunID          string    `json:"run_id"`
	Model          string    `json:"model"`
	Task           string    `json:"task"`
	ParentTurn     int64     `json:"parent_turn"`
	State          string    `json:"state"`
	CreatedAt      time.Time `json:"created_at"`
	FinishedAt     time.Time `json:"finished_at,omitempty"`
	FinalAnswer    string    `json:"final_answer,omitempty"`
	Error          string    `json:"error,omitempty"`
	EventCount     int       `json:"event_count"`
	LastEventIndex int       `json:"last_event_index"`
	Tail           []Event   `json:"tail,omitempty"`
}

type recordStore struct {
	root string
	lock *os.File
}

func openRecordStore(root string) (*recordStore, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("subagent record directory is required")
	}
	if err := atomicfile.EnsureDirectory(root, 0o700); err != nil {
		return nil, fmt.Errorf("create subagent record directory: %w", err)
	}
	lock, err := os.OpenFile(filepath.Join(root, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open subagent record lock: %w", err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("lock subagent records (another manager may own them): %w", err)
	}
	return &recordStore{root: root, lock: lock}, nil
}

func (s *recordStore) close() error { return s.lock.Close() }

func (s *recordStore) path(id string) string { return filepath.Join(s.root, id+".json") }

func (s *recordStore) remove(id string) error {
	if err := atomicfile.Remove(s.path(id)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove subagent record %q: %w", id, err)
	}
	return nil
}

func (s *recordStore) save(record sessionRecord) error {
	if err := record.validate(); err != nil {
		return err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if len(data) > maxRecordBytes {
		return errors.New("subagent record exceeds its size limit")
	}
	if err := atomicfile.WriteBytes(s.path(record.ID), append(data, '\n')); err != nil {
		return fmt.Errorf("write subagent record %q: %w", record.ID, err)
	}
	return nil
}

func (s *recordStore) load() ([]sessionRecord, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, err
	}
	var records []sessionRecord
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".tmp-") && entry.Type().IsRegular() {
			if err := atomicfile.Remove(filepath.Join(s.root, entry.Name())); err != nil {
				return nil, fmt.Errorf("remove interrupted record write: %w", err)
			}
			continue
		}
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		if !entry.Type().IsRegular() {
			return nil, fmt.Errorf("subagent record %q is not a regular file", entry.Name())
		}
		record, err := readSessionRecord(filepath.Join(s.root, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("load subagent record %q: %w", entry.Name(), err)
		}
		if entry.Name() != record.ID+".json" {
			return nil, fmt.Errorf("subagent record %q does not match its id", entry.Name())
		}
		records = append(records, record)
	}
	return records, nil
}

func readSessionRecord(path string) (sessionRecord, error) {
	file, err := os.Open(path)
	if err != nil {
		return sessionRecord{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxRecordBytes+1))
	if err != nil {
		return sessionRecord{}, err
	}
	if len(data) > maxRecordBytes {
		return sessionRecord{}, errors.New("record exceeds its size limit")
	}
	var record sessionRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return sessionRecord{}, err
	}
	return record, record.validate()
}

func (r sessionRecord) validate() error {
	if r.SchemaVersion != recordSchemaVersion {
		return fmt.Errorf("unsupported subagent record version %d", r.SchemaVersion)
	}
	if !recordIDPattern.MatchString(r.ID) || r.RunID == "" || r.Model == "" ||
		r.Task == "" || r.CreatedAt.IsZero() || r.ParentTurn < 0 {
		return errors.New("invalid subagent record identity")
	}
	switch r.State {
	case "running":
		if !r.FinishedAt.IsZero() {
			return errors.New("running subagent record has a finish timestamp")
		}
	case "completed", "failed", "killed", "interrupted":
		if r.FinishedAt.IsZero() {
			return errors.New("finished subagent record has no finish timestamp")
		}
	default:
		return fmt.Errorf("unknown subagent record state %q", r.State)
	}
	if r.EventCount < 0 || r.LastEventIndex != r.EventCount-1 ||
		len(r.Tail) > maxRecordTailEvents || len(r.Tail) > r.EventCount {
		return errors.New("invalid subagent event counts")
	}
	for i, event := range r.Tail {
		if event.Index != r.EventCount-len(r.Tail)+i {
			return errors.New("invalid subagent event cursor")
		}
	}
	return nil
}

// Restore opens the manager's private record directory before any sessions
// start. Its exclusive lock prevents a second process from reconciling work
// that still belongs to a live manager.
func (m *Manager) Restore(root string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.records != nil || len(m.sessions) != 0 || m.closed {
		return errors.New("subagent records must be restored before starting sessions")
	}
	store, err := openRecordStore(root)
	if err != nil {
		return err
	}
	records, err := store.load()
	if err != nil {
		_ = store.close()
		return err
	}
	sequence, err := store.loadSequence(records)
	if err != nil {
		_ = store.close()
		return err
	}
	runID, err := uuid.NewRandom()
	if err != nil {
		_ = store.close()
		return fmt.Errorf("create subagent run identity: %w", err)
	}
	sessions := make(map[string]*Session, len(records))
	for _, record := range records {
		if record.State == "running" {
			record.State = "interrupted"
			record.Error = "lost to restart"
			record.FinishedAt = time.Now().UTC()
			record.Tail = append(record.Tail, Event{
				Index: record.EventCount, Type: "interrupted", Error: record.Error,
			})
			record.EventCount++
			record.LastEventIndex = record.EventCount - 1
			if len(record.Tail) > maxRecordTailEvents {
				record.Tail = record.Tail[len(record.Tail)-maxRecordTailEvents:]
			}
			if err := store.save(record); err != nil {
				_ = store.close()
				return err
			}
			log.Printf(
				"q15: subagent event=reconciled session_id=%q state=interrupted reason=%q",
				record.ID,
				record.Error,
			)
		}
		session := &Session{
			ID: record.ID, Model: record.Model, Task: record.Task, Status: record.State,
			CreatedAt: record.CreatedAt, finishedAt: record.FinishedAt,
			parentTurn: record.ParentTurn, runID: record.RunID,
			finalAnswer: record.FinalAnswer, events: record.Tail,
			droppedEvents: record.EventCount - len(record.Tail),
			restored:      true, recordPath: store.path(record.ID), done: make(chan struct{}),
		}
		if record.Error != "" {
			session.err = errors.New(record.Error)
		}
		close(session.done)
		sessions[session.ID] = session
	}
	m.records, m.runID, m.sessions, m.next = store, runID.String(), sessions, sequence
	if err := m.evictFinishedSessionsLocked(); err != nil {
		m.records, m.runID, m.sessions = nil, "", make(map[string]*Session)
		m.next = 0
		_ = store.close()
		return err
	}
	return nil
}

// Close records shutdown for active sessions and waits for their writers to
// finish before releasing the record directory to another manager.
func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	for _, session := range m.sessions {
		session.mu.Lock()
		if session.Status == "running" {
			session.Status = "interrupted"
			session.err = errors.New("shutdown")
			session.addEventLocked("interrupted", "", session.err)
		}
		if session.cancel != nil {
			session.cancel()
		}
		session.mu.Unlock()
	}
	m.mu.Unlock()
	m.workers.Wait()
	if m.records != nil {
		return m.records.close()
	}
	return nil
}

func (s *Session) recordLocked() sessionRecord {
	record := sessionRecord{
		SchemaVersion: recordSchemaVersion, ID: s.ID, RunID: s.runID,
		Model: s.Model, Task: truncate(s.Task, defaultMaxOutput), ParentTurn: s.parentTurn,
		State: s.Status, CreatedAt: s.CreatedAt, FinishedAt: s.finishedAt,
		FinalAnswer: truncate(s.finalAnswer, defaultMaxOutput),
		EventCount:  s.droppedEvents + len(s.events),
	}
	if s.err != nil {
		record.Error = truncate(s.err.Error(), maxRecordErrorText)
	}
	record.LastEventIndex = record.EventCount - 1
	for _, event := range s.events[max(0, len(s.events)-maxRecordTailEvents):] {
		event.Text = truncate(event.Text, maxRecordEventText)
		event.Error = truncate(event.Error, maxRecordErrorText)
		record.Tail = append(record.Tail, event)
	}
	return record
}

func (m *Manager) saveSessionLocked(s *Session) {
	if m.records == nil {
		return
	}
	if err := m.records.save(s.recordLocked()); err != nil {
		s.persistenceErr = fmt.Errorf("persist subagent session: %w", err)
		log.Printf("q15: subagent event=persistence_failed session_id=%q error=%q", s.ID, err)
		if s.cancel != nil {
			s.cancel()
		}
	}
}
