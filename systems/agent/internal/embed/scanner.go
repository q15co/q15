package embed

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// ScanSource returns embedding documents for one typed source.
func ScanSource(ctx context.Context, settings Settings, source Source) ([]Document, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	resolved, err := resolveRuntimePath(settings, source.Path)
	if err != nil {
		return nil, err
	}
	switch source.SourceType {
	case SourceTypeMarkdownFile:
		return scanMarkdownFile(settings, source, resolved)
	case SourceTypeMarkdownTree:
		return scanMarkdownTree(settings, source, resolved, false)
	case SourceTypeChunkedMarkdownTree:
		return scanMarkdownTree(settings, source, resolved, true)
	default:
		return nil, fmt.Errorf("source_type %q is not supported", source.SourceType)
	}
}

func scanMarkdownFile(
	settings Settings,
	source Source,
	resolved resolvedPath,
) ([]Document, error) {
	info, err := os.Stat(resolved.localPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("stat markdown file %q: %w", source.Path, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("source path %q must be a markdown file", source.Path)
	}
	if !strings.EqualFold(filepath.Ext(resolved.localPath), ".md") {
		return nil, fmt.Errorf("source path %q must end in .md", source.Path)
	}
	return documentsFromMarkdownFile(settings, source, resolved, false)
}

func scanMarkdownTree(
	settings Settings,
	source Source,
	resolved resolvedPath,
	chunked bool,
) ([]Document, error) {
	info, err := os.Stat(resolved.localPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("stat markdown tree %q: %w", source.Path, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("source path %q must be a directory", source.Path)
	}

	var documents []Document
	err = filepath.WalkDir(
		resolved.localPath,
		func(localPath string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			if !strings.EqualFold(filepath.Ext(localPath), ".md") {
				return nil
			}
			rel, err := filepath.Rel(resolved.localPath, localPath)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if !sourceIncludes(source, rel) {
				return nil
			}
			docResolved := resolvedPath{
				runtimePath: childRuntimePath(resolved.runtimePath, rel),
				localPath:   localPath,
				rootRuntime: resolved.rootRuntime,
				rel:         path.Join(resolved.rel, rel),
			}
			docs, err := documentsFromMarkdownFile(settings, source, docResolved, chunked)
			if err != nil {
				return err
			}
			documents = append(documents, docs...)
			return nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf("scan markdown tree %q: %w", source.Path, err)
	}
	sort.Slice(documents, func(i, j int) bool {
		return documents[i].Identity < documents[j].Identity
	})
	return documents, nil
}

func documentsFromMarkdownFile(
	settings Settings,
	source Source,
	resolved resolvedPath,
	chunked bool,
) ([]Document, error) {
	raw, err := os.ReadFile(resolved.localPath)
	if err != nil {
		return nil, fmt.Errorf("read markdown %q: %w", resolved.runtimePath, err)
	}
	if !isLikelyUTF8(raw) {
		return nil, fmt.Errorf("markdown %q is not valid UTF-8", resolved.runtimePath)
	}
	frontmatter, body := splitFrontmatter(string(raw))
	basePayload := basePayload(source, resolved.runtimePath)
	for key, value := range frontmatter {
		basePayload[key] = value
	}
	if chunked {
		metadata, err := chunkedMetadata(settings, source, resolved)
		if err != nil {
			return nil, err
		}
		for key, value := range metadata {
			if _, exists := basePayload[key]; !exists {
				basePayload[key] = value
			}
		}
		body = strings.TrimSpace(body)
		if body == "" {
			return nil, nil
		}
		return []Document{
			newDocument(source, resolved.runtimePath, resolved.runtimePath, body, basePayload),
		}, nil
	}
	return splitMarkdownSections(source, resolved.runtimePath, body, basePayload), nil
}

func newDocument(
	source Source,
	runtimePath string,
	identity string,
	text string,
	payload map[string]any,
) Document {
	payload = clonePayload(payload)
	text = strings.TrimSpace(text)
	sum := sha256.Sum256([]byte(text))
	payload["text"] = text
	payload["content_hash"] = hex.EncodeToString(sum[:])
	payload["source_id"] = source.ID
	payload["source_type"] = source.SourceType
	payload["collection"] = source.Collection
	payload["file_path"] = runtimePath
	return Document{
		Collection:  source.Collection,
		SourceID:    source.ID,
		SourceType:  source.SourceType,
		Path:        runtimePath,
		Identity:    identity,
		Text:        text,
		ContentHash: hex.EncodeToString(sum[:]),
		Payload:     payload,
	}
}

func splitMarkdownSections(
	source Source,
	runtimePath string,
	body string,
	basePayload map[string]any,
) []Document {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil
	}
	lines := strings.Split(body, "\n")
	type section struct {
		title string
		start int
		end   int
	}
	var sections []section
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "#") {
			continue
		}
		level := 0
		for level < len(trimmed) && trimmed[level] == '#' {
			level++
		}
		if level == 0 || level > 6 || level >= len(trimmed) || trimmed[level] != ' ' {
			continue
		}
		if len(sections) > 0 {
			sections[len(sections)-1].end = i
		}
		sections = append(sections, section{
			title: strings.TrimSpace(trimmed[level:]),
			start: i,
			end:   len(lines),
		})
	}
	if len(sections) == 0 {
		return []Document{newDocument(source, runtimePath, runtimePath, body, basePayload)}
	}
	var documents []Document
	if sections[0].start > 0 {
		preamble := strings.TrimSpace(strings.Join(lines[:sections[0].start], "\n"))
		if preamble != "" {
			payload := clonePayload(basePayload)
			payload["section"] = "Preamble"
			payload["section_index"] = 0
			documents = append(
				documents,
				newDocument(source, runtimePath, runtimePath+"#preamble", preamble, payload),
			)
		}
	}
	for i, section := range sections {
		text := strings.TrimSpace(strings.Join(lines[section.start:section.end], "\n"))
		if text == "" {
			continue
		}
		payload := clonePayload(basePayload)
		payload["section"] = section.title
		payload["section_index"] = i + 1
		documents = append(
			documents,
			newDocument(
				source,
				runtimePath,
				fmt.Sprintf("%s#section-%04d", runtimePath, i+1),
				text,
				payload,
			),
		)
	}
	return documents
}

func splitFrontmatter(raw string) (map[string]any, string) {
	raw = strings.TrimPrefix(raw, "\ufeff")
	if !strings.HasPrefix(raw, "---\n") && !strings.HasPrefix(raw, "---\r\n") {
		return nil, raw
	}
	normalized := strings.ReplaceAll(raw, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != "---" {
			continue
		}
		metaRaw := strings.Join(lines[1:i], "\n")
		body := strings.Join(lines[i+1:], "\n")
		var parsed map[string]any
		if err := yaml.Unmarshal([]byte(metaRaw), &parsed); err != nil {
			return map[string]any{"frontmatter_error": err.Error()}, body
		}
		return normalizeYAMLMap(parsed), body
	}
	return nil, raw
}

func chunkedMetadata(
	settings Settings,
	source Source,
	resolved resolvedPath,
) (map[string]any, error) {
	metadataPath := strings.TrimSpace(source.MetadataPath)
	if metadataPath == "" {
		chunksDir := filepath.Dir(resolved.localPath)
		if filepath.Base(chunksDir) == "chunks" {
			candidate := filepath.Join(filepath.Dir(chunksDir), "meta.yml")
			if _, err := os.Stat(candidate); err == nil {
				raw, err := os.ReadFile(candidate)
				if err != nil {
					return nil, fmt.Errorf("read metadata %q: %w", candidate, err)
				}
				return parseMetadata(raw, "meta.yml"), nil
			}
		}
		return nil, nil
	}

	metaResolved, err := resolveRuntimePath(settings, metadataPath)
	if err != nil {
		return nil, fmt.Errorf("metadata_path: %w", err)
	}
	raw, err := os.ReadFile(metaResolved.localPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read metadata %q: %w", metaResolved.runtimePath, err)
	}
	return parseMetadata(raw, metaResolved.runtimePath), nil
}

func parseMetadata(raw []byte, source string) map[string]any {
	var parsed map[string]any
	if err := yaml.Unmarshal(raw, &parsed); err != nil {
		return map[string]any{"metadata_error": err.Error(), "metadata_source": source}
	}
	out := normalizeYAMLMap(parsed)
	if len(out) > 0 {
		out["metadata_source"] = source
	}
	return out
}

func normalizeYAMLMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[strings.TrimSpace(key)] = normalizeYAMLValue(value)
	}
	return out
}

func normalizeYAMLValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		return normalizeYAMLMap(v)
	case []any:
		out := make([]any, 0, len(v))
		for _, item := range v {
			out = append(out, normalizeYAMLValue(item))
		}
		return out
	case int:
		return int64(v)
	case int64:
		return v
	case uint64:
		return strconv.FormatUint(v, 10)
	case float64, bool, string, nil:
		return v
	default:
		return fmt.Sprintf("%v", v)
	}
}

func basePayload(source Source, runtimePath string) map[string]any {
	payload := map[string]any{
		"source_id":   source.ID,
		"source_type": source.SourceType,
		"collection":  source.Collection,
		"file_path":   runtimePath,
	}
	name := strings.TrimSuffix(path.Base(runtimePath), path.Ext(runtimePath))
	if name != "" {
		payload["file_stem"] = name
	}
	dir := path.Dir(runtimePath)
	if dir != "." && dir != "/" {
		payload["parent_slug"] = path.Base(dir)
		grandparent := path.Base(path.Dir(dir))
		if grandparent != "." && grandparent != "/" {
			payload["container_slug"] = grandparent
		}
	}
	return payload
}

func sourceIncludes(source Source, rel string) bool {
	rel = strings.TrimPrefix(path.Clean(strings.ReplaceAll(rel, "\\", "/")), "./")
	includes := source.IncludeGlobs
	if len(includes) == 0 {
		includes = []string{"*.md", "**/*.md"}
	}
	included := false
	for _, pattern := range includes {
		if globMatch(pattern, rel) {
			included = true
			break
		}
	}
	if !included {
		return false
	}
	for _, pattern := range source.ExcludeGlobs {
		if globMatch(pattern, rel) {
			return false
		}
	}
	return true
}

func globMatch(pattern string, rel string) bool {
	pattern = strings.TrimSpace(strings.ReplaceAll(pattern, "\\", "/"))
	if pattern == "" {
		return false
	}
	if ok, _ := path.Match(pattern, rel); ok {
		return true
	}
	if strings.HasPrefix(pattern, "**/") {
		suffix := strings.TrimPrefix(pattern, "**/")
		if ok, _ := path.Match(suffix, path.Base(rel)); ok {
			return true
		}
		return strings.HasSuffix(rel, strings.TrimPrefix(pattern, "**"))
	}
	if strings.HasSuffix(pattern, "/**") {
		prefix := strings.TrimSuffix(pattern, "/**")
		return rel == prefix || strings.HasPrefix(rel, prefix+"/")
	}
	if !strings.Contains(pattern, "/") {
		ok, _ := path.Match(pattern, path.Base(rel))
		return ok
	}
	return false
}

func clonePayload(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func isLikelyUTF8(raw []byte) bool {
	return strings.ToValidUTF8(string(raw), "") == string(raw)
}
