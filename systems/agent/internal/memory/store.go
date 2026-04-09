package memory

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/yuin/goldmark"
	meta "github.com/yuin/goldmark-meta"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
)

const (
	headStateRelativePath               = "history/state/head.json"
	consolidationCheckpointRelativePath = "history/state/consolidation_checkpoint.json"
	readmeRelativePath                  = "README.md"
	seedDirPath                         = "seeds"
	coreDirPath                         = "core"
	semanticDirPath                     = "semantic"
	workingDirPath                      = "working"
	workingMemoryFileName               = "WORKING_MEMORY.md"
	cognitionDirPath                    = "cognition"
	cognitionStatePath                  = cognitionDirPath + "/state"
	cognitionIndexerPath                = cognitionDirPath + "/indexer"
	cognitionRunsPath                   = cognitionDirPath + "/runs"
	cognitionTriggersPath               = cognitionDirPath + "/triggers"
	cognitionJobsPath                   = cognitionTriggersPath + "/jobs"
)

const workingMemorySeedPath = seedDirPath + "/" + workingMemoryFileName

//go:embed seeds/*.md
var seedFS embed.FS

var coreSeedPaths = map[string]string{
	filepath.Join(coreDirPath, "AGENT.md"): seedDirPath + "/AGENT.md",
	filepath.Join(coreDirPath, "USER.md"):  seedDirPath + "/USER.md",
	filepath.Join(coreDirPath, "SOUL.md"):  seedDirPath + "/SOUL.md",
}

var coreFrontmatterParser = goldmark.New(
	goldmark.WithExtensions(meta.Meta),
)

// Store persists the agent's episodic history, core self-model files, and
// related memory state on disk.
type Store struct {
	mu        sync.Mutex
	rootDir   string
	agentName string
	committer Committer
}

var _ agent.ConversationStore = (*Store)(nil)
var _ agent.CoreMemoryStore = (*Store)(nil)
var _ agent.WorkingMemoryStore = (*Store)(nil)

// NewStore constructs a memory store rooted at the provided directory.
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

// Init creates the on-disk memory scaffold and initializes git tracking.
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
		filepath.Join(s.rootDir, coreDirPath),
		filepath.Join(s.rootDir, semanticDirPath),
		filepath.Join(s.rootDir, workingDirPath),
		filepath.Join(s.rootDir, "history", "turns"),
		filepath.Join(s.rootDir, "history", "state"),
		filepath.Join(s.rootDir, "notes", "inbox"),
		filepath.Join(s.rootDir, "notes", "zettel"),
		filepath.Join(s.rootDir, "notes", "maps"),
		filepath.Join(s.rootDir, cognitionStatePath),
		filepath.Join(s.rootDir, cognitionIndexerPath),
		filepath.Join(s.rootDir, cognitionRunsPath),
		filepath.Join(s.rootDir, cognitionJobsPath),
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
	if err := s.ensureWorkingMemory(); err != nil {
		return err
	}
	if err := s.ensureHeadState(); err != nil {
		return err
	}
	if err := s.ensureConsolidationCheckpoint(); err != nil {
		return err
	}

	upgrade, err := s.upgradeHistory()
	if err != nil {
		return fmt.Errorf("upgrade transcript history: %w", err)
	}
	if upgrade.Upgraded > 0 || upgrade.Quarantined > 0 {
		log.Printf(
			"q15: transcript history upgrade finished (upgraded=%d quarantined=%d)",
			upgrade.Upgraded,
			upgrade.Quarantined,
		)
	}
	headSynced, err := s.syncHeadStateWithHistory()
	if err != nil {
		return fmt.Errorf("synchronize memory head state: %w", err)
	}

	if err := s.committer.EnsureRepo(ctx, s.rootDir); err != nil {
		return fmt.Errorf("ensure git memory repo: %w", err)
	}
	commitMessage := "memory: initialize repository"
	if upgrade.Upgraded > 0 || upgrade.Quarantined > 0 {
		commitMessage = fmt.Sprintf(
			"memory: upgrade transcript history to v%d",
			conversation.SchemaVersion,
		)
	} else if headSynced {
		commitMessage = "memory: synchronize transcript head state"
	}
	if _, err := s.committer.CommitAll(ctx, s.rootDir, commitMessage); err != nil {
		return fmt.Errorf("commit memory changes: %w", err)
	}

	return nil
}

// LoadRecentMessages loads the bounded unconsolidated replay slice used for
// prompt-visible episodic replay.
func (s *Store) LoadRecentMessages(ctx context.Context, turns int) ([]conversation.Message, error) {
	_ = ctx
	if turns <= 0 {
		return nil, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := s.listTurnEntries()
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, nil
	}

	checkpoint, err := s.readConsolidationCheckpoint()
	if err != nil {
		return nil, err
	}

	var selected []turnPathEntry
	if checkpoint.LastConsolidatedSeq <= 0 {
		start := max(0, len(entries)-turns)
		selected = entries[start:]
	} else {
		start := sort.Search(len(entries), func(i int) bool {
			return entries[i].Seq > checkpoint.LastConsolidatedSeq
		})
		if start == len(entries) {
			return nil, nil
		}
		selected = entries[start:]
		if len(selected) > turns {
			selected = selected[len(selected)-turns:]
		}
	}

	records := make([]turnRecord, 0, len(selected))
	for _, entry := range selected {
		turn, err := s.readTurn(entry.Path)
		if err != nil {
			return nil, err
		}
		records = append(records, turn)
	}

	out := make([]conversation.Message, 0, len(records)*2)
	for _, turn := range records {
		out = append(out, copyMessages(turn.Messages)...)
	}
	return out, nil
}

// LoadCoreMemory loads the current core self-model files for prompt injection.
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

// LoadWorkingMemory loads the canonical prompt-visible working-memory artifact.
func (s *Store) LoadWorkingMemory(ctx context.Context) (agent.WorkingMemory, error) {
	_ = ctx

	s.mu.Lock()
	defer s.mu.Unlock()

	path := s.workingMemoryPath()
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return agent.WorkingMemory{}, nil
		}
		return agent.WorkingMemory{}, fmt.Errorf("read working memory file %q: %w", path, err)
	}

	relative, err := filepath.Rel(s.rootDir, path)
	if err != nil {
		return agent.WorkingMemory{}, fmt.Errorf(
			"resolve relative working memory path %q: %w",
			path,
			err,
		)
	}
	return agent.WorkingMemory{
		RelativePath: filepath.ToSlash(relative),
		Content:      strings.TrimSpace(string(raw)),
	}, nil
}

// AppendTurn persists one completed conversation turn and commits it to git.
func (s *Store) AppendTurn(ctx context.Context, messages []conversation.Message) error {
	if len(messages) == 0 {
		return nil
	}

	messages = sanitizeStoredMessages(copyMessages(messages))
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
		SchemaVersion: conversation.SchemaVersion,
		ID:            fmt.Sprintf("turn-%020d", seq),
		Seq:           seq,
		CreatedAt:     now,
		Messages:      copyMessages(messages),
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

This directory contains q15's persistent agent-state root.

	- Core self-model files (always injected into the system prompt) are stored in core/*.md (for example AGENT.md, USER.md, SOUL.md).
	- Agent identity comes from config agent.name; use {{agent_name}} in core files instead of hardcoded names.
	- Semantic memory is stored under semantic/ for durable extracted knowledge the agent knows; it is not auto-injected.
	- The canonical prompt-visible working-memory artifact is working/WORKING_MEMORY.md for bounded active state.
	- Other files under working/ are not implicitly prompt-visible and notes/ is never working memory.
	- Episodic conversation turns are stored as canonical JSON files under history/turns/.
	- Transcript sequence bookkeeping is stored under history/state/head.json.
	- Replay checkpoints for consolidated episodic history are stored under history/state/consolidation_checkpoint.json.
	- Cognition subsystem maintenance state is stored under cognition/.
	- Job-owned cognition artifacts are stored under cognition/state/.
	- Per-job cognition trigger state is stored under cognition/triggers/jobs/.
	- Append-only cognition run provenance is stored under cognition/runs/.
	- Auxiliary notebook files are organized under notes/inbox, notes/zettel, and notes/maps as the built-in zettelkasten layout.
	- Git history tracks all memory changes.
`)
	if err := writeTextFileAtomic(path, content+"\n"); err != nil {
		return fmt.Errorf("write memory README: %w", err)
	}
	return nil
}

func (s *Store) ensureCoreMemory() error {
	for relativePath, seedPath := range coreSeedPaths {
		if err := s.ensureSeedFile(relativePath, seedPath); err != nil {
			return fmt.Errorf("initialize core memory seed %q: %w", seedPath, err)
		}
	}

	return nil
}

func (s *Store) ensureWorkingMemory() error {
	if err := s.ensureSeedFile(
		filepath.Join(workingDirPath, workingMemoryFileName),
		workingMemorySeedPath,
	); err != nil {
		return fmt.Errorf("initialize working memory seed %q: %w", workingMemorySeedPath, err)
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

func (s *Store) ensureConsolidationCheckpoint() error {
	path := s.consolidationCheckpointPath()
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat consolidation checkpoint: %w", err)
	}

	checkpoint := consolidationCheckpointState{
		UpdatedAt: time.Now().UTC(),
	}
	if err := writeJSONFileAtomic(path, checkpoint); err != nil {
		return fmt.Errorf("initialize consolidation checkpoint: %w", err)
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

func readEmbeddedSeed(path string) (string, error) {
	raw, err := seedFS.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}

func (s *Store) ensureSeedFile(relativePath, seedPath string) error {
	content, err := readEmbeddedSeed(seedPath)
	if err != nil {
		return fmt.Errorf("read embedded seed %q: %w", seedPath, err)
	}

	path := filepath.Join(s.rootDir, relativePath)
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat seeded file %q: %w", path, err)
	}

	if err := writeTextFileAtomic(path, content+"\n"); err != nil {
		return fmt.Errorf("write seeded file %q: %w", path, err)
	}
	return nil
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

func (s *Store) listTurnEntries() ([]turnPathEntry, error) {
	paths, err := s.listTurnPaths()
	if err != nil {
		return nil, err
	}

	entries := make([]turnPathEntry, 0, len(paths))
	for _, path := range paths {
		seq, err := turnSeqFromPath(path)
		if err != nil {
			record, err := s.readTurn(path)
			if err != nil {
				return nil, err
			}
			seq = record.Seq
		}
		entries = append(entries, turnPathEntry{
			Path: path,
			Seq:  seq,
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Seq == entries[j].Seq {
			return entries[i].Path < entries[j].Path
		}
		return entries[i].Seq < entries[j].Seq
	})
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
	if record.SchemaVersion != conversation.SchemaVersion {
		return turnRecord{}, fmt.Errorf(
			"turn record %q has unsupported schema_version %d",
			path,
			record.SchemaVersion,
		)
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

func (s *Store) readConsolidationCheckpoint() (consolidationCheckpointState, error) {
	data, err := os.ReadFile(s.consolidationCheckpointPath())
	if err != nil {
		if os.IsNotExist(err) {
			return consolidationCheckpointState{}, nil
		}
		return consolidationCheckpointState{}, fmt.Errorf("read consolidation checkpoint: %w", err)
	}

	var checkpoint consolidationCheckpointState
	if err := json.Unmarshal(data, &checkpoint); err != nil {
		return consolidationCheckpointState{}, fmt.Errorf(
			"decode consolidation checkpoint: %w",
			err,
		)
	}
	return checkpoint, nil
}

func (s *Store) headStatePath() string {
	return filepath.Join(s.rootDir, headStateRelativePath)
}

func (s *Store) consolidationCheckpointPath() string {
	return filepath.Join(s.rootDir, consolidationCheckpointRelativePath)
}

func (s *Store) workingMemoryPath() string {
	return filepath.Join(s.rootDir, workingDirPath, workingMemoryFileName)
}

func (s *Store) syncHeadStateWithHistory() (bool, error) {
	entries, err := s.listTurnEntries()
	if err != nil {
		return false, err
	}
	maxSeq := int64(0)
	if len(entries) > 0 {
		maxSeq = entries[len(entries)-1].Seq
	}

	head, err := s.readHeadState()
	if err != nil {
		return false, err
	}
	if head.LastSeq >= maxSeq {
		return false, nil
	}

	head.LastSeq = maxSeq
	head.UpdatedAt = time.Now().UTC()
	if err := writeJSONFileAtomic(s.headStatePath(), head); err != nil {
		return false, fmt.Errorf("write synchronized memory head state: %w", err)
	}
	return true, nil
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

func turnSeqFromPath(path string) (int64, error) {
	base := filepath.Base(path)
	if !strings.EqualFold(filepath.Ext(base), ".json") {
		return 0, fmt.Errorf("turn path %q must end with .json", path)
	}
	name := strings.TrimSuffix(base, filepath.Ext(base))
	seq, err := strconv.ParseInt(name, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse turn seq from path %q: %w", path, err)
	}
	if seq < 0 {
		return 0, fmt.Errorf("turn path %q has negative seq %d", path, seq)
	}
	return seq, nil
}

func copyMessages(in []conversation.Message) []conversation.Message {
	return conversation.CloneMessages(sanitizeStoredMessages(in))
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
