package files

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/agent"
)

// PathAccessPolicy restricts file tools to exact runtime-visible paths.
//
// Empty allowlists deny that access mode. Leave the policy nil when no file
// path restriction is intended.
type PathAccessPolicy struct {
	ReadPaths  []string
	WritePaths []string
}

// FileToolAccess describes the paths one file-tool call attempts to access.
type FileToolAccess struct {
	ReadPaths  []string
	WritePaths []string
}

var _ agent.ToolCallPolicy = PathAccessPolicy{}

// CheckToolCall rejects file-tool calls that access paths outside the policy.
func (p PathAccessPolicy) CheckToolCall(call agent.ToolCall) error {
	name := strings.TrimSpace(call.Name)
	access, ok, err := InspectToolCallAccess(call)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	readAllowed := normalizedPathSet(p.ReadPaths)
	writeAllowed := normalizedPathSet(p.WritePaths)
	for _, candidate := range access.ReadPaths {
		if _, ok := readAllowed[cleanToolPath(candidate)]; !ok {
			return fmt.Errorf("%s path %q is outside allowed read paths", name, candidate)
		}
	}
	for _, candidate := range access.WritePaths {
		if _, ok := writeAllowed[cleanToolPath(candidate)]; !ok {
			return fmt.Errorf("%s path %q is outside allowed write paths", name, candidate)
		}
	}
	return nil
}

// InspectToolCallAccess extracts file path access from a supported file-tool
// call. The boolean return is false for non-file tools.
func InspectToolCallAccess(call agent.ToolCall) (FileToolAccess, bool, error) {
	name := strings.TrimSpace(call.Name)
	switch name {
	case "read_file":
		pathValue, err := toolPathArgument(call.Arguments)
		if err != nil {
			return FileToolAccess{}, true, err
		}
		return FileToolAccess{ReadPaths: []string{cleanToolPath(pathValue)}}, true, nil
	case "write_file", "edit_file":
		pathValue, err := toolPathArgument(call.Arguments)
		if err != nil {
			return FileToolAccess{}, true, err
		}
		return FileToolAccess{WritePaths: []string{cleanToolPath(pathValue)}}, true, nil
	case "apply_patch":
		paths, err := applyPatchArgumentPaths(call.Arguments)
		if err != nil {
			return FileToolAccess{}, true, err
		}
		return FileToolAccess{WritePaths: paths}, true, nil
	default:
		return FileToolAccess{}, false, nil
	}
}

func toolPathArgument(arguments string) (string, error) {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments JSON: %w", err)
	}
	pathValue := strings.TrimSpace(args.Path)
	if pathValue == "" {
		return "", fmt.Errorf("missing required argument: path")
	}
	return pathValue, nil
}

func applyPatchArgumentPaths(arguments string) ([]string, error) {
	var args struct {
		Patch string `json:"patch"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return nil, fmt.Errorf("invalid arguments JSON: %w", err)
	}
	if strings.TrimSpace(args.Patch) == "" {
		return nil, fmt.Errorf("missing required argument: patch")
	}

	paths := pathsFromPatchEnvelope(args.Patch)
	if len(paths) == 0 {
		return nil, fmt.Errorf("apply_patch must reference at least one file path")
	}
	return paths, nil
}

func pathsFromPatchEnvelope(patch string) []string {
	normalized := strings.ReplaceAll(patch, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")

	var paths []string
	for _, line := range strings.Split(normalized, "\n") {
		for _, prefix := range []string{
			"*** Add File: ",
			"*** Delete File: ",
			"*** Update File: ",
			"*** Move to: ",
		} {
			value, ok := strings.CutPrefix(line, prefix)
			if !ok {
				continue
			}
			pathValue := strings.TrimSpace(value)
			if pathValue != "" {
				paths = append(paths, pathValue)
			}
		}
	}
	return uniqueCleanPaths(paths)
}

func normalizedPathSet(paths []string) map[string]struct{} {
	out := make(map[string]struct{}, len(paths))
	for _, pathValue := range paths {
		pathValue = cleanToolPath(pathValue)
		if pathValue == "" {
			continue
		}
		out[pathValue] = struct{}{}
	}
	return out
}

func uniqueCleanPaths(paths []string) []string {
	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))
	for _, pathValue := range paths {
		cleaned := cleanToolPath(pathValue)
		if cleaned == "" {
			continue
		}
		if _, ok := seen[cleaned]; ok {
			continue
		}
		seen[cleaned] = struct{}{}
		out = append(out, cleaned)
	}
	return out
}

func cleanToolPath(pathValue string) string {
	pathValue = strings.TrimSpace(pathValue)
	if pathValue == "" {
		return ""
	}
	cleaned := path.Clean(pathValue)
	if cleaned == "." {
		return ""
	}
	return cleaned
}
