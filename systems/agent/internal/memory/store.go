package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
)

const (
	headStateRelativePath = "state/head.json"
	readmeRelativePath    = "README.md"
)

type Store struct {
	mu        sync.Mutex
	rootDir   string
	committer Committer
}

var _ agent.ConversationStore = (*Store)(nil)

func NewStore(rootDir string, committer Committer) *Store {
	if committer == nil {
		committer = NewGitCommitter()
	}
	return &Store{
		rootDir:   filepath.Clean(strings.TrimSpace(rootDir)),
		committer: committer,
	}
}

func (s *Store) Init(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if strings.TrimSpace(s.rootDir) == "" {
		return fmt.Errorf("memory root dir is required")
	}
	if !filepath.IsAbs(s.rootDir) {
		return fmt.Errorf("memory root dir must be absolute")
	}

	dirs := []string{
		filepath.Join(s.rootDir, "history", "turns"),
		filepath.Join(s.rootDir, "notes", "inbox"),
		filepath.Join(s.rootDir, "notes", "zettel"),
		filepath.Join(s.rootDir, "notes", "maps"),
		filepath.Join(s.rootDir, "state", "indexer"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create memory dir %q: %w", dir, err)
		}
	}

	if err := s.ensureREADME(); err != nil {
		return err
	}
	if err := s.ensureHeadState(); err != nil {
		return err
	}

	if err := s.committer.EnsureRepo(ctx, s.rootDir); err != nil {
		return fmt.Errorf("ensure git memory repo: %w", err)
	}
	if _, err := s.committer.CommitAll(ctx, s.rootDir, "memory: initialize repository"); err != nil {
		return fmt.Errorf("commit memory scaffold: %w", err)
	}

	return nil
}

func (s *Store) LoadRecentMessages(ctx context.Context, turns int) ([]agent.Message, error) {
	_ = ctx
	if turns <= 0 {
		return nil, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	paths, err := s.listTurnPaths()
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, nil
	}

	start := 0
	if len(paths) > turns {
		start = len(paths) - turns
	}

	out := make([]agent.Message, 0, turns*2)
	for _, path := range paths[start:] {
		turn, err := s.readTurn(path)
		if err != nil {
			return nil, err
		}
		out = append(out, copyMessages(turn.Messages)...)
	}
	return out, nil
}

func (s *Store) AppendTurn(ctx context.Context, messages []agent.Message) error {
	if len(messages) == 0 {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	head, err := s.readHeadState()
	if err != nil {
		return err
	}

	seq := head.LastSeq + 1
	now := time.Now().UTC()
	record := turnRecord{
		ID:        fmt.Sprintf("turn-%020d", seq),
		Seq:       seq,
		CreatedAt: now,
		Messages:  copyMessages(messages),
	}

	turnPath := s.turnPath(now, seq)
	if err := writeJSONFileAtomic(turnPath, record); err != nil {
		return fmt.Errorf("write turn record %q: %w", turnPath, err)
	}

	head.LastSeq = seq
	head.UpdatedAt = now
	if err := writeJSONFileAtomic(s.headStatePath(), head); err != nil {
		return fmt.Errorf("write memory head state: %w", err)
	}

	if _, err := s.committer.CommitAll(ctx, s.rootDir, fmt.Sprintf("memory: append turn %d", seq)); err != nil {
		return fmt.Errorf("commit memory turn %d: %w", seq, err)
	}

	return nil
}

func (s *Store) ensureREADME() error {
	path := filepath.Join(s.rootDir, readmeRelativePath)
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat memory README: %w", err)
	}

	content := strings.TrimSpace(`
# q15 Agent Memory

This directory contains persistent agent memory.

- Conversation turns are stored as canonical JSON files under history/turns/.
- Notes are organized under notes/inbox, notes/zettel, and notes/maps.
- Git history tracks all memory changes.
`)
	if err := writeTextFileAtomic(path, content+"\n"); err != nil {
		return fmt.Errorf("write memory README: %w", err)
	}
	return nil
}

func (s *Store) ensureHeadState() error {
	path := s.headStatePath()
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat memory head state: %w", err)
	}

	head := headState{
		LastSeq:   0,
		UpdatedAt: time.Now().UTC(),
	}
	if err := writeJSONFileAtomic(path, head); err != nil {
		return fmt.Errorf("initialize memory head state: %w", err)
	}
	return nil
}

func (s *Store) listTurnPaths() ([]string, error) {
	base := filepath.Join(s.rootDir, "history", "turns")
	entries := make([]string, 0, 64)

	err := filepath.WalkDir(base, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if strings.EqualFold(filepath.Ext(d.Name()), ".json") {
			entries = append(entries, path)
		}
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("walk turn history: %w", err)
	}

	sort.Strings(entries)
	return entries, nil
}

func (s *Store) readTurn(path string) (turnRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return turnRecord{}, fmt.Errorf("read turn record %q: %w", path, err)
	}
	var record turnRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return turnRecord{}, fmt.Errorf("decode turn record %q: %w", path, err)
	}
	return record, nil
}

func (s *Store) readHeadState() (headState, error) {
	data, err := os.ReadFile(s.headStatePath())
	if err != nil {
		return headState{}, fmt.Errorf("read memory head state: %w", err)
	}

	var head headState
	if err := json.Unmarshal(data, &head); err != nil {
		return headState{}, fmt.Errorf("decode memory head state: %w", err)
	}
	return head, nil
}

func (s *Store) headStatePath() string {
	return filepath.Join(s.rootDir, headStateRelativePath)
}

func (s *Store) turnPath(ts time.Time, seq int64) string {
	return filepath.Join(
		s.rootDir,
		"history",
		"turns",
		ts.Format("2006"),
		ts.Format("01"),
		ts.Format("02"),
		fmt.Sprintf("%020d.json", seq),
	)
}

func copyMessages(in []agent.Message) []agent.Message {
	if len(in) == 0 {
		return nil
	}

	out := make([]agent.Message, len(in))
	for i, msg := range in {
		out[i] = msg
		if len(msg.ToolCalls) > 0 {
			out[i].ToolCalls = append([]agent.ToolCall(nil), msg.ToolCalls...)
		}
		if len(msg.ProviderRaw) > 0 {
			out[i].ProviderRaw = append([]byte(nil), msg.ProviderRaw...)
		}
	}
	return out
}

func writeJSONFileAtomic(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal JSON for %q: %w", path, err)
	}
	data = append(data, '\n')
	return writeBytesFileAtomic(path, data)
}

func writeTextFileAtomic(path, text string) error {
	return writeBytesFileAtomic(path, []byte(text))
}

func writeBytesFileAtomic(path string, data []byte) (err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create parent dir %q: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".tmp-*")
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
