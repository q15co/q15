package media

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/fileops"
	q15media "github.com/q15co/q15/systems/agent/internal/media"
)

const attachMediaScope = "tool:attach_media"

type attachMediaKindPolicy struct {
	displayName          string
	contentTypePrefixes  []string
	contentTypes         []string
	maxBytes             int64
	normalizeContentType func(string) string
}

var attachMediaKinds = map[conversation.MediaKind]attachMediaKindPolicy{
	conversation.MediaKindImage: {
		displayName:         "image",
		contentTypePrefixes: []string{"image/"},
		maxBytes:            q15media.DefaultMaxImageBytes,
	},
	conversation.MediaKindAudio: {
		displayName:          "audio",
		contentTypePrefixes:  []string{"audio/"},
		maxBytes:             q15media.DefaultMaxAudioBytes,
		normalizeContentType: normalizeAudioContentType,
	},
	conversation.MediaKindVideo: {
		displayName:         "video",
		contentTypePrefixes: []string{"video/"},
	},
	conversation.MediaKindDocument: {
		displayName: "document",
	},
	conversation.MediaKindSticker: {
		displayName: "sticker",
	},
	conversation.MediaKindAnimation: {
		displayName:         "animation",
		contentTypePrefixes: []string{"video/"},
		contentTypes:        []string{"image/gif"},
	},
	conversation.MediaKindVideoNote: {
		displayName:         "video note",
		contentTypePrefixes: []string{"video/"},
	},
}

// AttachMedia registers a shared-root local file, or reuses a stored media ref,
// so it is sent to the user through the matching transport media endpoint.
type AttachMedia struct {
	paths      fileops.Settings
	mediaStore q15media.Store
}

// NewAttachMedia constructs an attach_media tool.
func NewAttachMedia(paths fileops.Settings, mediaStore q15media.Store) *AttachMedia {
	return &AttachMedia{
		paths:      paths,
		mediaStore: mediaStore,
	}
}

// Definition returns the tool schema exposed to the model.
func (a *AttachMedia) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name: "attach_media",
		Description: "Send a local or already-stored image, audio, video, document, sticker, " +
			"animation, or video note to the user",
		PromptGuidance: []string{
			"Use image for an image you want to deliver. Use load_image instead when the image is only for your own visual inspection.",
			"Use audio for audio files and voice messages. OGG/Opus is delivered as a Telegram voice note; other audio formats are sent as audio files.",
			"Use document for PDFs, archives, office files, and other general files.",
			"Use video for a normal video, animation for a GIF or silent looping MP4, sticker for a WEBP/TGS/WEBM sticker, and video_note for a circular MP4 video note.",
			"Generated files must be written under a shared root like /workspace, not /tmp, so this tool can access them.",
			"To resend an inbound attachment, pass its media_ref instead of path and preserve its media kind.",
		},
		DeliversAttachments: true,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"kind": map[string]any{
					"type": "string",
					"enum": []string{
						string(conversation.MediaKindImage),
						string(conversation.MediaKindAudio),
						string(conversation.MediaKindDocument),
						string(conversation.MediaKindVideo),
						string(conversation.MediaKindSticker),
						string(conversation.MediaKindAnimation),
						string(conversation.MediaKindVideoNote),
					},
					"description": "How the attachment should be delivered to the user.",
				},
				"path": map[string]any{
					"type":        "string",
					"description": "Path to the local file. Relative paths resolve from /workspace. Mutually exclusive with media_ref.",
				},
				"media_ref": map[string]any{
					"type":        "string",
					"description": "A media://sha256/... ref for an already-stored attachment. Mutually exclusive with path.",
				},
			},
			"required": []string{"kind"},
		},
	}
}

// Run executes one attach_media tool call from raw JSON arguments.
func (a *AttachMedia) Run(ctx context.Context, arguments string) (string, error) {
	result, err := a.RunResult(ctx, arguments)
	if err != nil {
		return "", err
	}
	return result.Output, nil
}

// RunResult executes one attach_media tool call and returns a structured tool
// result.
func (a *AttachMedia) RunResult(
	ctx context.Context,
	arguments string,
) (agent.ToolResult, error) {
	_ = ctx

	var args attachMediaArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return agent.ToolResult{}, fmt.Errorf("invalid arguments JSON: %w", err)
	}
	if a.mediaStore == nil {
		return agent.ToolResult{}, fmt.Errorf("media store is not configured")
	}

	kind, policy, err := parseAttachMediaKind(args.Kind)
	if err != nil {
		return agent.ToolResult{}, err
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
		if _, err := validateAttachMediaFile(localPath, meta.ContentType, policy); err != nil {
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
			return agent.ToolResult{}, fmt.Errorf(
				"stat %s file %q: %w",
				policy.displayName,
				runtimePath,
				err,
			)
		}
		if info.IsDir() {
			return agent.ToolResult{}, fmt.Errorf("path %q must reference a file", runtimePath)
		}

		contentType, err := detectMediaContentType(localPath)
		if err != nil {
			return agent.ToolResult{}, fmt.Errorf(
				"detect %s type for %q: %w",
				policy.displayName,
				runtimePath,
				err,
			)
		}
		contentType, err = validateAttachMediaFile(localPath, contentType, policy)
		if err != nil {
			return agent.ToolResult{}, err
		}

		ref, err = a.mediaStore.Store(localPath, q15media.Meta{
			Filename:    info.Name(),
			ContentType: contentType,
			Source:      attachMediaScope,
		}, attachMediaScope)
		if err != nil {
			return agent.ToolResult{}, fmt.Errorf(
				"register %s %q: %w",
				policy.displayName,
				runtimePath,
				err,
			)
		}
		displayPath = runtimePath
	}

	return agent.ToolResult{
		Output: fmt.Sprintf(
			"Attached %s: %s\nMedia-Ref: %s",
			policy.displayName,
			displayPath,
			ref,
		),
		Attachments: []conversation.Part{conversation.Media(kind, ref)},
	}, nil
}

type attachMediaArgs struct {
	Kind     string `json:"kind"`
	Path     string `json:"path"`
	MediaRef string `json:"media_ref"`
}

func parseAttachMediaKind(
	raw string,
) (conversation.MediaKind, attachMediaKindPolicy, error) {
	kind := conversation.MediaKind(strings.TrimSpace(raw))
	policy, ok := attachMediaKinds[kind]
	if !ok {
		return "", attachMediaKindPolicy{}, fmt.Errorf(
			"unsupported media kind %q: expected image, audio, video, document, sticker, animation, or video_note",
			raw,
		)
	}
	return kind, policy, nil
}

func detectMediaContentType(localPath string) (string, error) {
	f, err := os.Open(localPath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	header := make([]byte, 512)
	n, err := f.Read(header)
	if err != nil && err != io.EOF {
		return "", err
	}
	contentType := strings.ToLower(http.DetectContentType(header[:n]))
	if contentType != "application/octet-stream" {
		return contentType, nil
	}

	extensionType := mime.TypeByExtension(strings.ToLower(filepath.Ext(localPath)))
	if extensionType == "" {
		return contentType, nil
	}
	if mediaType, _, err := mime.ParseMediaType(extensionType); err == nil {
		return strings.ToLower(mediaType), nil
	}
	return strings.ToLower(extensionType), nil
}

func validateAttachMediaFile(
	localPath string,
	contentType string,
	policy attachMediaKindPolicy,
) (string, error) {
	info, err := os.Stat(localPath)
	if err != nil {
		return "", fmt.Errorf("stat media file %q: %w", localPath, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("media path %q must reference a file", localPath)
	}
	if policy.maxBytes > 0 && info.Size() > policy.maxBytes {
		return "", fmt.Errorf(
			"%s %q exceeds maximum size %d bytes",
			policy.displayName,
			localPath,
			policy.maxBytes,
		)
	}

	contentType = strings.ToLower(strings.TrimSpace(contentType))
	if contentType == "" {
		contentType, err = detectMediaContentType(localPath)
		if err != nil {
			return "", err
		}
	}
	if policy.normalizeContentType != nil {
		contentType = policy.normalizeContentType(contentType)
	}

	if len(policy.contentTypePrefixes) == 0 && len(policy.contentTypes) == 0 {
		return contentType, nil
	}
	for _, prefix := range policy.contentTypePrefixes {
		if strings.HasPrefix(contentType, prefix) {
			return contentType, nil
		}
	}
	for _, allowed := range policy.contentTypes {
		if contentType == allowed {
			return contentType, nil
		}
	}
	return "", fmt.Errorf(
		"%s %q has incompatible content type %q",
		policy.displayName,
		localPath,
		contentType,
	)
}

func normalizeAudioContentType(contentType string) string {
	switch contentType {
	case "application/ogg", "application/opus":
		return "audio/ogg"
	default:
		return contentType
	}
}
