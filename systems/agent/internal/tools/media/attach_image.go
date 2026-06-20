package media

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/fileops"
	q15media "github.com/q15co/q15/systems/agent/internal/media"
)

const attachImageScope = "tool:attach_image"

// AttachImage registers a shared-root local image in the media store so it is
// sent to the user as an image message. It is the explicit delivery counterpart
// to load_image: load_image is agent-internal vision only, while attach_image
// is the deliberate "send this image to the user" action.
type AttachImage struct {
	paths      fileops.Settings
	mediaStore q15media.Store
}

// NewAttachImage constructs an attach_image tool.
func NewAttachImage(paths fileops.Settings, mediaStore q15media.Store) *AttachImage {
	return &AttachImage{
		paths:      paths,
		mediaStore: mediaStore,
	}
}

// Definition returns the tool schema exposed to the model.
func (a *AttachImage) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name:        "attach_image",
		Description: "Register a local image file from a shared runtime root so it is sent to the user as an image message",
		PromptGuidance: []string{
			"Use to send an image to the user after exec or another tool creates one you want to deliver.",
			"For agent-internal vision inspection (the model looking at an image), use load_image instead.",
			"Generated images must be written under a shared root like /workspace, not /tmp, so this tool can access them.",
		},
		DeliversAttachments: true,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path to the local image file. Relative paths resolve from /workspace.",
				},
				"media_ref": map[string]any{
					"type":        "string",
					"description": "A media://sha256/... ref for an already-stored image (e.g. from an inbound Telegram attachment). Mutually exclusive with path.",
				},
			},
		},
	}
}

// Run executes one attach_image tool call from raw JSON arguments.
func (a *AttachImage) Run(ctx context.Context, arguments string) (string, error) {
	result, err := a.RunResult(ctx, arguments)
	if err != nil {
		return "", err
	}
	return result.Output, nil
}

// RunResult executes one attach_image tool call and returns a structured tool
// result.
func (a *AttachImage) RunResult(ctx context.Context, arguments string) (agent.ToolResult, error) {
	_ = ctx

	var args attachImageArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return agent.ToolResult{}, fmt.Errorf("invalid arguments JSON: %w", err)
	}
	if a.mediaStore == nil {
		return agent.ToolResult{}, fmt.Errorf("media store is not configured")
	}

	args.Path = strings.TrimSpace(args.Path)
	args.MediaRef = strings.TrimSpace(args.MediaRef)

	if args.Path == "" && args.MediaRef == "" {
		return agent.ToolResult{}, fmt.Errorf("exactly one of path or media_ref is required")
	}
	if args.Path != "" && args.MediaRef != "" {
		return agent.ToolResult{}, fmt.Errorf("path and media_ref are mutually exclusive")
	}

	var ref string
	var displayPath string
	if args.MediaRef != "" {
		localPath, meta, err := a.mediaStore.Resolve(args.MediaRef)
		if err != nil {
			return agent.ToolResult{}, fmt.Errorf("resolve media_ref %q: %w", args.MediaRef, err)
		}
		if err := validateImageMeta(localPath, meta); err != nil {
			return agent.ToolResult{}, err
		}
		ref = args.MediaRef
		displayPath = args.MediaRef
	} else {
		localPath, runtimePath, err := fileops.ResolveLocalPath(a.paths, args.Path)
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

		ref, err = a.mediaStore.Store(localPath, q15media.Meta{
			Filename:    info.Name(),
			ContentType: contentType,
			Source:      "tool:attach_image",
		}, attachImageScope)
		if err != nil {
			return agent.ToolResult{}, fmt.Errorf("register image %q: %w", runtimePath, err)
		}
		displayPath = runtimePath
	}

	return agent.ToolResult{
		Output:      fmt.Sprintf("Attached image: %s\nMedia-Ref: %s", displayPath, ref),
		Attachments: []conversation.Part{conversation.Image(ref, "")},
	}, nil
}

type attachImageArgs struct {
	Path     string `json:"path"`
	MediaRef string `json:"media_ref"`
}
