// Package memory owns authored identity files. Conversation state lives in Postgres.
package memory

import (
	"context"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/atomicfile"
	"github.com/q15co/q15/systems/agent/internal/memoryrepo"
	"github.com/yuin/goldmark"
	meta "github.com/yuin/goldmark-meta"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
)

const coreDirPath = "core"

//go:embed seeds/*.md
var seedFS embed.FS
var coreSeedPaths = map[string]string{
	"core/AGENT.md": "seeds/AGENT.md", "core/USER.md": "seeds/USER.md", "core/SOUL.md": "seeds/SOUL.md",
}
var coreFrontmatterParser = goldmark.New(goldmark.WithExtensions(meta.Meta))

// Store supplies identity and serializes file-tool mutations.
type Store struct {
	repository *memoryrepo.Repository
	agentName  string
}

// Init initializes only authored identity and notebook directories.
func (s *Store) Init(ctx context.Context) error {
	if s == nil || s.repository == nil {
		return fmt.Errorf("identity repository is required")
	}
	if err := s.repository.Init(ctx); err != nil {
		return err
	}
	release := s.repository.Acquire()
	defer release()
	for _, directory := range []string{"core", "notes"} {
		if err := os.MkdirAll(filepath.Join(s.root(), directory), 0755); err != nil {
			return err
		}
	}
	if err := s.ensureCoreMemory(); err != nil {
		return err
	}
	return s.repository.Commit(ctx, "memory: initialize authored identity", "core", "notes")
}

// NewStore opens authored identity through its repository boundary.
func NewStore(
	repository *memoryrepo.Repository,
	agentName string,
) *Store {
	return &Store{
		repository: repository,
		agentName:  normalizeAgentName(agentName),
	}
}

func (s *Store) root() string {
	if s == nil || s.repository == nil {
		return ""
	}
	return s.repository.Root()
}

// LoadCoreMemory renders authored templates without changing their source files.
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

func (s *Store) ensureCoreMemory() error {
	for relativePath, seedPath := range coreSeedPaths {
		if err := s.ensureSeedFile(relativePath, seedPath); err != nil {
			return fmt.Errorf("initialize core memory seed %q: %w", seedPath, err)
		}
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

func writeTextFileAtomic(path, text string) error {
	return atomicfile.WriteBytes(path, []byte(text))
}
