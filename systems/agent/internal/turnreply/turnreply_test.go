package turnreply

import (
	"testing"

	"github.com/q15co/q15/systems/agent/internal/conversation"
)

// deliverTools is the canonical deliver set used across these tests: the two
// explicit "send to the user" tools. load_image is deliberately absent.
var deliverTools = map[string]struct{}{
	"attach_audio": {},
	"attach_image": {},
}

func extractor() *Extractor {
	return NewExtractor(deliverTools)
}

func TestExtractPrefersAssistantAttachmentsOverToolFallback(t *testing.T) {
	got := (&Extractor{deliverTools: deliverTools}).Extract([]conversation.Message{
		{
			Role: conversation.ToolRole,
			Parts: []conversation.Part{
				conversation.ToolResult("call-1", "loaded", false),
				conversation.Image("media://sha256/tool", ""),
			},
		},
		conversation.AssistantMessage(
			conversation.Text("done", ""),
			conversation.Image("media://sha256/assistant", ""),
			conversation.Image("media://sha256/assistant", ""),
		),
	})

	if got.Text != "done" {
		t.Fatalf("Extract().Text = %q, want done", got.Text)
	}
	if len(got.Attachments) != 1 || got.Attachments[0].MediaRef != "media://sha256/assistant" {
		t.Fatalf("Extract().Attachments = %#v, want assistant attachment", got.Attachments)
	}
}

// TestExtractPromotesDeliverToolAttachmentFollowedByUnrelatedTool locks in the
// #110 fix: an attachment produced by a deliver tool (attach_audio) survives a
// later, unrelated tool call instead of being silently dropped because it sits
// outside the trailing tool block.
func TestExtractPromotesDeliverToolAttachmentFollowedByUnrelatedTool(t *testing.T) {
	got := extractor().Extract([]conversation.Message{
		conversation.UserMessage("send the voice note then clean up"),
		conversation.AssistantMessage(
			conversation.ToolCall("call-1", "attach_audio", `{"path":"out.ogg"}`),
		),
		{
			Role: conversation.ToolRole,
			Parts: []conversation.Part{
				conversation.ToolResult("call-1", "Attached audio: /workspace/out.ogg", false),
				conversation.Audio("media://sha256/au"),
			},
		},
		conversation.AssistantMessage(
			conversation.ToolCall("call-2", "exec", `{"command":"rm -f /tmp/*"}`),
		),
		{
			Role: conversation.ToolRole,
			Parts: []conversation.Part{
				conversation.ToolResult("call-2", "cleaned up", false),
			},
		},
		conversation.AssistantMessage(conversation.Text("done", "")),
	})

	if got.Text != "done" {
		t.Fatalf("Extract().Text = %q, want done", got.Text)
	}
	if len(got.Attachments) != 1 ||
		got.Attachments[0].Type != conversation.AudioPartType ||
		got.Attachments[0].MediaRef != "media://sha256/au" {
		t.Fatalf("Extract().Attachments = %#v, want audio attachment preserved", got.Attachments)
	}
}

// TestExtractDoesNotPromoteVisionOnlyLoadImage guards against the leak: an
// image loaded for agent-internal vision must never reach the user, even when
// no deliver attachment exists to take precedence.
func TestExtractDoesNotPromoteVisionOnlyLoadImage(t *testing.T) {
	got := extractor().Extract([]conversation.Message{
		conversation.UserMessage("inspect this image"),
		conversation.AssistantMessage(
			conversation.ToolCall("call-1", "load_image", `{"path":"preview.png"}`),
		),
		{
			Role: conversation.ToolRole,
			Parts: []conversation.Part{
				conversation.ToolResult("call-1", "Loaded image: /workspace/preview.png", false),
				conversation.Image("media://sha256/vision", ""),
			},
		},
		conversation.AssistantMessage(conversation.Text("it is a cat", "")),
	})

	if len(got.Attachments) != 0 {
		t.Fatalf(
			"Extract().Attachments = %#v, want none (load_image is vision-only)",
			got.Attachments,
		)
	}
}

// TestExtractPromotesAttachImage verifies the new deliberate image-delivery
// tool is promoted position-independently.
func TestExtractPromotesAttachImage(t *testing.T) {
	got := extractor().Extract([]conversation.Message{
		conversation.UserMessage("send me the rendered chart"),
		conversation.AssistantMessage(
			conversation.ToolCall("call-1", "exec", `{"command":"render chart"}`),
		),
		{
			Role: conversation.ToolRole,
			Parts: []conversation.Part{
				conversation.ToolResult("call-1", "rendered /workspace/chart.png", false),
			},
		},
		conversation.AssistantMessage(
			conversation.ToolCall("call-2", "attach_image", `{"path":"chart.png"}`),
		),
		{
			Role: conversation.ToolRole,
			Parts: []conversation.Part{
				conversation.ToolResult("call-2", "Attached image: /workspace/chart.png", false),
				conversation.Image("media://sha256/chart", ""),
			},
		},
		conversation.AssistantMessage(conversation.Text("here it is", "")),
	})

	if len(got.Attachments) != 1 ||
		got.Attachments[0].Type != conversation.ImagePartType ||
		got.Attachments[0].MediaRef != "media://sha256/chart" {
		t.Fatalf("Extract().Attachments = %#v, want the attached image", got.Attachments)
	}
}

// TestExtractSkipsOrphanedToolResult ensures a tool result with no matching
// tool_call is never promoted (and does not panic). Orphaned results cannot be
// attributed to a deliver tool, so they are left alone — the safe default.
func TestExtractSkipsOrphanedToolResult(t *testing.T) {
	got := extractor().Extract([]conversation.Message{
		conversation.UserMessage("send the audio"),
		{
			Role: conversation.ToolRole,
			Parts: []conversation.Part{
				// No prior assistant tool_call carries id "call-9".
				conversation.ToolResult("call-9", "Attached audio", false),
				conversation.Audio("media://sha256/orphan"),
			},
		},
		conversation.AssistantMessage(conversation.Text("done", "")),
	})

	if len(got.Attachments) != 0 {
		t.Fatalf(
			"Extract().Attachments = %#v, want none (orphaned result not promoted)",
			got.Attachments,
		)
	}
}

// TestExtractDedupesSameRefAcrossDeliverResults verifies that the same media
// ref produced by two deliver-tool results is delivered exactly once.
func TestExtractDedupesSameRefAcrossDeliverResults(t *testing.T) {
	got := extractor().Extract([]conversation.Message{
		conversation.UserMessage("send the audio twice"),
		conversation.AssistantMessage(
			conversation.ToolCall("call-1", "attach_audio", `{"path":"a.ogg"}`),
		),
		{
			Role: conversation.ToolRole,
			Parts: []conversation.Part{
				conversation.ToolResult("call-1", "Attached audio", false),
				conversation.Audio("media://sha256/same"),
			},
		},
		conversation.AssistantMessage(
			conversation.ToolCall("call-2", "attach_audio", `{"path":"b.ogg"}`),
		),
		{
			Role: conversation.ToolRole,
			Parts: []conversation.Part{
				conversation.ToolResult("call-2", "Attached audio", false),
				conversation.Audio("media://sha256/same"),
			},
		},
		conversation.AssistantMessage(conversation.Text("done", "")),
	})

	if len(got.Attachments) != 1 || got.Attachments[0].MediaRef != "media://sha256/same" {
		t.Fatalf("Extract().Attachments = %#v, want single deduped audio", got.Attachments)
	}
}

// TestEmptyDeliverSetPromotesNothing verifies the leak-safe default: with no
// deliver tools configured, nothing is promoted from tool messages (assistant-
// owned attachments would still be collected, but none exist here).
func TestEmptyDeliverSetPromotesNothing(t *testing.T) {
	got := NewExtractor(nil).Extract([]conversation.Message{
		conversation.UserMessage("send the audio"),
		conversation.AssistantMessage(
			conversation.ToolCall("call-1", "attach_audio", `{"path":"out.ogg"}`),
		),
		{
			Role: conversation.ToolRole,
			Parts: []conversation.Part{
				conversation.ToolResult("call-1", "Attached audio", false),
				conversation.Audio("media://sha256/au"),
			},
		},
		conversation.AssistantMessage(conversation.Text("done", "")),
	})

	if len(got.Attachments) != 0 {
		t.Fatalf("Extract().Attachments = %#v, want none for empty deliver set", got.Attachments)
	}
}

func TestCanonicalizePromotesAllDeliverAttachmentsAcrossTurn(t *testing.T) {
	got := extractor().Canonicalize([]conversation.Message{
		conversation.UserMessage("send the preview voice then the final image"),
		conversation.AssistantMessage(
			conversation.ToolCall("call-1", "attach_audio", `{"path":"voice.ogg"}`),
		),
		{
			Role: conversation.ToolRole,
			Parts: []conversation.Part{
				conversation.ToolResult("call-1", "Attached audio", false),
				conversation.Audio("media://sha256/voice"),
				conversation.Audio("media://sha256/voice"),
			},
		},
		conversation.AssistantMessage(
			conversation.ToolCall("call-2", "attach_image", `{"path":"final.png"}`),
		),
		{
			Role: conversation.ToolRole,
			Parts: []conversation.Part{
				conversation.ToolResult("call-2", "Attached image", false),
				conversation.Image("media://sha256/final", ""),
				conversation.Image("media://sha256/final", ""),
			},
		},
		conversation.AssistantMessage(conversation.Text("done", "")),
	})

	if len(got) != 6 {
		t.Fatalf("Canonicalize len = %d, want 6", len(got))
	}
	if audioToolParts := got[2].Parts; len(audioToolParts) != 1 ||
		audioToolParts[0].Type != conversation.ToolResultPartType {
		t.Fatalf("audio tool parts = %#v, want tool_result only", audioToolParts)
	}
	if imageToolParts := got[4].Parts; len(imageToolParts) != 1 ||
		imageToolParts[0].Type != conversation.ToolResultPartType {
		t.Fatalf("image tool parts = %#v, want tool_result only", imageToolParts)
	}
	final := got[5]
	if final.Role != conversation.AssistantRole || len(final.Parts) != 3 {
		t.Fatalf("final assistant = %#v, want text plus voice and final image", final)
	}
	if final.Parts[0].Type != conversation.TextPartType {
		t.Fatalf("final assistant part[0] = %#v, want text", final.Parts[0])
	}
	if final.Parts[1].Type != conversation.AudioPartType ||
		final.Parts[1].MediaRef != "media://sha256/voice" {
		t.Fatalf("final attachment[0] = %#v, want voice audio", final.Parts[1])
	}
	if final.Parts[2].Type != conversation.ImagePartType ||
		final.Parts[2].MediaRef != "media://sha256/final" {
		t.Fatalf("final attachment[1] = %#v, want final image", final.Parts[2])
	}

	selection := extractor().Extract(got)
	if len(selection.Attachments) != 2 {
		t.Fatalf("Extract().Attachments = %#v, want voice and final image", selection.Attachments)
	}
}

// TestCanonicalizeKeepsVisionImageOnToolMessage verifies the other half of the
// intent split: a non-deliver (vision) image is NOT promoted and NOT stripped,
// so it stays on its tool message for model inspection and cannot leak.
func TestCanonicalizeKeepsVisionImageOnToolMessage(t *testing.T) {
	got := extractor().Canonicalize([]conversation.Message{
		conversation.UserMessage("inspect this image and tell me"),
		conversation.AssistantMessage(
			conversation.ToolCall("call-1", "load_image", `{"path":"cat.png"}`),
		),
		{
			Role: conversation.ToolRole,
			Parts: []conversation.Part{
				conversation.ToolResult("call-1", "Loaded image", false),
				conversation.Image("media://sha256/cat", ""),
			},
		},
		conversation.AssistantMessage(conversation.Text("it is a cat", "")),
	})

	// Vision image survives on the tool message, and is not on the assistant.
	var toolImages, assistantImages int
	for _, msg := range got {
		for _, part := range msg.Parts {
			if part.Type != conversation.ImagePartType {
				continue
			}
			switch msg.Role {
			case conversation.ToolRole:
				toolImages++
			case conversation.AssistantRole:
				assistantImages++
			}
		}
	}
	if toolImages != 1 {
		t.Fatalf("tool images = %d, want vision image retained on tool message", toolImages)
	}
	if assistantImages != 0 {
		t.Fatalf("assistant images = %d, want vision image not promoted", assistantImages)
	}
}

// TestCanonicalizePreservesToolCallAndToolResultMessages locks in the #86
// invariant: a message carrying a tool_call or tool_result part is never
// compacted away, so tool results stay anchored to their calls and call ids
// stay resolvable for delivery.
func TestCanonicalizePreservesToolCallAndToolResultMessages(t *testing.T) {
	// Assistant message with ONLY a tool_call (no text) + its tool_result with no
	// deliverable attachment: both must survive compaction.
	got := extractor().Canonicalize([]conversation.Message{
		conversation.AssistantMessage(
			conversation.ToolCall("call-1", "exec", `{"command":"echo hi"}`),
		),
		conversation.ToolResultMessage("call-1", "hi", false),
		conversation.AssistantMessage(conversation.Text("done", "")),
	})

	var hasToolCall, hasToolResult bool
	for _, msg := range got {
		for _, part := range msg.Parts {
			switch part.Type {
			case conversation.ToolCallPartType:
				hasToolCall = true
			case conversation.ToolResultPartType:
				hasToolResult = true
			}
		}
	}
	if !hasToolCall {
		t.Fatalf("canonicalized messages = %#v, want tool_call part retained", got)
	}
	if !hasToolResult {
		t.Fatalf("canonicalized messages = %#v, want tool_result part retained", got)
	}
}

func TestCanonicalizeSynthesizesImageOnlyAssistant(t *testing.T) {
	got := extractor().Canonicalize([]conversation.Message{
		conversation.UserMessage("send the image only"),
		conversation.AssistantMessage(
			conversation.ToolCall("call-1", "attach_image", `{"path":"cat.png"}`),
		),
		{
			Role: conversation.ToolRole,
			Parts: []conversation.Part{
				conversation.ToolResult("call-1", "Attached image: /workspace/cat.png", false),
				conversation.Image("media://sha256/cat", ""),
			},
		},
	})

	if len(got) != 4 {
		t.Fatalf("Canonicalize len = %d, want 4", len(got))
	}
	last := got[len(got)-1]
	if last.Role != conversation.AssistantRole {
		t.Fatalf("last role = %q, want assistant", last.Role)
	}
	if len(last.Parts) != 1 || last.Parts[0].Type != conversation.ImagePartType {
		t.Fatalf("last assistant parts = %#v, want image-only assistant", last.Parts)
	}
}
