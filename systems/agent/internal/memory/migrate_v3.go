package memory

import (
	"github.com/q15co/q15/systems/agent/internal/conversation"
)

// migrateV3Messages rewrites legacy v3 transcript parts that used first-class
// "image" and "audio" PartTypes into the unified flat media model
// (MediaPartType + MediaKind). It must run before sanitizeStoredMessages /
// NormalizeParts, because once the constants are removed those legacy types are
// unknown and would be dropped as empty.
//
// All other part types pass through unchanged. Part order, roles, text,
// reasoning replay, tool calls/results, and temporal metadata are preserved
// because the rewrite touches only the Type/MediaKind fields of media parts.
func migrateV3Messages(messages []conversation.Message) []conversation.Message {
	if len(messages) == 0 {
		return nil
	}

	out := make([]conversation.Message, len(messages))
	for i, msg := range messages {
		out[i] = conversation.Message{
			Role:         msg.Role,
			Parts:        migrateV3Parts(msg.Parts),
			UserTemporal: msg.UserTemporal,
		}
	}
	return out
}

func migrateV3Parts(parts []conversation.Part) []conversation.Part {
	if len(parts) == 0 {
		return nil
	}

	out := make([]conversation.Part, 0, len(parts))
	for _, part := range parts {
		out = append(out, migrateV3Part(part))
	}
	return out
}

// migrateV3Part rewrites a single legacy v3 image/audio part into the flat
// media model. Legacy "image" parts preserve both MediaRef and DataURL; legacy
// "audio" parts preserve MediaRef. Every other part type is returned as-is so
// existing text, reasoning, tool-call, and tool-result data is untouched.
func migrateV3Part(part conversation.Part) conversation.Part {
	switch part.Type {
	case conversation.PartType("image"):
		return conversation.Image(part.MediaRef, part.DataURL)
	case conversation.PartType("audio"):
		return conversation.Audio(part.MediaRef)
	default:
		return part
	}
}
