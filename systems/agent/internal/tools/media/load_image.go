// Package media provides media registration tools for the agent.
package media

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/fileops"
	q15media "github.com/q15co/q15/systems/agent/internal/media"
)

const loadImageScope = "tool:load_image"

// LoadImage registers a shared-root local image in the media store so the
// model can inspect it on the next turn.
type LoadImage struct {
	paths      fileops.Settings
	mediaStore q15media.Store
}

// NewLoadImage constructs a load_image tool.
func NewLoadImage(paths fileops.Settings, mediaStore q15media.Store) *LoadImage {
	return &LoadImage{
		paths:      paths,
		mediaStore: mediaStore,
	}
}

// Definition returns the tool schema exposed to the model.
func (l *LoadImage) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name:        "load_image",
		Description: "Register a local image file from a shared runtime root so the model can inspect it with vision on the next turn",
		PromptGuidance: []string{
			"Use after exec or another tool creates an image file you need to inspect.",
			"Generated images must be written under a shared root like /workspace, not /tmp, so this tool can access them.",
		},
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path to the local image file. Relative paths resolve from /workspace.",
				},
			},
			"required": []string{"path"},
		},
	}
}

// Run executes one load_image tool call from raw JSON arguments.
func (l *LoadImage) Run(ctx context.Context, arguments string) (string, error) {
	result, err := l.RunResult(ctx, arguments)
	if err != nil {
		return "", err
	}
	return result.Output, nil
}

// RunResult executes one load_image tool call and returns a structured tool result.
func (l *LoadImage) RunResult(ctx context.Context, arguments string) (agent.ToolResult, error) {
	_ = ctx

	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return agent.ToolResult{}, fmt.Errorf("invalid arguments JSON: %w", err)
	}
	if l.mediaStore == nil {
		return agent.ToolResult{}, fmt.Errorf("media store is not configured")
	}

	localPath, runtimePath, err := fileops.ResolveLocalPath(l.paths, args.Path)
	if err != nil {
		return agent.ToolResult{}, err
	}

	info, err := os.Stat(localPath)
	if err != nil {
		return agent.ToolResult{}, fmt.Errorf("stat image file %q: %w", runtimePath, err)
	}
	if info.IsDir() {
		return agent.ToolResult{}, fmt.Errorf("path %q must reference a file", runtimePath)
	}
	if info.Size() > int64(q15media.DefaultMaxImageBytes) {
		return agent.ToolResult{}, fmt.Errorf(
			"image %q exceeds maximum size %d bytes",
			runtimePath,
			q15media.DefaultMaxImageBytes,
		)
	}

	contentType, err := detectImageContentType(localPath)
	if err != nil {
		return agent.ToolResult{}, fmt.Errorf("detect image type for %q: %w", runtimePath, err)
	}

	ref, err := l.mediaStore.Store(localPath, q15media.Meta{
		Filename:    info.Name(),
		ContentType: contentType,
		Source:      "tool:load_image",
	}, loadImageScope)
	if err != nil {
		return agent.ToolResult{}, fmt.Errorf("register image %q: %w", runtimePath, err)
	}

	return agent.ToolResult{
		Output:    fmt.Sprintf("Loaded image: %s\nMedia-Ref: %s", runtimePath, ref),
		MediaRefs: []string{ref},
	}, nil
}

func detectImageContentType(localPath string) (string, error) {
	f, err := os.Open(localPath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	header := make([]byte, 512)
	n, err := f.Read(header)
	if err != nil {
		return "", err
	}
	contentType := strings.ToLower(http.DetectContentType(header[:n]))
	if !strings.HasPrefix(contentType, "image/") {
		return "", fmt.Errorf("file does not appear to be an image (detected %q)", contentType)
	}
	return contentType, nil
}
