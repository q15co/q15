package media

import (
	"fmt"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
)

// Support describes which media kinds the selected model can render inline.
// Unsupported kinds are downgraded to text hints before the provider sees them.
// A nil or empty map renders every media kind as a text hint.
type Support map[conversation.MediaKind]bool

// providerInlineKinds is the set of media kinds for which at least one provider
// client has a real inline serialization path today. A model catalog capability
// is necessary but not sufficient for inline rendering: the kind must also
// appear here, otherwise it is downgraded to a text hint regardless of what the
// model declares.
//
// Only image is serialized by provider clients today. Audio is declared by some
// models (Capabilities.AudioInput) but no provider serializes it yet, so it is
// deliberately absent here and always downgraded to a hint. When a provider
// gains a real serializer for a kind, add it to this set — nothing else in the
// adaptation layer needs to change.
var providerInlineKinds = map[conversation.MediaKind]bool{
	conversation.MediaKindImage: true,
}

// SupportFromCapabilities builds the per-turn media Support set from a model's
// catalog capabilities, gating each declared capability on whether a provider
// client can actually serialize that media kind inline (see providerInlineKinds).
//
// This is the single sanctioned mapping from modelcatalog.Capabilities to the
// Support map consumed by AdaptMediaToCapabilities; every production Support
// builder must go through it so the "is there a provider serializer?" truth
// lives in exactly one place.
func SupportFromCapabilities(caps modelcatalog.Capabilities) Support {
	support := Support{}
	if caps.ImageInput && providerInlineKinds[conversation.MediaKindImage] {
		support[conversation.MediaKindImage] = true
	}
	if caps.AudioInput && providerInlineKinds[conversation.MediaKindAudio] {
		support[conversation.MediaKindAudio] = true
	}
	return support
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
		case conversation.MediaPartType:
			if support[part.MediaKind] {
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
	kind := string(part.MediaKind)
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

func mediaHintGuidance(kind string) string {
	switch kind {
	case "image":
		return "The current model cannot display this media inline. Use load_image with the media_ref to inspect it with vision on the next turn, or delegate to a model that supports image input."
	case "audio":
		return "The current model cannot process this media inline. Use exec tools to process the file at the path above (e.g. transcribe or transcode)."
	case "video", "video_note", "animation":
		return "The current model cannot process this media inline. Use exec tools to process the file at the path above (e.g. extract frames or audio with ffmpeg)."
	case "document":
		return "The current model cannot read this file inline. Use exec tools to access the file at the path above."
	case "sticker":
		return "The current model cannot render this sticker inline. It is available as a file at the path above."
	default:
		return "The current model cannot process this media inline. Use exec tools to process the file at the path above."
	}
}
