package embed

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Registry persists and validates typed ingestion sources.
type Registry struct {
	path     string
	settings Settings
}

type registryFileDisk struct {
	Version int          `json:"version"`
	Sources []sourceDisk `json:"sources"`
}

type sourceDisk struct {
	ID           string   `json:"id"`
	Collection   string   `json:"collection"`
	SourceType   string   `json:"source_type"`
	Path         string   `json:"path"`
	IncludeGlobs []string `json:"include_globs,omitempty"`
	ExcludeGlobs []string `json:"exclude_globs,omitempty"`
	MetadataPath string   `json:"metadata_path,omitempty"`
	Enabled      *bool    `json:"enabled,omitempty"`
}

// NewRegistry constructs a registry backed by /workspace/.q15/embed/sources.json.
func NewRegistry(settings Settings) *Registry {
	return &Registry{
		path:     defaultRegistryPath(settings),
		settings: settings,
	}
}

// List returns the persisted sources, or the built-in memory sources when no
// registry file exists yet.
func (r *Registry) List(ctx context.Context) ([]Source, error) {
	_ = ctx
	file, err := r.load()
	if err != nil {
		return nil, err
	}
	return cloneSources(file.Sources), nil
}

// Add validates and persists one source. When ID is empty, a stable ID is
// derived from collection, source_type, and path.
func (r *Registry) Add(ctx context.Context, source Source) (Source, error) {
	_ = ctx
	source, err := r.normalizeSource(source, true)
	if err != nil {
		return Source{}, err
	}

	file, err := r.load()
	if err != nil {
		return Source{}, err
	}
	for _, existing := range file.Sources {
		if existing.ID == source.ID {
			return Source{}, fmt.Errorf("source id %q already exists", source.ID)
		}
	}
	file.Sources = append(file.Sources, source)
	sortSources(file.Sources)
	if err := r.save(file); err != nil {
		return Source{}, err
	}
	return source, nil
}

// Remove deletes a source from the registry. Existing vector points are pruned
// by the next sync run.
func (r *Registry) Remove(ctx context.Context, id string) (Source, error) {
	_ = ctx
	id = strings.TrimSpace(id)
	if id == "" {
		return Source{}, fmt.Errorf("id is required")
	}
	file, err := r.load()
	if err != nil {
		return Source{}, err
	}
	out := file.Sources[:0]
	var removed Source
	for _, source := range file.Sources {
		if source.ID == id {
			removed = source
			continue
		}
		out = append(out, source)
	}
	if removed.ID == "" {
		return Source{}, fmt.Errorf("source id %q not found", id)
	}
	file.Sources = out
	if err := r.save(file); err != nil {
		return Source{}, err
	}
	return removed, nil
}

// SetEnabled toggles a source without removing its registry entry.
func (r *Registry) SetEnabled(ctx context.Context, id string, enabled bool) (Source, error) {
	_ = ctx
	id = strings.TrimSpace(id)
	if id == "" {
		return Source{}, fmt.Errorf("id is required")
	}
	file, err := r.load()
	if err != nil {
		return Source{}, err
	}
	for i := range file.Sources {
		if file.Sources[i].ID != id {
			continue
		}
		file.Sources[i].Enabled = enabled
		if err := r.save(file); err != nil {
			return Source{}, err
		}
		return file.Sources[i], nil
	}
	return Source{}, fmt.Errorf("source id %q not found", id)
}

func (r *Registry) load() (RegistryFile, error) {
	data, err := os.ReadFile(r.path)
	if err != nil {
		if os.IsNotExist(err) {
			return RegistryFile{Version: 1, Sources: defaultSources()}, nil
		}
		return RegistryFile{}, fmt.Errorf("read source registry: %w", err)
	}
	var disk registryFileDisk
	if err := json.Unmarshal(data, &disk); err != nil {
		return RegistryFile{}, fmt.Errorf("decode source registry: %w", err)
	}
	file := RegistryFile{
		Version: disk.Version,
		Sources: make([]Source, 0, len(disk.Sources)),
	}
	for _, source := range disk.Sources {
		file.Sources = append(file.Sources, source.toSource())
	}
	if file.Version == 0 {
		file.Version = 1
	}
	for i := range file.Sources {
		source, err := r.normalizeSource(file.Sources[i], false)
		if err != nil {
			return RegistryFile{}, fmt.Errorf("sources[%d]: %w", i, err)
		}
		file.Sources[i] = source
	}
	sortSources(file.Sources)
	return file, nil
}

func (r *Registry) save(file RegistryFile) error {
	if file.Version == 0 {
		file.Version = 1
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o755); err != nil {
		return fmt.Errorf("create source registry dir: %w", err)
	}
	sortSources(file.Sources)
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("encode source registry: %w", err)
	}
	data = append(data, '\n')
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write source registry temp file: %w", err)
	}
	if err := os.Rename(tmp, r.path); err != nil {
		return fmt.Errorf("replace source registry: %w", err)
	}
	return nil
}

func (r *Registry) normalizeSource(source Source, requirePathExists bool) (Source, error) {
	source.Collection = strings.TrimSpace(source.Collection)
	if err := validateCollection(source.Collection); err != nil {
		return Source{}, err
	}
	source.SourceType = strings.TrimSpace(source.SourceType)
	if err := validateSourceType(source.SourceType); err != nil {
		return Source{}, err
	}
	resolved, err := resolveRuntimePath(r.settings, source.Path)
	if err != nil {
		return Source{}, err
	}
	source.Path = resolved.runtimePath
	if requirePathExists {
		info, err := os.Stat(resolved.localPath)
		if err != nil {
			return Source{}, fmt.Errorf("stat source path %q: %w", source.Path, err)
		}
		if err := validateSourcePathCompatibility(source, info); err != nil {
			return Source{}, err
		}
	}
	if source.MetadataPath != "" {
		if source.SourceType != SourceTypeChunkedMarkdownTree {
			return Source{}, fmt.Errorf(
				"metadata_path is only supported for source_type %q",
				SourceTypeChunkedMarkdownTree,
			)
		}
		metadata, err := resolveRuntimePath(r.settings, source.MetadataPath)
		if err != nil {
			return Source{}, fmt.Errorf("metadata_path: %w", err)
		}
		source.MetadataPath = metadata.runtimePath
		if requirePathExists {
			info, err := os.Stat(metadata.localPath)
			if err != nil {
				return Source{}, fmt.Errorf("stat metadata_path %q: %w", source.MetadataPath, err)
			}
			if info.IsDir() {
				return Source{}, fmt.Errorf("metadata_path %q must be a file", source.MetadataPath)
			}
		}
	}
	source.IncludeGlobs = cleanGlobs(source.IncludeGlobs)
	source.ExcludeGlobs = cleanGlobs(source.ExcludeGlobs)
	source.ID = strings.TrimSpace(source.ID)
	if source.ID == "" {
		source.ID = defaultSourceID(source)
	}
	if !isSafeID(source.ID) {
		return Source{}, fmt.Errorf(
			"source id %q may only contain letters, digits, '.', '_', '/', or '-'",
			source.ID,
		)
	}
	return source, nil
}

func (s sourceDisk) toSource() Source {
	enabled := true
	if s.Enabled != nil {
		enabled = *s.Enabled
	}
	return Source{
		ID:           s.ID,
		Collection:   s.Collection,
		SourceType:   s.SourceType,
		Path:         s.Path,
		IncludeGlobs: s.IncludeGlobs,
		ExcludeGlobs: s.ExcludeGlobs,
		MetadataPath: s.MetadataPath,
		Enabled:      enabled,
	}
}

func validateSourcePathCompatibility(source Source, info os.FileInfo) error {
	switch source.SourceType {
	case SourceTypeMarkdownFile:
		if info.IsDir() {
			return fmt.Errorf("source path %q must be a markdown file", source.Path)
		}
		if !strings.EqualFold(filepath.Ext(source.Path), ".md") {
			return fmt.Errorf("source path %q must end in .md", source.Path)
		}
	case SourceTypeMarkdownTree, SourceTypeChunkedMarkdownTree:
		if !info.IsDir() {
			return fmt.Errorf("source path %q must be a directory", source.Path)
		}
	}
	return nil
}

func validateCollection(collection string) error {
	switch collection {
	case CollectionLibrary, CollectionZettelkasten, CollectionSemantic, CollectionCore:
		return nil
	default:
		return fmt.Errorf(
			"collection %q is not supported (want one of: %s)",
			collection,
			strings.Join(SupportedCollections(), ", "),
		)
	}
}

func validateSourceType(sourceType string) error {
	switch sourceType {
	case SourceTypeMarkdownTree, SourceTypeMarkdownFile, SourceTypeChunkedMarkdownTree:
		return nil
	default:
		return fmt.Errorf(
			"source_type %q is not supported (want one of: %s)",
			sourceType,
			strings.Join(SupportedSourceTypes(), ", "),
		)
	}
}

// SupportedCollections returns the fixed q15 vector collection names.
func SupportedCollections() []string {
	return []string{
		CollectionLibrary,
		CollectionZettelkasten,
		CollectionSemantic,
		CollectionCore,
	}
}

// SupportedSourceTypes returns the typed scanner names.
func SupportedSourceTypes() []string {
	return []string{
		SourceTypeMarkdownTree,
		SourceTypeMarkdownFile,
		SourceTypeChunkedMarkdownTree,
	}
}

func defaultSources() []Source {
	return []Source{
		{
			ID:           "core-memory",
			Collection:   CollectionCore,
			SourceType:   SourceTypeMarkdownTree,
			Path:         "/memory/core",
			IncludeGlobs: []string{"*.md", "**/*.md"},
			Enabled:      true,
		},
		{
			ID:           "semantic-memory",
			Collection:   CollectionSemantic,
			SourceType:   SourceTypeMarkdownTree,
			Path:         "/memory/semantic",
			IncludeGlobs: []string{"*.md", "**/*.md"},
			Enabled:      true,
		},
		{
			ID:           "zettelkasten-notes",
			Collection:   CollectionZettelkasten,
			SourceType:   SourceTypeMarkdownTree,
			Path:         "/memory/notes",
			IncludeGlobs: []string{"*.md", "**/*.md"},
			Enabled:      true,
		},
	}
}

func defaultSourceID(source Source) string {
	sum := sha256.Sum256(
		[]byte(source.Collection + "\x00" + source.SourceType + "\x00" + source.Path),
	)
	slug := strings.Trim(path.Base(source.Path), ".")
	slug = strings.ToLower(strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		case r >= '0' && r <= '9':
			return r
		case r == '-' || r == '_' || r == '.':
			return r
		default:
			return '-'
		}
	}, slug))
	slug = strings.Trim(slug, "-")
	if slug == "" {
		slug = "source"
	}
	return source.Collection + "/" + source.SourceType + "/" + slug + "-" + hex.EncodeToString(sum[:])[:12]
}

func isSafeID(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '.', r == '_', r == '/', r == '-':
		default:
			return false
		}
	}
	return true
}

func cleanGlobs(in []string) []string {
	var out []string
	seen := map[string]struct{}{}
	for _, item := range in {
		item = strings.TrimSpace(strings.ReplaceAll(item, "\\", "/"))
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	sort.Strings(out)
	return out
}

func cloneSources(in []Source) []Source {
	if len(in) == 0 {
		return nil
	}
	out := make([]Source, len(in))
	copy(out, in)
	for i := range out {
		out[i].IncludeGlobs = append([]string(nil), in[i].IncludeGlobs...)
		out[i].ExcludeGlobs = append([]string(nil), in[i].ExcludeGlobs...)
	}
	return out
}

func sortSources(sources []Source) {
	sort.Slice(sources, func(i, j int) bool {
		return sources[i].ID < sources[j].ID
	})
}
