// Package memory persists agent conversation, cognition, and authored memory
// state in the Git-backed memory repository.
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
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/atomicfile"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/memoryrepo"
	"github.com/yuin/goldmark"
	meta "github.com/yuin/goldmark-meta"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
)

const (
	headStateRelativePath                    = "history/state/head.json"
	consolidationCheckpointRelativePath      = "history/state/consolidation_checkpoint.json"
	semanticExtractionCheckpointRelativePath = "cognition/state/semantic_extraction_checkpoint.json"
	readmeRelativePath                       = "README.md"
	seedDirPath                              = "seeds"
	coreDirPath                              = "core"
	semanticDirPath                          = "semantic"
	workingDirPath                           = "working"
	workingMemoryFileName                    = "WORKING_MEMORY.md"
	cognitionDirPath                         = "cognition"
	cognitionStatePath                       = cognitionDirPath + "/state"
	cognitionIndexerPath                     = cognitionDirPath + "/indexer"
	cognitionRunsPath                        = cognitionDirPath + "/runs"
	cognitionTriggersPath                    = cognitionDirPath + "/triggers"
	cognitionJobsPath                        = cognitionTriggersPath + "/jobs"
)

const workingMemorySeedPath = seedDirPath + "/" + workingMemoryFileName

const (
	semanticFactsFileName       = "facts.md"
	semanticPreferencesFileName = "preferences.md"
	semanticProjectsFileName    = "projects.md"
)

const (
	semanticFactsRelativePath       = semanticDirPath + "/" + semanticFactsFileName
	semanticPreferencesRelativePath = semanticDirPath + "/" + semanticPreferencesFileName
	semanticProjectsRelativePath    = semanticDirPath + "/" + semanticProjectsFileName
)

const (
	semanticFactsSeedPath       = seedDirPath + "/" + semanticFactsFileName
	semanticPreferencesSeedPath = seedDirPath + "/" + semanticPreferencesFileName
	semanticProjectsSeedPath    = seedDirPath + "/" + semanticProjectsFileName
)

//go:embed seeds/*.md
var seedFS embed.FS

var coreSeedPaths = map[string]string{
	filepath.Join(coreDirPath, "AGENT.md"): seedDirPath + "/AGENT.md",
	filepath.Join(coreDirPath, "USER.md"):  seedDirPath + "/USER.md",
	filepath.Join(coreDirPath, "SOUL.md"):  seedDirPath + "/SOUL.md",
}

var semanticSeedPaths = []struct {
	relativePath string
	seedPath     string
}{
	{
		relativePath: semanticFactsRelativePath,
		seedPath:     semanticFactsSeedPath,
	},
	{
		relativePath: semanticPreferencesRelativePath,
		seedPath:     semanticPreferencesSeedPath,
	},
	{
		relativePath: semanticProjectsRelativePath,
		seedPath:     semanticProjectsSeedPath,
	},
}

var memoryCommitPaths = []string{
	readmeRelativePath,
	coreDirPath,
	semanticDirPath,
	workingDirPath,
	"history",
	"notes",
	cognitionDirPath,
}

var coreFrontmatterParser = goldmark.New(
	goldmark.WithExtensions(meta.Meta),
)

// Store persists the agent's episodic history, core self-model files, and
// related memory state on disk.
type Store struct {
	repository *memoryrepo.Repository
	agentName  string
}

var _ agent.ConversationStore = (*Store)(nil)
var _ agent.CoreMemoryStore = (*Store)(nil)
var _ agent.SemanticMemoryStore = (*Store)(nil)
var _ agent.WorkingMemoryStore = (*Store)(nil)

// NewStore constructs a memory store over its Git-backed repository.
func NewStore(
	repository *memoryrepo.Repository,
	agentName string,
) *Store {
	return &Store{
		repository: repository,
		agentName:  normalizeAgentName(agentName),
	}
}

// Init creates the on-disk memory scaffold and initializes git tracking.
func (s *Store) Init(ctx context.Context) error {
	if s == nil || s.repository == nil {
		return fmt.Errorf("memory repository is required")
	}
	if err := s.repository.Init(ctx); err != nil {
		return err
	}

	release := s.repository.Acquire()
	defer release()

	root := s.root()
	dirs := []string{
		filepath.Join(root, coreDirPath),
		filepath.Join(root, semanticDirPath),
		filepath.Join(root, workingDirPath),
		filepath.Join(root, "history", "turns"),
		filepath.Join(root, "history", "state"),
		filepath.Join(root, "notes", "inbox"),
		filepath.Join(root, "notes", "zettel"),
		filepath.Join(root, "notes", "maps"),
		filepath.Join(root, cognitionStatePath),
		filepath.Join(root, cognitionIndexerPath),
		filepath.Join(root, cognitionRunsPath),
		filepath.Join(root, cognitionJobsPath),
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
	if err := s.ensureSemanticMemory(); err != nil {
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
	if err := s.ensureSemanticExtractionCheckpoint(); err != nil {
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

	commitMessage := "memory: initialize repository"
	if upgrade.Upgraded > 0 || upgrade.Quarantined > 0 {
		commitMessage = fmt.Sprintf(
			"memory: upgrade transcript history to v%d",
			conversation.SchemaVersion,
		)
	} else if headSynced {
		commitMessage = "memory: synchronize transcript head state"
	}
	if err := s.repository.Commit(
		ctx,
		commitMessage,
		memoryCommitPaths...,
	); err != nil {
		return fmt.Errorf("commit memory changes: %w", err)
	}

	return nil
}

func (s *Store) root() string {
	if s == nil || s.repository == nil {
		return ""
	}
	return s.repository.Root()
}

// LoadRecentMessages loads the bounded unconsolidated replay slice used for
// prompt-visible episodic replay.
func (s *Store) LoadRecentMessages(ctx context.Context, turns int) ([]conversation.Message, error) {
	_ = ctx
	if turns <= 0 {
		return nil, nil
	}

	release := s.repository.Acquire()
	defer release()

	return s.loadMessagesLocked(turns, true)
}

// LoadLatestMessages loads the bounded latest-turn replay slice without using
// the working-memory consolidation checkpoint.
func (s *Store) LoadLatestMessages(ctx context.Context, turns int) ([]conversation.Message, error) {
	_ = ctx
	if turns <= 0 {
		return nil, nil
	}

	release := s.repository.Acquire()
	defer release()

	return s.loadMessagesLocked(turns, false)
}

// LoadMessagesSinceSeq loads all messages from turns after the provided
// transcript sequence boundary.
func (s *Store) LoadMessagesSinceSeq(
	ctx context.Context,
	afterSeq int64,
) ([]conversation.Message, error) {
	_ = ctx

	release := s.repository.Acquire()
	defer release()

	return s.loadMessagesSinceSeqLocked(afterSeq)
}

// LoadLastUserTimestamp returns the most recent persisted user-message
// timestamp carried in canonical message metadata.
func (s *Store) LoadLastUserTimestamp(
	ctx context.Context,
) (time.Time, bool, error) {
	_ = ctx

	release := s.repository.Acquire()
	defer release()

	entries, err := s.listTurnEntries()
	if err != nil {
		return time.Time{}, false, err
	}
	for i := len(entries) - 1; i >= 0; i-- {
		turn, err := s.readTurn(entries[i].Path)
		if err != nil {
			return time.Time{}, false, err
		}
		for j := len(turn.Messages) - 1; j >= 0; j-- {
			if timestamp, ok := conversation.UserMessageTimeLocal(turn.Messages[j]); ok {
				return timestamp, true, nil
			}
		}
	}
	return time.Time{}, false, nil
}

// LoadCoreMemory loads the current core self-model files for prompt injection.
func (s *Store) LoadCoreMemory(ctx context.Context) (agent.CoreMemory, error) {
	_ = ctx

	release := s.repository.Acquire()
	defer release()

	files, err := s.loadCoreFiles()
	if err != nil {
		return agent.CoreMemory{}, err
	}
	return agent.CoreMemory{
		Files: files,
	}, nil
}

// LoadSemanticMemory loads the canonical durable semantic-memory files in
// stable semantic order.
func (s *Store) LoadSemanticMemory(ctx context.Context) (agent.SemanticMemory, error) {
	_ = ctx

	release := s.repository.Acquire()
	defer release()

	out := make([]agent.SemanticMemoryFile, 0, len(semanticSeedPaths))
	for _, file := range semanticSeedPaths {
		content, err := s.loadSeededMemoryFile(file.relativePath, file.seedPath)
		if err != nil {
			return agent.SemanticMemory{}, err
		}
		out = append(out, agent.SemanticMemoryFile{
			RelativePath: file.relativePath,
			Content:      content,
		})
	}

	return agent.SemanticMemory{
		Files: out,
	}, nil
}

// LoadWorkingMemory loads the canonical prompt-visible working-memory artifact.
func (s *Store) LoadWorkingMemory(ctx context.Context) (agent.WorkingMemory, error) {
	_ = ctx

	release := s.repository.Acquire()
	defer release()

	path := s.workingMemoryPath()
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return agent.WorkingMemory{}, nil
		}
		return agent.WorkingMemory{}, fmt.Errorf("read working memory file %q: %w", path, err)
	}

	relative, err := filepath.Rel(s.root(), path)
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

	release := s.repository.Acquire()
	defer release()

	return s.appendTurnLocked(ctx, withoutExternalEventMetadata(messages))
}

// RecordDeliveredAssistantEvent idempotently appends an externally delivered
// assistant event to the canonical transcript. Callers must invoke this only
// after the delivery transport acknowledges the exact event text.
func (s *Store) RecordDeliveredAssistantEvent(
	ctx context.Context,
	event conversation.DeliveredAssistantEvent,
) error {
	if strings.TrimSpace(event.Text) == "" {
		return fmt.Errorf("delivered assistant event text is required")
	}

	event.Metadata = conversation.NormalizeExternalEventMetadata(event.Metadata)
	if !event.Metadata.Valid() {
		return fmt.Errorf("delivered assistant event metadata is incomplete or invalid")
	}

	release := s.repository.Acquire()
	defer release()

	recordedSeq, recorded, err := s.findDeliveredAssistantEventLocked(event.Metadata)
	if err != nil {
		return err
	}
	if recorded {
		return s.reconcileDeliveredAssistantEventLocked(ctx, recordedSeq)
	}

	return s.appendTurnLocked(
		ctx,
		[]conversation.Message{conversation.DeliveredAssistantEventMessage(event)},
	)
}

func (s *Store) appendTurnLocked(
	ctx context.Context,
	messages []conversation.Message,
) error {
	messages = sanitizeStoredMessages(copyMessages(messages))
	if len(messages) == 0 {
		return nil
	}

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

	if err := s.repository.Commit(
		ctx,
		fmt.Sprintf("memory: append turn %d", seq),
		memoryCommitPaths...,
	); err != nil {
		return fmt.Errorf("commit memory turn %d: %w", seq, err)
	}

	return nil
}

func (s *Store) findDeliveredAssistantEventLocked(
	metadata conversation.ExternalEventMetadata,
) (int64, bool, error) {
	entries, err := s.listTurnEntries()
	if err != nil {
		return 0, false, err
	}

	for i := len(entries) - 1; i >= 0; i-- {
		turn, err := s.readTurn(entries[i].Path)
		if err != nil {
			return 0, false, err
		}
		for _, message := range turn.Messages {
			existing := message.ExternalEvent
			if existing == nil {
				continue
			}
			normalized := conversation.NormalizeExternalEventMetadata(*existing)
			if normalized.Source == metadata.Source &&
				normalized.JobID == metadata.JobID &&
				normalized.RunID == metadata.RunID {
				return turn.Seq, true, nil
			}
		}
	}
	return 0, false, nil
}

func (s *Store) reconcileDeliveredAssistantEventLocked(
	ctx context.Context,
	recordedSeq int64,
) error {
	if _, err := s.syncHeadStateWithHistory(); err != nil {
		return fmt.Errorf("reconcile delivered assistant event head state: %w", err)
	}

	if err := s.repository.Commit(
		ctx,
		fmt.Sprintf("memory: reconcile delivered assistant event turn %d", recordedSeq),
		memoryCommitPaths...,
	); err != nil {
		return fmt.Errorf(
			"commit reconciled delivered assistant event turn %d: %w",
			recordedSeq,
			err,
		)
	}
	return nil
}

func (s *Store) ensureREADME() error {
	path := filepath.Join(s.root(), readmeRelativePath)
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat memory README: %w", err)
	}

	content := strings.TrimSpace(`
# q15 Agent Memory

This directory contains q15's Git-backed agent memory root.

	- Core self-model files (always injected into the system prompt) are stored in core/*.md (for example AGENT.md, USER.md, SOUL.md).
	- Agent identity comes from config agent.name; use {{agent_name}} in core files instead of hardcoded names.
	- Semantic memory is stored under semantic/ for durable extracted knowledge the agent knows; its canonical editable files are semantic/facts.md, semantic/preferences.md, and semantic/projects.md.
	- Semantic memory is tool-fetched for cognition jobs and is not auto-injected into reply prompts.
	- The canonical prompt-visible working-memory artifact is working/WORKING_MEMORY.md for bounded active state.
	- Other files under working/ are not implicitly prompt-visible and notes/ is never working memory.
	- Episodic conversation turns are stored as canonical JSON files under history/turns/.
	- Transcript sequence bookkeeping is stored under history/state/head.json.
	- Replay checkpoints for consolidated episodic history are stored under history/state/consolidation_checkpoint.json.
	- Cognition subsystem maintenance state is stored under cognition/.
	- Job-owned cognition artifacts are stored under cognition/state/.
	- Semantic extraction replay checkpoints are stored under cognition/state/semantic_extraction_checkpoint.json.
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

func (s *Store) ensureSemanticMemory() error {
	for _, file := range semanticSeedPaths {
		if err := s.ensureSeedFile(file.relativePath, file.seedPath); err != nil {
			return fmt.Errorf(
				"initialize semantic memory seed %q: %w",
				file.seedPath,
				err,
			)
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

func (s *Store) ensureSemanticExtractionCheckpoint() error {
	path := s.semanticExtractionCheckpointPath()
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat semantic extraction checkpoint: %w", err)
	}

	checkpoint := semanticExtractionCheckpointState{
		UpdatedAt: time.Now().UTC(),
	}
	if err := writeJSONFileAtomic(path, checkpoint); err != nil {
		return fmt.Errorf("initialize semantic extraction checkpoint: %w", err)
	}
	return nil
}

func (s *Store) loadCoreFiles() ([]agent.CoreMemoryFile, error) {
	base := filepath.Join(s.root(), coreDirPath)

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
		relative, err := filepath.Rel(s.root(), path)
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

func (s *Store) loadSeededMemoryFile(relativePath, seedPath string) (string, error) {
	path := filepath.Join(s.root(), filepath.FromSlash(relativePath))
	raw, err := os.ReadFile(path)
	if err == nil {
		return strings.TrimSpace(string(raw)), nil
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("read semantic memory file %q: %w", path, err)
	}

	content, err := readEmbeddedSeed(seedPath)
	if err != nil {
		return "", fmt.Errorf("read embedded seed %q: %w", seedPath, err)
	}
	return content, nil
}

func (s *Store) ensureSeedFile(relativePath, seedPath string) error {
	content, err := readEmbeddedSeed(seedPath)
	if err != nil {
		return fmt.Errorf("read embedded seed %q: %w", seedPath, err)
	}

	path := filepath.Join(s.root(), relativePath)
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
	base := filepath.Join(s.root(), "history", "turns")
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

func (s *Store) readSemanticExtractionCheckpoint() (semanticExtractionCheckpointState, error) {
	data, err := os.ReadFile(s.semanticExtractionCheckpointPath())
	if err != nil {
		if os.IsNotExist(err) {
			return semanticExtractionCheckpointState{}, nil
		}
		return semanticExtractionCheckpointState{}, fmt.Errorf(
			"read semantic extraction checkpoint: %w",
			err,
		)
	}

	var checkpoint semanticExtractionCheckpointState
	if err := json.Unmarshal(data, &checkpoint); err != nil {
		return semanticExtractionCheckpointState{}, fmt.Errorf(
			"decode semantic extraction checkpoint: %w",
			err,
		)
	}
	return checkpoint, nil
}

func (s *Store) loadMessagesLocked(
	turns int,
	checkpointAware bool,
) ([]conversation.Message, error) {
	entries, err := s.listTurnEntries()
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, nil
	}

	var selected []turnPathEntry
	if checkpointAware {
		checkpoint, err := s.readConsolidationCheckpoint()
		if err != nil {
			return nil, err
		}
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
	} else {
		start := max(0, len(entries)-turns)
		selected = entries[start:]
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
		out = append(out, promptVisibleMessages(turn.Messages)...)
	}
	return out, nil
}

func (s *Store) loadMessagesSinceSeqLocked(
	afterSeq int64,
) ([]conversation.Message, error) {
	entries, err := s.listTurnEntries()
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, nil
	}

	start := sort.Search(len(entries), func(i int) bool {
		return entries[i].Seq > afterSeq
	})
	if start == len(entries) {
		return nil, nil
	}

	records := make([]turnRecord, 0, len(entries)-start)
	for _, entry := range entries[start:] {
		turn, err := s.readTurn(entry.Path)
		if err != nil {
			return nil, err
		}
		records = append(records, turn)
	}

	out := make([]conversation.Message, 0, len(records)*2)
	for _, turn := range records {
		out = append(out, promptVisibleMessages(turn.Messages)...)
	}
	return out, nil
}

func (s *Store) headStatePath() string {
	return filepath.Join(s.root(), headStateRelativePath)
}

func (s *Store) consolidationCheckpointPath() string {
	return filepath.Join(s.root(), consolidationCheckpointRelativePath)
}

func (s *Store) semanticExtractionCheckpointPath() string {
	return filepath.Join(s.root(), semanticExtractionCheckpointRelativePath)
}

func (s *Store) workingMemoryPath() string {
	return filepath.Join(s.root(), workingDirPath, workingMemoryFileName)
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
		s.root(),
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

func promptVisibleMessages(in []conversation.Message) []conversation.Message {
	stored := copyMessages(in)
	out := make([]conversation.Message, 0, len(stored))
	for _, message := range stored {
		out = append(
			out,
			conversation.PromptVisibleExternalEventMessages(message)...,
		)
	}
	return out
}

// withoutExternalEventMetadata enforces RecordDeliveredAssistantEvent as the
// only persistence path that may establish trusted delivery provenance.
// Ordinary turns ultimately contain model-authored messages, so even
// well-formed metadata on that path must be treated as untrusted.
func withoutExternalEventMetadata(
	in []conversation.Message,
) []conversation.Message {
	out := conversation.CloneMessages(in)
	for i := range out {
		out[i].ExternalEvent = nil
	}
	return out
}

func writeJSONFileAtomic(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal JSON for %q: %w", path, err)
	}
	data = append(data, '\n')
	return atomicfile.WriteBytes(path, data)
}

func writeTextFileAtomic(path, text string) error {
	return atomicfile.WriteBytes(path, []byte(text))
}

func writeBytesFileAtomic(path string, data []byte) error {
	return atomicfile.WriteBytes(path, data)
}
