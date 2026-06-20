package media

import (
	"fmt"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/conversation"
)

// Support describes which media types the selected model can render
// inline. Unsupported types are downgraded to text hints before the provider
// sees them.
type Support struct {
	Image bool
	Audio bool
}

// AdaptMediaToCapabilities returns a copy of messages where media parts the
// selected model cannot handle inline are replaced with text-hint parts.
// Supported media parts are kept as typed parts for the provider to serialize.
//
// The canonical transcript is never mutated: the function clones the input and
// operates on the copy. The same transcript may therefore be rendered against
// a text-only model on one turn (hints) and a vision model on another (inline)
// without corruption.
func AdaptMediaToCapabilities(
	messages []conversation.Message,
	support Support,
	store Store,
) []conversation.Message {
	out := conversation.CloneMessages(messages)
	for i := range out {
		out[i].Parts = adaptParts(out[i].Parts, support, store)
	}
	return out
}

func adaptParts(
	parts []conversation.Part,
	support Support,
	store Store,
) []conversation.Part {
	if len(parts) == 0 {
		return nil
	}
	adapted := make([]conversation.Part, 0, len(parts))
	for _, part := range conversation.NormalizeParts(parts) {
		switch part.Type {
		case conversation.ImagePartType:
			if support.Image {
				adapted = append(adapted, part)
			} else {
				adapted = append(adapted, conversation.Text(mediaHintText(part, store), ""))
			}
		case conversation.AudioPartType:
			if support.Audio {
				adapted = append(adapted, part)
			} else {
				adapted = append(adapted, conversation.Text(mediaHintText(part, store), ""))
			}
		default:
			adapted = append(adapted, part)
		}
	}
	return conversation.NormalizeParts(adapted)
}

// mediaHintText produces a text fallback for a media part the model cannot
// render inline. It includes the kind, media ref, the resolved file path
// (when the store can resolve it), and a one-line guidance hint.
//
// A nil store or an unresolvable ref omits the File: line gracefully; the hint
// never returns an error.
func mediaHintText(part conversation.Part, store Store) string {
	part = conversation.NormalizePart(part)
	kind := mediaKind(part.Type)
	ref := strings.TrimSpace(part.MediaRef)

	lines := []string{
		fmt.Sprintf("[Media: %s]", kind),
		"Media-Ref: " + ref,
	}
	if store != nil && ref != "" {
		if localPath, _, err := store.Resolve(ref); err == nil && localPath != "" {
			lines = append(lines, "File: "+localPath)
		}
	}
	lines = append(lines, mediaHintGuidance(kind))
	return strings.Join(lines, "\n")
}

func mediaKind(partType conversation.PartType) string {
	switch partType {
	case conversation.ImagePartType:
		return "image"
	case conversation.AudioPartType:
		return "audio"
	default:
		return string(partType)
	}
}

func mediaHintGuidance(kind string) string {
	switch kind {
	case "image":
		return "The current model cannot display this media inline. Use load_image with the media_ref to inspect it with vision on the next turn, or delegate to a model that supports image input."
	case "audio":
		return "The current model cannot process this media inline. Use exec tools to process the file at the path above (e.g. transcribe or transcode)."
	default:
		return "The current model cannot process this media inline. Use exec tools to process the file at the path above."
	}
}
