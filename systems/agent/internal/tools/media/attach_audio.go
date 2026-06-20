package media

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/fileops"
	q15media "github.com/q15co/q15/systems/agent/internal/media"
)

const attachAudioScope = "tool:attach_audio"

// AttachAudio registers a shared-root local audio file in the media store so it
// is sent to the user as a voice/audio message.
type AttachAudio struct {
	paths      fileops.Settings
	mediaStore q15media.Store
}

// NewAttachAudio constructs an attach_audio tool.
func NewAttachAudio(paths fileops.Settings, mediaStore q15media.Store) *AttachAudio {
	return &AttachAudio{
		paths:      paths,
		mediaStore: mediaStore,
	}
}

// Definition returns the tool schema exposed to the model.
func (a *AttachAudio) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name:        "attach_audio",
		Description: "Register a local audio file from a shared runtime root so it is sent to the user as a voice/audio message",
		PromptGuidance: []string{
			"Use after exec or TTS produces an audio file (e.g. /workspace/out.ogg) you want to send as voice.",
			"Telegram voice notes should be OGG/Opus; other formats are sent as audio files.",
		},
		DeliversAttachments: true,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path to the local audio file. Relative paths resolve from /workspace.",
				},
			},
			"required": []string{"path"},
		},
	}
}

// Run executes one attach_audio tool call from raw JSON arguments.
func (a *AttachAudio) Run(ctx context.Context, arguments string) (string, error) {
	result, err := a.RunResult(ctx, arguments)
	if err != nil {
		return "", err
	}
	return result.Output, nil
}

// RunResult executes one attach_audio tool call and returns a structured tool result.
func (a *AttachAudio) RunResult(ctx context.Context, arguments string) (agent.ToolResult, error) {
	_ = ctx

	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return agent.ToolResult{}, fmt.Errorf("invalid arguments JSON: %w", err)
	}
	if a.mediaStore == nil {
		return agent.ToolResult{}, fmt.Errorf("media store is not configured")
	}

	localPath, runtimePath, err := fileops.ResolveLocalPath(a.paths, args.Path)
	if err != nil {
		return agent.ToolResult{}, err
	}

	info, err := os.Stat(localPath)
	if err != nil {
		return agent.ToolResult{}, fmt.Errorf("stat audio file %q: %w", runtimePath, err)
	}
	if info.IsDir() {
		return agent.ToolResult{}, fmt.Errorf("path %q must reference a file", runtimePath)
	}
	if info.Size() > int64(q15media.DefaultMaxImageBytes) {
		return agent.ToolResult{}, fmt.Errorf(
			"audio %q exceeds maximum size %d bytes",
			runtimePath,
			q15media.DefaultMaxImageBytes,
		)
	}

	contentType, err := detectAudioContentType(localPath)
	if err != nil {
		return agent.ToolResult{}, fmt.Errorf("detect audio type for %q: %w", runtimePath, err)
	}

	ref, err := a.mediaStore.Store(localPath, q15media.Meta{
		Filename:    info.Name(),
		ContentType: contentType,
		Source:      "tool:attach_audio",
	}, attachAudioScope)
	if err != nil {
		return agent.ToolResult{}, fmt.Errorf("register audio %q: %w", runtimePath, err)
	}

	return agent.ToolResult{
		Output:      fmt.Sprintf("Attached audio: %s\nMedia-Ref: %s", runtimePath, ref),
		Attachments: []conversation.Part{conversation.Audio(ref)},
	}, nil
}

func detectAudioContentType(localPath string) (string, error) {
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
	if !strings.HasPrefix(contentType, "audio/") &&
		contentType != "application/ogg" && contentType != "application/opus" {
		return "", fmt.Errorf("file does not appear to be audio (detected %q)", contentType)
	}
	// Normalize OGG containers so downstream voice detection works.
	if contentType == "application/ogg" || contentType == "application/opus" {
		return "audio/ogg", nil
	}
	return contentType, nil
}
