package modelselection

import (
	"testing"

	"github.com/q15co/q15/systems/agent/internal/conversation"
)

func TestInferRequirementsDefaultsToTextOnly(t *testing.T) {
	got := InferRequirements(Request{
		Messages: []conversation.Message{
			conversation.SystemMessage("system"),
			conversation.UserMessage("hello"),
		},
		ToolCount: 1,
	})

	if !got.Text || got.ImageInput || got.ToolCalling {
		t.Fatalf("InferRequirements() = %#v, want text-only requirements", got)
	}
}

func TestPassthroughPreservesOrderAndDropsEmptyRefs(t *testing.T) {
	plan, err := (Passthrough{}).Plan(
		[]string{"primary", " ", "backup"},
		Requirements{Text: true},
	)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if got, want := plan.EligibleRefs, []string{"primary", "backup"}; len(got) != len(want) ||
		got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("eligible refs = %#v, want %#v", got, want)
	}
}

func TestCapabilitiesMissingReasonUsesStableOrder(t *testing.T) {
	reason := (Capabilities{}).MissingReason(Requirements{
		Text:        true,
		ImageInput:  true,
		ToolCalling: true,
	})
	if reason != "missing capabilities [text, image_input, tool_calling]" {
		t.Fatalf("MissingReason() = %q", reason)
	}
}
