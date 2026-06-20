package turnreply

import (
	"testing"

	"github.com/q15co/q15/systems/agent/internal/conversation"
)

func TestExtractPrefersAssistantAttachmentsOverToolFallback(t *testing.T) {
	got := Extract([]conversation.Message{
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

func TestCanonicalizePromotesAllToolAttachmentsAcrossTurn(t *testing.T) {
	got := Canonicalize([]conversation.Message{
		conversation.UserMessage("send the preview then the final image"),
		conversation.AssistantMessage(
			conversation.ToolCall("call-1", "load_image", `{"path":"preview.png"}`),
		),
		{
			Role: conversation.ToolRole,
			Parts: []conversation.Part{
				conversation.ToolResult("call-1", "Loaded preview", false),
				conversation.Image("media://sha256/preview", ""),
				conversation.Image("media://sha256/preview", ""),
			},
		},
		conversation.AssistantMessage(
			conversation.ToolCall("call-2", "load_image", `{"path":"final.png"}`),
		),
		{
			Role: conversation.ToolRole,
			Parts: []conversation.Part{
				conversation.ToolResult("call-2", "Loaded final", false),
				conversation.Image("media://sha256/final", ""),
				conversation.Image("media://sha256/final", ""),
			},
		},
		conversation.AssistantMessage(conversation.Text("done", "")),
	})

	if len(got) != 6 {
		t.Fatalf("Canonicalize len = %d, want 6", len(got))
	}
	if previewParts := got[2].Parts; len(previewParts) != 1 ||
		previewParts[0].Type != conversation.ToolResultPartType {
		t.Fatalf("preview tool parts = %#v, want tool_result only", previewParts)
	}
	if finalToolParts := got[4].Parts; len(finalToolParts) != 1 ||
		finalToolParts[0].Type != conversation.ToolResultPartType {
		t.Fatalf("final tool parts = %#v, want tool_result only", finalToolParts)
	}
	final := got[5]
	if final.Role != conversation.AssistantRole || len(final.Parts) != 3 {
		t.Fatalf("final assistant = %#v, want text plus preview and final image", final)
	}
	if final.Parts[0].Type != conversation.TextPartType {
		t.Fatalf("final assistant part[0] = %#v, want text", final.Parts[0])
	}
	if final.Parts[1].Type != conversation.ImagePartType ||
		final.Parts[1].MediaRef != "media://sha256/preview" {
		t.Fatalf("final attachment[0] = %#v, want preview image", final.Parts[1])
	}
	if final.Parts[2].Type != conversation.ImagePartType ||
		final.Parts[2].MediaRef != "media://sha256/final" {
		t.Fatalf("final attachment[1] = %#v, want final image", final.Parts[2])
	}

	selection := Extract(got)
	if len(selection.Attachments) != 2 {
		t.Fatalf("Extract().Attachments = %#v, want preview and final image", selection.Attachments)
	}
	if selection.Attachments[0].MediaRef != "media://sha256/preview" ||
		selection.Attachments[1].MediaRef != "media://sha256/final" {
		t.Fatalf("Extract().Attachments = %#v, want [preview, final]", selection.Attachments)
	}
}

func TestCanonicalizeSynthesizesImageOnlyAssistant(t *testing.T) {
	got := Canonicalize([]conversation.Message{
		conversation.UserMessage("send the image only"),
		conversation.AssistantMessage(
			conversation.ToolCall("call-1", "load_image", `{"path":"cat.png"}`),
		),
		{
			Role: conversation.ToolRole,
			Parts: []conversation.Part{
				conversation.ToolResult("call-1", "Loaded image: /workspace/cat.png", false),
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

func TestExtractCarriesAudioAttachmentFromToolResult(t *testing.T) {
	got := Extract([]conversation.Message{
		conversation.UserMessage("send the audio"),
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
		conversation.AssistantMessage(conversation.Text("done", "")),
	})

	if len(got.Attachments) != 1 ||
		got.Attachments[0].Type != conversation.AudioPartType ||
		got.Attachments[0].MediaRef != "media://sha256/au" {
		t.Fatalf("Extract().Attachments = %#v, want audio attachment", got.Attachments)
	}
}

// TestExtractSurvivesAudioAttachmentFollowedByUnrelatedTool locks in the
// regression from issue #110: an attachment produced by an earlier tool call
// (attach_audio) must survive a later, unrelated tool call (exec cleanup)
// instead of being silently dropped because it sits outside the trailing tool
// batch.
func TestExtractSurvivesAudioAttachmentFollowedByUnrelatedTool(t *testing.T) {
	got := Extract([]conversation.Message{
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

// TestCanonicalizePromotesAttachmentFollowedByUnrelatedTool mirrors the Extract
// regression for Canonicalize: the earlier tool attachment must be promoted
// onto the terminal assistant reply and stripped from its tool message even
// when a later, unrelated tool call separates it from the trailing tool batch.
func TestCanonicalizePromotesAttachmentFollowedByUnrelatedTool(t *testing.T) {
	got := Canonicalize([]conversation.Message{
		conversation.UserMessage("send the audio then clean up"),
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

	last := got[len(got)-1]
	if last.Role != conversation.AssistantRole {
		t.Fatalf("last role = %q, want assistant", last.Role)
	}
	var hasAudio bool
	for _, part := range last.Parts {
		if part.Type == conversation.AudioPartType && part.MediaRef == "media://sha256/au" {
			hasAudio = true
		}
	}
	if !hasAudio {
		t.Fatalf("final assistant parts = %#v, want audio attachment promoted", last.Parts)
	}

	for _, msg := range got {
		if msg.Role != conversation.ToolRole {
			continue
		}
		for _, part := range msg.Parts {
			if part.Type == conversation.AudioPartType {
				t.Fatalf("tool message still carries audio attachment: %#v", msg.Parts)
			}
		}
	}

	selection := Extract(got)
	if len(selection.Attachments) != 1 ||
		selection.Attachments[0].MediaRef != "media://sha256/au" {
		t.Fatalf(
			"Extract().Attachments = %#v, want promoted audio attachment",
			selection.Attachments,
		)
	}
}
