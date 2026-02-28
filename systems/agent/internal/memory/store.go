package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/yuin/goldmark"
	meta "github.com/yuin/goldmark-meta"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
)

const (
	headStateRelativePath = "state/head.json"
	readmeRelativePath    = "README.md"
	coreDirPath           = "core"
)

var coreFrontmatterParser = goldmark.New(
	goldmark.WithExtensions(meta.Meta),
)

type Store struct {
	mu        sync.Mutex
	rootDir   string
	agentName string
	committer Committer
}

var _ agent.ConversationStore = (*Store)(nil)
var _ agent.CoreMemoryStore = (*Store)(nil)

func NewStore(rootDir string, agentName string, committer Committer) *Store {
	if committer == nil {
		committer = NewGitCommitter()
	}
	return &Store{
		rootDir:   filepath.Clean(strings.TrimSpace(rootDir)),
		agentName: normalizeAgentName(agentName),
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
		filepath.Join(s.rootDir, "core"),
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
	if err := s.ensureCoreMemory(); err != nil {
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

func (s *Store) LoadCoreMemory(ctx context.Context) (agent.CoreMemory, error) {
	_ = ctx

	s.mu.Lock()
	defer s.mu.Unlock()

	files, err := s.loadCoreFiles()
	if err != nil {
		return agent.CoreMemory{}, err
	}
	return agent.CoreMemory{
		Files: files,
	}, nil
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

	- Core memory (always injected into the system prompt) is stored in core/*.md (for example AGENT.md, USER.md, SOUL.md).
	- Conversation turns are stored as canonical JSON files under history/turns/.
	- Notes are organized under notes/inbox, notes/zettel, and notes/maps.
	- Git history tracks all memory changes.
`)
	if err := writeTextFileAtomic(path, content+"\n"); err != nil {
		return fmt.Errorf("write memory README: %w", err)
	}
	return nil
}

func (s *Store) ensureCoreMemory() error {
	files := map[string]string{
		filepath.Join(coreDirPath, "AGENT.md"): strings.TrimSpace(`
---
description: Core runtime behavior guidance for the assistant; keep concise and actionable.
limit: 6000
---

# AGENT.md

## Role

- You are {{agent_name}}, a pragmatic software assistant.
- Prioritize correctness, clarity, and concrete outcomes.

## Collaboration

- Be direct and concise by default.
- Explain tradeoffs when decisions matter.
- Surface uncertainty explicitly and verify when needed.

## Safety

- Avoid destructive actions without clear intent.
- Respect privacy and do not expose secrets.
`),
		filepath.Join(coreDirPath, "USER.md"): strings.TrimSpace(`
---
description: Durable user profile and interaction preferences.
limit: 6000
---

# USER.md

## Identity

- Preferred name: unknown
- Timezone: unknown

## Communication Preferences

- Tone: unknown
- Verbosity: unknown
- Formatting preferences: unknown

## Long-Term Notes

- (Add durable user preferences and constraints here.)
`),
		filepath.Join(coreDirPath, "SOUL.md"): strings.TrimSpace(`
---
description: Evolving assistant personality, voice, and behavioral principles.
limit: 6000
---

# SOUL.md

## Voice

- Practical, calm, and technically rigorous.
- Confident without overclaiming.

## Principles

- Prefer useful action over performative language.
- Keep context organized so future sessions stay coherent.
- Update this file as behavior evolves.
`),
	}

	for relativePath, content := range files {
		path := filepath.Join(s.rootDir, relativePath)
		if _, err := os.Stat(path); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("stat core memory file %q: %w", path, err)
		}

		if err := writeTextFileAtomic(path, content+"\n"); err != nil {
			return fmt.Errorf("initialize core memory file %q: %w", path, err)
		}
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

func (s *Store) loadCoreFiles() ([]agent.CoreMemoryFile, error) {
	base := filepath.Join(s.rootDir, coreDirPath)

	entries, err := os.ReadDir(base)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read core directory: %w", err)
	}

	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			paths = append(paths, filepath.Join(base, entry.Name()))
		}
	}

	sort.Strings(paths)
	out := make([]agent.CoreMemoryFile, 0, len(paths))
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read core file %q: %w", path, err)
		}
		description, limit, body := parseMarkdownFrontmatter(string(raw))
		relative, err := filepath.Rel(s.rootDir, path)
		if err != nil {
			return nil, fmt.Errorf("resolve relative core path %q: %w", path, err)
		}
		out = append(out, agent.CoreMemoryFile{
			RelativePath: filepath.ToSlash(relative),
			Description:  s.renderCoreTemplate(description),
			Limit:        limit,
			Content:      s.renderCoreTemplate(body),
		})
	}

	return out, nil
}

func parseMarkdownFrontmatter(raw string) (description string, limit int, body string) {
	normalized := strings.ReplaceAll(raw, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	normalized = strings.TrimSpace(normalized)
	if normalized == "" {
		return "", 0, ""
	}

	body = stripYAMLFrontmatter(normalized)

	ctx := parser.NewContext()
	coreFrontmatterParser.Parser().Parse(
		text.NewReader([]byte(normalized)),
		parser.WithContext(ctx),
	)

	values, err := meta.TryGet(ctx)
	if err != nil || values == nil {
		return "", 0, body
	}

	if value, ok := values["description"].(string); ok {
		description = strings.TrimSpace(value)
	}
	if description == "" {
		if value, ok := values["Description"].(string); ok {
			description = strings.TrimSpace(value)
		}
	}

	switch value := values["limit"].(type) {
	case int:
		limit = value
	case int64:
		limit = int(value)
	case float64:
		limit = int(value)
	case string:
		n, convErr := strconv.Atoi(strings.TrimSpace(value))
		if convErr == nil {
			limit = n
		}
	}
	if limit == 0 {
		switch value := values["Limit"].(type) {
		case int:
			limit = value
		case int64:
			limit = int(value)
		case float64:
			limit = int(value)
		case string:
			n, convErr := strconv.Atoi(strings.TrimSpace(value))
			if convErr == nil {
				limit = n
			}
		}
	}

	return description, limit, body
}

func (s *Store) renderCoreTemplate(raw string) string {
	if raw == "" {
		return ""
	}
	return strings.ReplaceAll(raw, "{{agent_name}}", s.agentName)
}

func normalizeAgentName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "q15"
	}
	return name
}

func stripYAMLFrontmatter(normalized string) string {
	rest, ok := strings.CutPrefix(normalized, "---\n")
	if !ok {
		return normalized
	}

	for {
		line, next, hasNewline := strings.Cut(rest, "\n")
		if isYAMLSeparator(line) {
			return strings.TrimSpace(next)
		}
		if !hasNewline {
			return normalized
		}
		rest = next
	}
}

func isYAMLSeparator(line string) bool {
	line = strings.TrimSpace(line)
	return line != "" && strings.Trim(line, "-") == ""
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
