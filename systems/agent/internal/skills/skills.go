// Package skills manages builtin and shared skills for the agent.
package skills

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/q15co/q15/systems/agent/internal/fileops"
	"github.com/yuin/goldmark"
	meta "github.com/yuin/goldmark-meta"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
)

// DefaultContainerDir and BuiltinNamespace define the container-visible skill roots.
const (
	DefaultContainerDir    = "/skills"
	BuiltinNamespace       = "@builtin"
	skillFileName          = "SKILL.md"
	defaultReadLimitLines  = 400
	maxReadLimitLines      = 400
	maxReadBytes           = 16 * 1024
	maxDescriptionChars    = 1024
	maxSkillNameChars      = 63
	maxDeclaredToolNameLen = 64
	maxDeclaredTools       = 32
)

var (
	skillNameRE = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	// toolNameRE matches declared tool dependency names such as read_file,
	// web_fetch, attach_audio, exec_start, and subagent_read.
	toolNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
)

//go:embed builtins
var builtinSkillFS embed.FS

var frontmatterParser = goldmark.New(
	goldmark.WithExtensions(meta.Meta),
)

// Settings configures agent-visible skills roots.
type Settings struct {
	WorkspaceLocalDir   string
	WorkspaceRuntimeDir string
	SkillsLocalDir      string
	SkillsRuntimeDir    string
}

// Source identifies the source of a skill entry.
type Source string

// Source values identify where a skill entry or validation result came from.
const (
	SourceBuiltin Source = "builtin"
	SourceShared  Source = "shared"
	SourceDraft   Source = "draft"
)

// Entry is one prompt-visible skill.
type Entry struct {
	Name          string
	Description   string
	Source        Source
	SkillPath     string
	SkillFilePath string
	Tools         []string
}

// Catalog contains visible skills plus any non-fatal discovery warnings.
type Catalog struct {
	Entries  []Entry
	Warnings []string
}

// ResolvedSkill is a fully resolved skill, including its stripped SKILL.md
// body, for delegation to sub-agents. Unlike Entry, it carries the body text
// so a sub-agent receives the complete workflow without the parent copying
// the SKILL.md content into the task prompt.
type ResolvedSkill struct {
	Name          string
	Description   string
	Source        Source
	SkillPath     string
	SkillFilePath string
	Tools         []string
	Body          string
}

// ValidationResult contains structured validation output for one skill.
type ValidationResult struct {
	Valid         bool
	Name          string
	Description   string
	Source        Source
	SkillPath     string
	SkillFilePath string
	Tools         []string
	Warnings      []string
	Errors        []string
}

type metadata struct {
	Name        string
	Description string
	Body        string
	Tools       []string
}

// Manager owns skills discovery, validation, and builtin reads.
type Manager struct {
	settings Settings
}

// NewManager normalizes and returns a skills manager.
func NewManager(cfg Settings) *Manager {
	cfg.WorkspaceLocalDir = cleanOptionalLocalPath(cfg.WorkspaceLocalDir)
	cfg.WorkspaceRuntimeDir = normalizeContainerRoot(cfg.WorkspaceRuntimeDir)
	cfg.SkillsLocalDir = cleanOptionalLocalPath(cfg.SkillsLocalDir)
	cfg.SkillsRuntimeDir = normalizeContainerRoot(cfg.SkillsRuntimeDir)
	if cfg.SkillsRuntimeDir == "" {
		cfg.SkillsRuntimeDir = DefaultContainerDir
	}
	return &Manager{settings: cfg}
}

// SkillsDir returns the normalized container skills root.
func (m *Manager) SkillsDir() string {
	if m == nil {
		return DefaultContainerDir
	}
	if m.settings.SkillsRuntimeDir == "" {
		return DefaultContainerDir
	}
	return m.settings.SkillsRuntimeDir
}

// SharedSkillsEnabled reports whether a shared filesystem root is configured.
func (m *Manager) SharedSkillsEnabled() bool {
	return m != nil && strings.TrimSpace(m.settings.SkillsLocalDir) != ""
}

// LoadCatalog returns the current visible skills catalog.
func (m *Manager) LoadCatalog() Catalog {
	catalog := Catalog{
		Entries:  builtinEntries(m.SkillsDir()),
		Warnings: nil,
	}
	if m == nil || strings.TrimSpace(m.settings.SkillsLocalDir) == "" {
		return catalog
	}

	entries, warnings := m.loadSharedEntries()
	catalog.Warnings = append(catalog.Warnings, warnings...)

	builtinNames := make(map[string]struct{}, len(catalog.Entries))
	for _, entry := range catalog.Entries {
		builtinNames[entry.Name] = struct{}{}
	}
	for _, entry := range entries {
		if _, exists := builtinNames[entry.Name]; exists {
			catalog.Warnings = append(
				catalog.Warnings,
				fmt.Sprintf(
					"skipping shared skill %q because a builtin skill with that name already exists",
					entry.Name,
				),
			)
			continue
		}
		catalog.Entries = append(catalog.Entries, entry)
	}

	sort.Slice(catalog.Entries, func(i, j int) bool {
		if catalog.Entries[i].Name == catalog.Entries[j].Name {
			return catalog.Entries[i].Source < catalog.Entries[j].Source
		}
		return catalog.Entries[i].Name < catalog.Entries[j].Name
	})
	return catalog
}

// ValidateSkill validates one builtin, shared, or workspace skill path.
func (m *Manager) ValidateSkill(rawPath string) (ValidationResult, error) {
	rawPath = strings.TrimSpace(rawPath)
	if rawPath == "" {
		return ValidationResult{
			Valid: false,
			Errors: []string{
				"path is required",
			},
		}, nil
	}

	switch {
	case m.isBuiltinPath(rawPath):
		return m.validateBuiltin(rawPath), nil
	default:
		return m.validateFilesystem(rawPath), nil
	}
}

// ResolveSkill resolves a skill by bare name or container/workspace path and
// returns its full metadata plus stripped SKILL.md body for delegation. A
// bare name (no path separator) resolves through the live catalog (builtins
// and shared skills). Paths resolve through ValidateSkill/resolveFilesystemPath.
// Invalid or unknown skills return a hard error so a delegation never
// silently runs without promised instructions.
func (m *Manager) ResolveSkill(ref string) (ResolvedSkill, error) {
	if m == nil {
		return ResolvedSkill{}, fmt.Errorf("skills manager is not configured")
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ResolvedSkill{}, fmt.Errorf("skill ref is required")
	}

	// Bare name: resolve through the catalog (builtins + shared).
	if !strings.Contains(ref, "/") {
		for _, entry := range m.LoadCatalog().Entries {
			if entry.Name != ref {
				continue
			}
			return m.resolveFromEntry(entry)
		}
		return ResolvedSkill{}, fmt.Errorf("unknown skill %q", ref)
	}

	// Path-based ref: use the validation pipeline to resolve and confirm the
	// skill is well-formed, then read its body from the resolved source.
	result, err := m.ValidateSkill(ref)
	if err != nil {
		return ResolvedSkill{}, err
	}
	if !result.Valid {
		return ResolvedSkill{}, fmt.Errorf("invalid skill %q: %s", ref, joinErrors(result.Errors))
	}
	return m.resolveFromResult(result)
}

// resolveFromEntry reads the body for a catalog entry and builds a ResolvedSkill.
func (m *Manager) resolveFromEntry(entry Entry) (ResolvedSkill, error) {
	resolved := ResolvedSkill{
		Name:          entry.Name,
		Description:   entry.Description,
		Source:        entry.Source,
		SkillPath:     entry.SkillPath,
		SkillFilePath: entry.SkillFilePath,
		Tools:         append([]string(nil), entry.Tools...),
	}
	body, err := m.readSkillBody(entry.Source, entry.SkillFilePath)
	if err != nil {
		return ResolvedSkill{}, err
	}
	resolved.Body = body
	return resolved, nil
}

// resolveFromResult reads the body for a validated result and builds a
// ResolvedSkill.
func (m *Manager) resolveFromResult(result ValidationResult) (ResolvedSkill, error) {
	resolved := ResolvedSkill{
		Name:          result.Name,
		Description:   result.Description,
		Source:        result.Source,
		SkillPath:     result.SkillPath,
		SkillFilePath: result.SkillFilePath,
		Tools:         append([]string(nil), result.Tools...),
	}
	body, err := m.readSkillBody(result.Source, result.SkillFilePath)
	if err != nil {
		return ResolvedSkill{}, err
	}
	resolved.Body = body
	return resolved, nil
}

// readSkillBody reads and strips the SKILL.md body from the appropriate source
// (embedded builtin FS or host filesystem).
func (m *Manager) readSkillBody(source Source, skillFilePath string) (string, error) {
	if source == SourceBuiltin {
		rel, err := m.resolveBuiltinRelative(skillFilePath)
		if err != nil {
			return "", err
		}
		meta, errs, _ := readMetadataFromFS(builtinSkillFS, rel)
		if len(errs) > 0 {
			return "", fmt.Errorf("read builtin skill body: %s", joinErrors(errs))
		}
		return meta.Body, nil
	}

	// Shared and workspace draft skills live on the host filesystem under the
	// resolved skill directory.
	hostDir, err := m.skillFilePathToHostDir(skillFilePath, source)
	if err != nil {
		return "", err
	}
	meta, errs, _ := readMetadataFromFile(filepath.Join(hostDir, skillFileName))
	if len(errs) > 0 {
		return "", fmt.Errorf("read skill body: %s", joinErrors(errs))
	}
	return meta.Body, nil
}

// skillFilePathToHostDir converts a container-visible SkillFilePath back to the
// host directory that holds the SKILL.md file for filesystem-backed skills.
func (m *Manager) skillFilePathToHostDir(skillFilePath string, source Source) (string, error) {
	switch source {
	case SourceShared:
		if strings.TrimSpace(m.settings.SkillsLocalDir) == "" {
			return "", fmt.Errorf("shared skills root is not configured")
		}
		rel := strings.TrimPrefix(path.Clean(skillFilePath), m.SkillsDir()+"/")
		rel = strings.TrimSuffix(rel, "/"+skillFileName)
		first, _, _ := strings.Cut(rel, "/")
		if err := validateRelativePath(first); err != nil {
			return "", err
		}
		return filepath.Join(m.settings.SkillsLocalDir, filepath.FromSlash(first)), nil
	case SourceDraft:
		if strings.TrimSpace(m.settings.WorkspaceLocalDir) == "" ||
			strings.TrimSpace(m.settings.WorkspaceRuntimeDir) == "" {
			return "", fmt.Errorf("workspace root is not configured")
		}
		rel := strings.TrimPrefix(path.Clean(skillFilePath), m.settings.WorkspaceRuntimeDir+"/")
		rel = strings.TrimSuffix(rel, "/"+skillFileName)
		if err := validateRelativePath(rel); err != nil {
			return "", err
		}
		return filepath.Join(m.settings.WorkspaceLocalDir, filepath.FromSlash(rel)), nil
	default:
		return "", fmt.Errorf("unsupported skill source %q", source)
	}
}

// ReadBuiltinFile serves one builtin skill file via the read_file pagination
// contract. The handled return indicates whether the path belongs to the
// builtin namespace.
func (m *Manager) ReadBuiltinFile(
	rawPath string,
	offsetLines int,
	limitLines int,
) (fileops.ReadResult, bool, error) {
	if !m.isBuiltinPath(rawPath) {
		return fileops.ReadResult{}, false, nil
	}

	rel, err := m.resolveBuiltinRelative(rawPath)
	if err != nil {
		return fileops.ReadResult{}, true, err
	}
	raw, err := builtinSkillFS.ReadFile(rel)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fileops.ReadResult{}, true, fmt.Errorf("file not found")
		}
		return fileops.ReadResult{}, true, fmt.Errorf("read builtin file: %w", err)
	}
	if strings.HasSuffix(rel, "/") {
		return fileops.ReadResult{}, true, fmt.Errorf("path is not a regular file")
	}
	if bytesIndexByte(raw, 0) >= 0 {
		return fileops.ReadResult{}, true, fmt.Errorf(
			"file contains NUL bytes and is not supported as text",
		)
	}
	if !utf8.Valid(raw) {
		return fileops.ReadResult{}, true, fmt.Errorf("file is not valid UTF-8")
	}
	result, err := paginateText(string(raw), offsetLines, limitLines)
	return result, true, err
}

func (m *Manager) loadSharedEntries() ([]Entry, []string) {
	entries, err := os.ReadDir(m.settings.SkillsLocalDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, []string{fmt.Sprintf("read shared skills root: %v", err)}
	}

	out := make([]Entry, 0, len(entries))
	warnings := make([]string, 0)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		result := m.validateSharedDir(entry.Name())
		if !result.Valid {
			warnings = append(
				warnings,
				fmt.Sprintf(
					"skipping invalid shared skill %q: %s",
					entry.Name(),
					joinErrors(result.Errors),
				),
			)
			continue
		}
		out = append(out, Entry{
			Name:          result.Name,
			Description:   result.Description,
			Source:        SourceShared,
			SkillPath:     result.SkillPath,
			SkillFilePath: result.SkillFilePath,
			Tools:         append([]string(nil), result.Tools...),
		})
		for _, warning := range result.Warnings {
			warnings = append(warnings, fmt.Sprintf("%s: %s", entry.Name(), warning))
		}
	}
	return out, warnings
}

func (m *Manager) validateSharedDir(dirName string) ValidationResult {
	result := ValidationResult{
		Source:        SourceShared,
		SkillPath:     path.Join(m.SkillsDir(), dirName),
		SkillFilePath: path.Join(m.SkillsDir(), dirName, skillFileName),
	}
	hostDir := filepath.Join(m.settings.SkillsLocalDir, dirName)
	return m.validateDir(hostDir, result)
}

func (m *Manager) validateBuiltin(rawPath string) ValidationResult {
	result := ValidationResult{Source: SourceBuiltin}
	cleaned := path.Clean(strings.TrimSpace(rawPath))
	root := path.Join(m.SkillsDir(), BuiltinNamespace)
	trimmed := strings.TrimPrefix(cleaned, root)
	trimmed = strings.TrimPrefix(trimmed, "/")
	parts := strings.Split(trimmed, "/")
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		result.Errors = append(result.Errors, "builtin skill path is incomplete")
		return result
	}
	name := parts[0]
	result.SkillPath = path.Join(root, name)
	result.SkillFilePath = path.Join(result.SkillPath, skillFileName)
	relDir := path.Join("builtins", name)
	_, err := fs.Stat(builtinSkillFS, relDir)
	if err != nil {
		result.Errors = append(result.Errors, "builtin skill not found")
		return result
	}
	meta, errs, warnings := readMetadataFromFS(builtinSkillFS, path.Join(relDir, skillFileName))
	result.Errors = append(result.Errors, errs...)
	result.Warnings = append(result.Warnings, warnings...)
	result.Name = meta.Name
	result.Description = meta.Description
	result.Tools = append([]string(nil), meta.Tools...)
	if len(result.Errors) == 0 && meta.Name != name {
		result.Errors = append(
			result.Errors,
			fmt.Sprintf("builtin directory %q does not match skill name %q", name, meta.Name),
		)
	}
	result.Valid = len(result.Errors) == 0
	return result
}

func (m *Manager) validateFilesystem(rawPath string) ValidationResult {
	resolved, err := m.resolveFilesystemPath(rawPath)
	if err != nil {
		return ValidationResult{
			Valid:  false,
			Errors: []string{err.Error()},
		}
	}
	return m.validateDir(resolved.hostDir, ValidationResult{
		Source:        resolved.source,
		SkillPath:     resolved.containerDir,
		SkillFilePath: path.Join(resolved.containerDir, skillFileName),
	})
}

func (m *Manager) validateDir(hostDir string, result ValidationResult) ValidationResult {
	info, err := os.Stat(hostDir)
	if err != nil {
		if os.IsNotExist(err) {
			result.Errors = append(result.Errors, "skill directory not found")
			return result
		}
		result.Errors = append(result.Errors, fmt.Sprintf("stat skill directory: %v", err))
		return result
	}
	if !info.IsDir() {
		result.Errors = append(result.Errors, "path is not a directory")
		return result
	}

	meta, errs, warnings := readMetadataFromFile(filepath.Join(hostDir, skillFileName))
	result.Errors = append(result.Errors, errs...)
	result.Warnings = append(result.Warnings, warnings...)
	result.Name = meta.Name
	result.Description = meta.Description
	result.Tools = append([]string(nil), meta.Tools...)

	dirName := filepath.Base(hostDir)
	if meta.Name != "" && dirName != meta.Name {
		result.Errors = append(
			result.Errors,
			fmt.Sprintf("directory name %q must match skill name %q", dirName, meta.Name),
		)
	}
	if result.Source == SourceShared && builtinExists(meta.Name) {
		result.Errors = append(
			result.Errors,
			fmt.Sprintf("skill name %q is reserved by a builtin skill", meta.Name),
		)
	}

	result.Valid = len(result.Errors) == 0
	return result
}

type resolvedFilesystemPath struct {
	hostDir      string
	containerDir string
	source       Source
}

func (m *Manager) resolveFilesystemPath(rawPath string) (resolvedFilesystemPath, error) {
	rawPath = strings.TrimSpace(rawPath)
	if rawPath == "" {
		return resolvedFilesystemPath{}, fmt.Errorf("path is required")
	}
	if path.IsAbs(rawPath) {
		cleaned := path.Clean(rawPath)
		switch {
		case strings.HasPrefix(cleaned, m.SkillsDir()+"/"):
			if strings.TrimSpace(m.settings.SkillsLocalDir) == "" {
				return resolvedFilesystemPath{}, fmt.Errorf("shared skills root is not configured")
			}
			rel := strings.TrimPrefix(cleaned, m.SkillsDir()+"/")
			if rel == "" {
				return resolvedFilesystemPath{}, fmt.Errorf("path must reference a skill")
			}
			first, _, _ := strings.Cut(rel, "/")
			if first == BuiltinNamespace {
				return resolvedFilesystemPath{}, fmt.Errorf(
					"builtin skills are validated separately",
				)
			}
			if err := validateRelativePath(first); err != nil {
				return resolvedFilesystemPath{}, err
			}
			return resolvedFilesystemPath{
				hostDir:      filepath.Join(m.settings.SkillsLocalDir, filepath.FromSlash(first)),
				containerDir: path.Join(m.SkillsDir(), first),
				source:       SourceShared,
			}, nil
		case m.settings.WorkspaceRuntimeDir != "" && strings.HasPrefix(cleaned, m.settings.WorkspaceRuntimeDir+"/"):
			rel := strings.TrimPrefix(cleaned, m.settings.WorkspaceRuntimeDir+"/")
			return resolveWorkspaceSkillDir(
				m.settings.WorkspaceLocalDir,
				m.settings.WorkspaceRuntimeDir,
				rel,
			)
		default:
			return resolvedFilesystemPath{}, fmt.Errorf(
				"absolute paths must be under %s or %s/@builtin",
				m.SkillsDir(),
				m.SkillsDir(),
			)
		}
	}

	return resolveWorkspaceSkillDir(
		m.settings.WorkspaceLocalDir,
		m.settings.WorkspaceRuntimeDir,
		rawPath,
	)
}

func resolveWorkspaceSkillDir(
	workspaceLocalDir string,
	workspaceRuntimeDir string,
	rel string,
) (resolvedFilesystemPath, error) {
	if strings.TrimSpace(workspaceLocalDir) == "" || strings.TrimSpace(workspaceRuntimeDir) == "" {
		return resolvedFilesystemPath{}, fmt.Errorf("workspace root is not configured")
	}
	cleaned := path.Clean(strings.TrimSpace(rel))
	if err := validateRelativePath(cleaned); err != nil {
		return resolvedFilesystemPath{}, err
	}
	hostDir := filepath.Join(workspaceLocalDir, filepath.FromSlash(cleaned))
	containerDir := path.Join(workspaceRuntimeDir, cleaned)
	if strings.EqualFold(path.Base(containerDir), skillFileName) {
		hostDir = filepath.Dir(hostDir)
		containerDir = path.Dir(containerDir)
	}
	return resolvedFilesystemPath{
		hostDir:      hostDir,
		containerDir: containerDir,
		source:       SourceDraft,
	}, nil
}

func builtinEntries(skillsDir string) []Entry {
	names, err := builtinSkillNames()
	if err != nil {
		return nil
	}

	out := make([]Entry, 0, len(names))
	for _, dirName := range names {
		meta, errs, _ := readMetadataFromFS(
			builtinSkillFS,
			path.Join("builtins", dirName, skillFileName),
		)
		if len(errs) > 0 || strings.TrimSpace(meta.Name) == "" || meta.Name != dirName {
			continue
		}
		out = append(out, Entry{
			Name:          meta.Name,
			Description:   meta.Description,
			Source:        SourceBuiltin,
			SkillPath:     path.Join(skillsDir, BuiltinNamespace, dirName),
			SkillFilePath: path.Join(skillsDir, BuiltinNamespace, dirName, skillFileName),
			Tools:         append([]string(nil), meta.Tools...),
		})
	}
	return out
}

func builtinExists(name string) bool {
	for _, entry := range builtinEntries(DefaultContainerDir) {
		if entry.Name == name {
			return true
		}
	}
	return false
}

func builtinSkillNames() ([]string, error) {
	entries, err := fs.ReadDir(builtinSkillFS, "builtins")
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names, nil
}

func readMetadataFromFile(path string) (metadata, []string, []string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return metadata{}, []string{"missing SKILL.md"}, nil
		}
		return metadata{}, []string{fmt.Sprintf("read SKILL.md: %v", err)}, nil
	}
	return readMetadata(raw)
}

func readMetadataFromFS(fsys fs.FS, path string) (metadata, []string, []string) {
	raw, err := fs.ReadFile(fsys, path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return metadata{}, []string{"missing SKILL.md"}, nil
		}
		return metadata{}, []string{fmt.Sprintf("read SKILL.md: %v", err)}, nil
	}
	return readMetadata(raw)
}

func readMetadata(raw []byte) (metadata, []string, []string) {
	if bytesIndexByte(raw, 0) >= 0 {
		return metadata{}, []string{"SKILL.md contains NUL bytes"}, nil
	}
	if !utf8.Valid(raw) {
		return metadata{}, []string{"SKILL.md must be valid UTF-8"}, nil
	}

	textValue := normalizeText(string(raw))
	body := stripYAMLFrontmatter(textValue)

	ctx := parser.NewContext()
	frontmatterParser.Parser().Parse(
		text.NewReader([]byte(textValue)),
		parser.WithContext(ctx),
	)
	values, err := meta.TryGet(ctx)
	if err != nil || values == nil {
		return metadata{}, []string{"SKILL.md must start with YAML frontmatter"}, nil
	}

	md := metadata{
		Body: body,
	}
	if value, ok := values["name"].(string); ok {
		md.Name = strings.TrimSpace(value)
	}
	if value, ok := values["description"].(string); ok {
		md.Description = strings.TrimSpace(value)
	}

	errorsList := make([]string, 0)
	if md.Name == "" {
		errorsList = append(errorsList, "frontmatter field \"name\" is required")
	} else {
		if len(md.Name) > maxSkillNameChars {
			errorsList = append(
				errorsList,
				fmt.Sprintf("skill name must be <= %d characters", maxSkillNameChars),
			)
		}
		if !skillNameRE.MatchString(md.Name) {
			errorsList = append(
				errorsList,
				"skill name must use lowercase letters, digits, and hyphens only",
			)
		}
	}
	if md.Description == "" {
		errorsList = append(errorsList, "frontmatter field \"description\" is required")
	} else if len(md.Description) > maxDescriptionChars {
		errorsList = append(
			errorsList,
			fmt.Sprintf("description must be <= %d characters", maxDescriptionChars),
		)
	}

	warnings := make([]string, 0)
	md.Tools, errorsList, warnings = parseToolsField(values, errorsList, warnings)
	if lineCount(textValue) > 500 {
		warnings = append(warnings, "SKILL.md exceeds 500 lines; prefer progressive disclosure")
	}
	return md, errorsList, warnings
}

// parseToolsField reads the optional `tools` frontmatter field and returns a
// stable-deduplicated, first-seen-order slice of validated tool dependency
// names. The errors and warnings slices are extended in place. An absent field
// yields nil tools with no warnings or errors.
func parseToolsField(
	values map[string]interface{},
	errorsList []string,
	warnings []string,
) ([]string, []string, []string) {
	raw, present := values["tools"]
	if !present {
		return nil, errorsList, warnings
	}
	list, ok := raw.([]interface{})
	if !ok {
		return nil,
			append(errorsList, "frontmatter field \"tools\" must be a list of strings"),
			warnings
	}
	if len(list) > maxDeclaredTools {
		return nil,
			append(
				errorsList,
				fmt.Sprintf("tools must declare <= %d entries", maxDeclaredTools),
			),
			warnings
	}
	seen := make(map[string]struct{}, len(list))
	out := make([]string, 0, len(list))
	for i, item := range list {
		str, ok := item.(string)
		if !ok {
			return nil,
				append(
					errorsList,
					fmt.Sprintf("tools[%d] must be a string", i),
				),
				warnings
		}
		name := strings.TrimSpace(str)
		if name == "" {
			return nil,
				append(
					errorsList,
					fmt.Sprintf("tools[%d] is empty", i),
				),
				warnings
		}
		if len(name) > maxDeclaredToolNameLen {
			return nil,
				append(
					errorsList,
					fmt.Sprintf(
						"tools[%d] must be <= %d characters",
						i,
						maxDeclaredToolNameLen,
					),
				),
				warnings
		}
		if !toolNameRE.MatchString(name) {
			return nil,
				append(
					errorsList,
					fmt.Sprintf(
						"tools[%d] must use lowercase letters, digits, underscores, and hyphens only",
						i,
					),
				),
				warnings
		}
		if _, dup := seen[name]; dup {
			warnings = append(warnings, fmt.Sprintf("duplicate declared tool %q", name))
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out, errorsList, warnings
}

func normalizeText(raw string) string {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")
	return strings.TrimSpace(raw)
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

func lineCount(raw string) int {
	if raw == "" {
		return 0
	}
	return strings.Count(raw, "\n") + 1
}

func joinErrors(errors []string) string {
	if len(errors) == 0 {
		return ""
	}
	return strings.Join(errors, "; ")
}

func (m *Manager) isBuiltinPath(rawPath string) bool {
	cleaned := path.Clean(strings.TrimSpace(rawPath))
	root := path.Join(m.SkillsDir(), BuiltinNamespace)
	return cleaned == root || strings.HasPrefix(cleaned, root+"/")
}

func (m *Manager) resolveBuiltinRelative(rawPath string) (string, error) {
	cleaned := path.Clean(strings.TrimSpace(rawPath))
	root := path.Join(m.SkillsDir(), BuiltinNamespace)
	if cleaned == root {
		return "", fmt.Errorf("path must reference a file, not a root")
	}
	rel := strings.TrimPrefix(cleaned, root+"/")
	if rel == cleaned || rel == "" {
		return "", fmt.Errorf("path must reference a builtin skill file")
	}
	if err := validateRelativePath(rel); err != nil {
		return "", err
	}
	return path.Join("builtins", rel), nil
}

func normalizeContainerRoot(root string) string {
	root = strings.TrimSpace(root)
	if root == "" {
		return ""
	}
	cleaned := path.Clean(root)
	if !path.IsAbs(cleaned) {
		return ""
	}
	return cleaned
}

func cleanOptionalLocalPath(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	return filepath.Clean(raw)
}

func validateRelativePath(rel string) error {
	if rel == "" || rel == "." {
		return fmt.Errorf("path must reference a file")
	}
	if strings.ContainsRune(rel, '\\') {
		return fmt.Errorf("path must use forward slashes")
	}
	if !fs.ValidPath(rel) {
		return fmt.Errorf("path %q is invalid", rel)
	}
	if !filepath.IsLocal(filepath.FromSlash(rel)) {
		return fmt.Errorf("path %q escapes root", rel)
	}
	return nil
}

func paginateText(
	text string,
	offsetLines int,
	limitLines int,
) (fileops.ReadResult, error) {
	lines := splitLines(normalizeText(text))
	totalLines := len(lines)
	if totalLines == 0 {
		return fileops.ReadResult{}, fmt.Errorf("file is empty")
	}

	offset := offsetLines
	if offset == 0 {
		offset = 1
	}
	if offset < 1 {
		return fileops.ReadResult{}, fmt.Errorf("offset_lines must be >= 1")
	}
	if offset > totalLines {
		return fileops.ReadResult{}, fmt.Errorf("offset_lines is beyond end of file")
	}

	limit := limitLines
	if limit == 0 {
		limit = defaultReadLimitLines
	}
	if limit < 1 {
		return fileops.ReadResult{}, fmt.Errorf("limit_lines must be >= 1")
	}
	if limit > maxReadLimitLines {
		limit = maxReadLimitLines
	}

	start := offset - 1
	out := make([]string, 0, limit)
	bytesUsed := 0
	truncated := false
	nextOffset := 0

	for i := start; i < totalLines; i++ {
		line := lines[i]
		lineBytes := len(line)
		if len(out) > 0 {
			lineBytes++
		}
		if len(out) >= limit || bytesUsed+lineBytes > maxReadBytes {
			truncated = true
			nextOffset = i + 1
			break
		}
		out = append(out, line)
		bytesUsed += lineBytes
	}

	if !truncated && start+len(out) < totalLines {
		truncated = true
		nextOffset = start + len(out) + 1
	}

	result := fileops.ReadResult{
		Content:    strings.Join(out, "\n"),
		Truncated:  truncated,
		TotalLines: totalLines,
	}
	if truncated {
		result.NextOffsetLines = nextOffset
	}
	return result, nil
}

func splitLines(text string) []string {
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func bytesIndexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}
