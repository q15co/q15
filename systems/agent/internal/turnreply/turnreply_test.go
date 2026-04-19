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

func TestCanonicalizePromotesTrailingToolBatchOnly(t *testing.T) {
	got := Canonicalize([]conversation.Message{
		conversation.UserMessage("send the final image"),
		conversation.AssistantMessage(
			conversation.ToolCall("call-1", "load_image", `{"path":"preview.png"}`),
		),
		{
			Role: conversation.ToolRole,
			Parts: []conversation.Part{
				conversation.ToolResult("call-1", "Loaded preview", false),
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
	if previewParts := got[2].Parts; len(previewParts) != 2 {
		t.Fatalf("preview tool parts = %#v, want preview tool result plus image", previewParts)
	}
	if finalToolParts := got[4].Parts; len(finalToolParts) != 1 ||
		finalToolParts[0].Type != conversation.ToolResultPartType {
		t.Fatalf("final tool parts = %#v, want tool_result only", finalToolParts)
	}
	final := got[5]
	if final.Role != conversation.AssistantRole || len(final.Parts) != 2 {
		t.Fatalf("final assistant = %#v, want text plus one image", final)
	}
	if final.Parts[1].Type != conversation.ImagePartType ||
		final.Parts[1].MediaRef != "media://sha256/final" {
		t.Fatalf("final attachment = %#v, want final image", final.Parts[1])
	}

	selection := Extract(got)
	if len(selection.Attachments) != 1 ||
		selection.Attachments[0].MediaRef != "media://sha256/final" {
		t.Fatalf("Extract().Attachments = %#v, want only final image", selection.Attachments)
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
