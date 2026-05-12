package embed

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

type rootMapping struct {
	runtime string
	local   string
}

type resolvedPath struct {
	runtimePath string
	localPath   string
	rootRuntime string
	rel         string
}

func cleanRuntimePath(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("path is required")
	}
	raw = path.Clean(strings.ReplaceAll(raw, "\\", "/"))
	if !strings.HasPrefix(raw, "/") {
		raw = "/workspace/" + raw
		raw = path.Clean(raw)
	}
	return raw, nil
}

func resolveRuntimePath(settings Settings, raw string) (resolvedPath, error) {
	runtimePath, err := cleanRuntimePath(raw)
	if err != nil {
		return resolvedPath{}, err
	}

	for _, root := range runtimeRoots(settings) {
		if root.local == "" {
			continue
		}
		rel, ok := pathRel(root.runtime, runtimePath)
		if !ok {
			continue
		}
		localPath := filepath.Join(root.local, filepath.FromSlash(rel))
		return resolvedPath{
			runtimePath: runtimePath,
			localPath:   filepath.Clean(localPath),
			rootRuntime: root.runtime,
			rel:         rel,
		}, nil
	}

	return resolvedPath{}, fmt.Errorf(
		"path %q must be under /workspace, /memory, or /skills",
		runtimePath,
	)
}

func runtimeRoots(settings Settings) []rootMapping {
	return []rootMapping{
		{runtime: "/workspace", local: settings.WorkspaceLocalDir},
		{runtime: "/memory", local: settings.MemoryLocalDir},
		{runtime: "/skills", local: settings.SkillsLocalDir},
	}
}

func pathRel(root string, candidate string) (string, bool) {
	root = path.Clean(root)
	candidate = path.Clean(candidate)
	if candidate == root {
		return ".", true
	}
	prefix := strings.TrimSuffix(root, "/") + "/"
	if !strings.HasPrefix(candidate, prefix) {
		return "", false
	}
	return strings.TrimPrefix(candidate, prefix), true
}

func childRuntimePath(parentRuntime string, rel string) string {
	if rel == "." || rel == "" {
		return path.Clean(parentRuntime)
	}
	return path.Clean(parentRuntime + "/" + rel)
}

func defaultRegistryPath(settings Settings) string {
	if strings.TrimSpace(settings.RegistryPath) != "" {
		return filepath.Clean(settings.RegistryPath)
	}
	return filepath.Join(
		settings.WorkspaceLocalDir,
		filepath.FromSlash(DefaultRegistryRelativePath),
	)
}

func defaultStatePath(settings Settings) string {
	if strings.TrimSpace(settings.StatePath) != "" {
		return filepath.Clean(settings.StatePath)
	}
	return filepath.Join(settings.WorkspaceLocalDir, filepath.FromSlash(DefaultStateRelativePath))
}

func normalizeModel(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return DefaultEmbeddingModel
	}
	return model
}

func normalizeDimensions(dimensions int) int {
	if dimensions == 0 {
		return DefaultEmbeddingDimensions
	}
	return dimensions
}
