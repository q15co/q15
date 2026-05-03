package cognition

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/q15co/q15/systems/agent/internal/conversation"
)

func TestRenderTranscriptArtifactTruncatesToolCallArguments(t *testing.T) {
	t.Parallel()

	payload := strings.Repeat("a", transcriptToolCallArgumentsLimit) + "b"

	got := renderTranscriptArtifact([]conversation.Message{
		conversation.AssistantMessage(conversation.ToolCall("call-1", "exec", payload)),
	})

	for _, want := range []string{
		`<part id="call-1" index="1" name="exec" type="tool_call">`,
		strings.Repeat("a", transcriptToolCallArgumentsLimit) +
			"\n[truncated: 1 bytes omitted]",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered transcript missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, payload) {
		t.Fatalf("rendered transcript contains untruncated tool arguments")
	}
}

func TestRenderTranscriptArtifactTruncatesToolResultContent(t *testing.T) {
	t.Parallel()

	payload := strings.Repeat("r", transcriptToolResultContentLimit) + "tail"

	got := renderTranscriptArtifact([]conversation.Message{
		conversation.ToolResultMessage("call-1", payload, true),
	})

	for _, want := range []string{
		`<part index="1" is_error="true" tool_call_id="call-1" type="tool_result">`,
		strings.Repeat("r", transcriptToolResultContentLimit) +
			"\n[truncated: 4 bytes omitted]",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered transcript missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, payload) {
		t.Fatalf("rendered transcript contains untruncated tool result")
	}
}

func TestRenderTranscriptArtifactKeepsToolPayloadsAtLimit(t *testing.T) {
	t.Parallel()

	arguments := strings.Repeat("a", transcriptToolCallArgumentsLimit)
	result := strings.Repeat("r", transcriptToolResultContentLimit)

	got := renderTranscriptArtifact([]conversation.Message{
		conversation.AssistantMessage(conversation.ToolCall("call-1", "exec", arguments)),
		conversation.ToolResultMessage("call-1", result, false),
	})

	for _, want := range []string{arguments, result} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered transcript missing untruncated payload")
		}
	}
	if strings.Contains(got, "[truncated:") {
		t.Fatalf("rendered transcript marked in-limit payload as truncated:\n%s", got)
	}
}

func TestRenderTranscriptArtifactDoesNotTruncateTextParts(t *testing.T) {
	t.Parallel()

	text := strings.Repeat("x", transcriptToolResultContentLimit+1)

	got := renderTranscriptArtifact([]conversation.Message{
		conversation.UserMessage(text),
	})

	if !strings.Contains(got, text) {
		t.Fatalf("rendered transcript missing full text part")
	}
	if strings.Contains(got, "[truncated:") {
		t.Fatalf("rendered transcript truncated non-tool text:\n%s", got)
	}
}

func TestRenderTranscriptArtifactPreservesEmptyToolPayloadFallbacks(t *testing.T) {
	t.Parallel()

	got := renderTranscriptArtifact([]conversation.Message{
		conversation.AssistantMessage(conversation.ToolCall("call-1", "exec", "")),
		conversation.ToolResultMessage("call-1", "", false),
	})

	for _, want := range []string{"{}", "(empty tool result)"} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered transcript missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "[truncated:") {
		t.Fatalf("rendered transcript truncated empty fallback payloads:\n%s", got)
	}
}

func TestTruncateTranscriptToolPayloadUsesValidUTF8AndCountsOmittedBytes(t *testing.T) {
	t.Parallel()

	got := truncateTranscriptToolPayload("界a界b", 2)

	if !utf8.ValidString(got) {
		t.Fatalf("truncated payload is invalid UTF-8: %q", got)
	}
	if want := "界a\n[truncated: 4 bytes omitted]"; got != want {
		t.Fatalf("truncated payload = %q, want %q", got, want)
	}
}
