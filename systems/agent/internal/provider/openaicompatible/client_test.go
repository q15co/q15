package openaicompatible

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	q15media "github.com/q15co/q15/systems/agent/internal/media"
)

func TestMapMessagesBuildsAssistantReplayWithReasoningAndTools(t *testing.T) {
	messages, err := mapMessages([]conversation.Message{
		conversation.AssistantMessage(
			conversation.Reasoning("portable summary", map[string]json.RawMessage{
				openAICompatibleReplayKey: json.RawMessage(`{"reasoning_opaque":"opaque-token"}`),
			}),
			conversation.Text("hello", ""),
			conversation.ToolCall("call-1", "shell", `{"cmd":"pwd"}`),
		),
	}, nil)
	if err != nil {
		t.Fatalf("mapMessages error = %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(messages))
	}

	data, err := json.Marshal(messages[0])
	if err != nil {
		t.Fatalf("json.Marshal(messages[0]) error = %v", err)
	}
	body := string(data)
	for _, want := range []string{
		`"role":"assistant"`,
		`"content":"hello"`,
		`"reasoning_content":"portable summary"`,
		`"reasoning_opaque":"opaque-token"`,
		`"tool_calls":[`,
		`"name":"shell"`,
		`"arguments":"{\"cmd\":\"pwd\"}"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("serialized replay missing %q: %s", want, body)
		}
	}
}

func TestMapMessagesUsesPortableReasoningTextAcrossProviderFallback(t *testing.T) {
	messages, err := mapMessages([]conversation.Message{
		conversation.AssistantMessage(
			conversation.Reasoning("portable summary", map[string]json.RawMessage{
				"openai_responses": json.RawMessage(
					`{"type":"reasoning","encrypted_content":"abc"}`,
				),
			}),
		),
	}, nil)
	if err != nil {
		t.Fatalf("mapMessages error = %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(messages))
	}

	data, err := json.Marshal(messages[0])
	if err != nil {
		t.Fatalf("json.Marshal(messages[0]) error = %v", err)
	}
	body := string(data)
	if !strings.Contains(body, `"reasoning_content":"portable summary"`) {
		t.Fatalf("serialized replay missing portable reasoning: %s", body)
	}
	if strings.Contains(body, "encrypted_content") {
		t.Fatalf("serialized replay should not include unmatched opaque replay: %s", body)
	}
}

func TestMapMessagesCoalescesContiguousAssistantMessagesForToolReplay(t *testing.T) {
	messages, err := mapMessages([]conversation.Message{
		conversation.AssistantMessage(
			conversation.Reasoning("portable summary", map[string]json.RawMessage{
				"openai_responses": json.RawMessage(
					`{"type":"reasoning","encrypted_content":"abc"}`,
				),
			}),
		),
		conversation.AssistantMessage(
			conversation.ToolCall("call-1", "shell", `{"cmd":"pwd"}`),
		),
	}, nil)
	if err != nil {
		t.Fatalf("mapMessages error = %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(messages))
	}

	data, err := json.Marshal(messages[0])
	if err != nil {
		t.Fatalf("json.Marshal(messages[0]) error = %v", err)
	}
	body := string(data)
	for _, want := range []string{
		`"role":"assistant"`,
		`"content":null`,
		`"reasoning_content":"portable summary"`,
		`"tool_calls":[`,
		`"name":"shell"`,
		`"arguments":"{\"cmd\":\"pwd\"}"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("serialized replay missing %q: %s", want, body)
		}
	}
}

func TestMapMessagesSynthesizesReasoningContentForOpaqueToolReplay(t *testing.T) {
	messages, err := mapMessages([]conversation.Message{
		conversation.AssistantMessage(
			conversation.Reasoning("", map[string]json.RawMessage{
				"openai_responses": json.RawMessage(
					`{"id":"rs_123","type":"reasoning","encrypted_content":"abc","summary":[]}`,
				),
			}),
		),
		conversation.AssistantMessage(
			conversation.ToolCall("call-1", "shell", `{"cmd":"pwd"}`),
		),
	}, nil)
	if err != nil {
		t.Fatalf("mapMessages error = %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(messages))
	}

	data, err := json.Marshal(messages[0])
	if err != nil {
		t.Fatalf("json.Marshal(messages[0]) error = %v", err)
	}
	body := string(data)
	for _, want := range []string{
		`"role":"assistant"`,
		`"content":null`,
		`"reasoning_content":"` + synthesizedReasoningContent + `"`,
		`"tool_calls":[`,
		`"name":"shell"`,
		`"arguments":"{\"cmd\":\"pwd\"}"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("serialized replay missing %q: %s", want, body)
		}
	}
}

func TestMapMessagesSynthesizesReasoningContentForToolReplayWithoutReasoningPart(t *testing.T) {
	messages, err := mapMessages([]conversation.Message{
		conversation.AssistantMessage(
			conversation.ToolCall("call-1", "shell", `{"cmd":"pwd"}`),
		),
	}, nil)
	if err != nil {
		t.Fatalf("mapMessages error = %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(messages))
	}

	data, err := json.Marshal(messages[0])
	if err != nil {
		t.Fatalf("json.Marshal(messages[0]) error = %v", err)
	}
	body := string(data)
	for _, want := range []string{
		`"role":"assistant"`,
		`"content":null`,
		`"reasoning_content":"` + synthesizedReasoningContent + `"`,
		`"tool_calls":[`,
		`"name":"shell"`,
		`"arguments":"{\"cmd\":\"pwd\"}"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("serialized replay missing %q: %s", want, body)
		}
	}
}

func TestParseAssistantMessageExtractsReasoningAndTools(t *testing.T) {
	raw := json.RawMessage(`{
		"role": "assistant",
		"content": "hello",
		"reasoning_content": "portable summary",
		"reasoning_opaque": "opaque-token"
	}`)

	var toolCalls []openai.ChatCompletionMessageToolCallUnion
	if err := json.Unmarshal([]byte(`[
		{
			"id": "call-1",
			"type": "function",
			"function": {
				"name": "shell",
				"arguments": "{\"cmd\":\"pwd\"}"
			}
		}
	]`), &toolCalls); err != nil {
		t.Fatalf("json.Unmarshal(toolCalls) error = %v", err)
	}

	got, err := parseAssistantMessage(raw, toolCalls)
	if err != nil {
		t.Fatalf("parseAssistantMessage() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("messages len = %d, want 1", len(got))
	}
	if got[0].Role != conversation.AssistantRole {
		t.Fatalf("role = %q, want assistant", got[0].Role)
	}
	if len(got[0].Parts) != 3 {
		t.Fatalf("parts len = %d, want 3", len(got[0].Parts))
	}
	if got[0].Parts[0].Type != conversation.ReasoningPartType ||
		got[0].Parts[0].Text != "portable summary" {
		t.Fatalf("reasoning part = %#v", got[0].Parts[0])
	}
	if string(
		got[0].Parts[0].Replay[openAICompatibleReplayKey],
	) != `{"reasoning_opaque":"opaque-token"}` {
		t.Fatalf("reasoning replay = %s", got[0].Parts[0].Replay[openAICompatibleReplayKey])
	}
	if got[0].Parts[1].Type != conversation.TextPartType || got[0].Parts[1].Text != "hello" {
		t.Fatalf("text part = %#v", got[0].Parts[1])
	}
	if got[0].Parts[2].Type != conversation.ToolCallPartType || got[0].Parts[2].Name != "shell" {
		t.Fatalf("tool call part = %#v", got[0].Parts[2])
	}
}

func TestMapMessagesBuildsMultipartUserMessageForImageInput(t *testing.T) {
	store, ref, rawImage := mustStoreTestImage(t)

	messages, err := mapMessages([]conversation.Message{
		conversation.UserMessageParts(
			conversation.Text("what is this?", ""),
			conversation.Image(ref, ""),
		),
	}, store)
	if err != nil {
		t.Fatalf("mapMessages() error = %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(messages))
	}

	data, err := json.Marshal(messages[0])
	if err != nil {
		t.Fatalf("json.Marshal(messages[0]) error = %v", err)
	}
	body := string(data)
	wantDataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(rawImage)
	for _, want := range []string{
		`"role":"user"`,
		`"type":"text"`,
		`"text":"what is this?"`,
		`"type":"image_url"`,
		wantDataURL,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("serialized user message missing %q: %s", want, body)
		}
	}
}

func TestMapMessagesPrependsExplicitUserTemporalMetadataTag(t *testing.T) {
	location := time.FixedZone("UTC+2", 2*60*60)
	metadata := &conversation.UserTemporalMetadata{
		TimeLocal:            time.Date(2026, time.April, 12, 10, 11, 12, 0, location),
		SincePrevUserMessage: conversation.NewDuration(3*time.Minute + 42*time.Second),
	}

	t.Run("text only", func(t *testing.T) {
		messages, err := mapMessages([]conversation.Message{{
			Role:         conversation.UserRole,
			Parts:        []conversation.Part{conversation.Text("hello", "")},
			UserTemporal: metadata,
		}}, nil)
		if err != nil {
			t.Fatalf("mapMessages() error = %v", err)
		}

		data, err := json.Marshal(messages[0])
		if err != nil {
			t.Fatalf("json.Marshal(messages[0]) error = %v", err)
		}
		body := string(data)
		for _, want := range []string{
			`message_meta day_of_week_local=\"Sunday\" timestamp_local=\"20260412T101112+0200\" since_prev_user_message=\"3m42s\"/`,
			`\n\nhello`,
		} {
			if !strings.Contains(body, want) {
				t.Fatalf("serialized user message missing metadata prefix %q: %s", want, body)
			}
		}
	})

	t.Run("text and image", func(t *testing.T) {
		store, ref, _ := mustStoreTestImage(t)
		messages, err := mapMessages([]conversation.Message{{
			Role: conversation.UserRole,
			Parts: []conversation.Part{
				conversation.Text("describe this", ""),
				conversation.Image(ref, ""),
			},
			UserTemporal: metadata,
		}}, store)
		if err != nil {
			t.Fatalf("mapMessages() error = %v", err)
		}

		data, err := json.Marshal(messages[0])
		if err != nil {
			t.Fatalf("json.Marshal(messages[0]) error = %v", err)
		}
		body := string(data)
		for _, want := range []string{
			`message_meta day_of_week_local=\"Sunday\" timestamp_local=\"20260412T101112+0200\" since_prev_user_message=\"3m42s\"/`,
			`"text":"describe this"`,
			`"type":"image_url"`,
		} {
			if !strings.Contains(body, want) {
				t.Fatalf("serialized user message missing %q: %s", want, body)
			}
		}
	})

	t.Run("image only", func(t *testing.T) {
		store, ref, _ := mustStoreTestImage(t)
		messages, err := mapMessages([]conversation.Message{{
			Role: conversation.UserRole,
			Parts: []conversation.Part{
				conversation.Image(ref, ""),
			},
			UserTemporal: metadata,
		}}, store)
		if err != nil {
			t.Fatalf("mapMessages() error = %v", err)
		}

		data, err := json.Marshal(messages[0])
		if err != nil {
			t.Fatalf("json.Marshal(messages[0]) error = %v", err)
		}
		body := string(data)
		for _, want := range []string{
			`message_meta day_of_week_local=\"Sunday\" timestamp_local=\"20260412T101112+0200\" since_prev_user_message=\"3m42s\"/`,
			`"type":"image_url"`,
		} {
			if !strings.Contains(body, want) {
				t.Fatalf("serialized user message missing %q: %s", want, body)
			}
		}
	})
}

func TestMapMessagesRejectsInvalidImageInput(t *testing.T) {
	store, err := q15media.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	_, err = mapMessages([]conversation.Message{
		conversation.UserMessageParts(conversation.Image("media://sha256/not-real", "")),
	}, store)
	if err == nil || !strings.Contains(err.Error(), "resolve image input") {
		t.Fatalf("mapMessages() error = %v, want image resolution failure", err)
	}
}

func TestMapMessagesAddsVisionFollowupForToolProducedImage(t *testing.T) {
	store, ref, _ := mustStoreTestImage(t)

	messages, err := mapMessages([]conversation.Message{
		{
			Role: conversation.ToolRole,
			Parts: []conversation.Part{
				conversation.ToolResult("call-1", "captured screenshot", false),
				conversation.Image(ref, ""),
			},
		},
	}, store)
	if err != nil {
		t.Fatalf("mapMessages() error = %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("messages len = %d, want 2", len(messages))
	}

	toolJSON, err := json.Marshal(messages[0])
	if err != nil {
		t.Fatalf("json.Marshal(tool message) error = %v", err)
	}
	if !strings.Contains(string(toolJSON), `"role":"tool"`) {
		t.Fatalf("tool message = %s, want tool role", string(toolJSON))
	}

	followupJSON, err := json.Marshal(messages[1])
	if err != nil {
		t.Fatalf("json.Marshal(followup message) error = %v", err)
	}
	body := string(followupJSON)
	for _, want := range []string{
		`"role":"user"`,
		toolImageFollowupText,
		`"type":"image_url"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("followup missing %q: %s", want, body)
		}
	}
}

func TestMapMessagesPreservesSeparateSystemMessagesAndAddsBootstrapUserForSystemOnlyRequests(
	t *testing.T,
) {
	messages, err := mapMessages([]conversation.Message{
		conversation.SystemMessage("cognition prompt"),
		conversation.SystemMessage("provider profile"),
	}, nil)
	if err != nil {
		t.Fatalf("mapMessages() error = %v", err)
	}
	if len(messages) != 3 {
		t.Fatalf("messages len = %d, want 3", len(messages))
	}

	for idx, wantContent := range []string{"cognition prompt", "provider profile"} {
		systemJSON, err := json.Marshal(messages[idx])
		if err != nil {
			t.Fatalf("json.Marshal(system message %d) error = %v", idx, err)
		}
		systemBody := string(systemJSON)
		for _, want := range []string{
			`"role":"system"`,
			`"content":"` + wantContent + `"`,
		} {
			if !strings.Contains(systemBody, want) {
				t.Fatalf("system message missing %q: %s", want, systemBody)
			}
		}
	}

	userJSON, err := json.Marshal(messages[2])
	if err != nil {
		t.Fatalf("json.Marshal(user message) error = %v", err)
	}
	userBody := string(userJSON)
	for _, want := range []string{
		`"role":"user"`,
		systemOnlyFollowupText,
	} {
		if !strings.Contains(userBody, want) {
			t.Fatalf("bootstrap message missing %q: %s", want, userBody)
		}
	}
}

func TestMapMessagesPreservesSystemMessagesAndNonSystemOrder(t *testing.T) {
	messages, err := mapMessages([]conversation.Message{
		conversation.SystemMessage("base"),
		conversation.UserMessage("hello"),
		conversation.SystemMessage("steering"),
	}, nil)
	if err != nil {
		t.Fatalf("mapMessages() error = %v", err)
	}
	if len(messages) != 3 {
		t.Fatalf("messages len = %d, want 3", len(messages))
	}

	firstJSON, err := json.Marshal(messages[0])
	if err != nil {
		t.Fatalf("json.Marshal(first message) error = %v", err)
	}
	firstBody := string(firstJSON)
	for _, want := range []string{
		`"role":"system"`,
		`"content":"base"`,
	} {
		if !strings.Contains(firstBody, want) {
			t.Fatalf("first message missing %q: %s", want, firstBody)
		}
	}

	secondJSON, err := json.Marshal(messages[1])
	if err != nil {
		t.Fatalf("json.Marshal(second message) error = %v", err)
	}
	secondBody := string(secondJSON)
	if !strings.Contains(secondBody, `"role":"user"`) ||
		!strings.Contains(secondBody, `"content":"hello"`) {
		t.Fatalf("second message = %s, want unchanged user message", secondBody)
	}
	if strings.Contains(secondBody, systemOnlyFollowupText) {
		t.Fatalf("second message unexpectedly included bootstrap text: %s", secondBody)
	}

	thirdJSON, err := json.Marshal(messages[2])
	if err != nil {
		t.Fatalf("json.Marshal(third message) error = %v", err)
	}
	thirdBody := string(thirdJSON)
	for _, want := range []string{
		`"role":"system"`,
		`"content":"steering"`,
	} {
		if !strings.Contains(thirdBody, want) {
			t.Fatalf("third message missing %q: %s", want, thirdBody)
		}
	}
}

func TestMapMessagesAddsBootstrapUserBeforeAssistantToolReplayWhenNoUserExists(t *testing.T) {
	messages, err := mapMessages([]conversation.Message{
		conversation.SystemMessage("cognition prompt"),
		conversation.AssistantMessage(
			conversation.ToolCall(
				"call-1",
				"read_file",
				`{"path":"/memory/working/WORKING_MEMORY.md"}`,
			),
		),
		conversation.ToolResultMessage("call-1", "file contents", false),
	}, nil)
	if err != nil {
		t.Fatalf("mapMessages() error = %v", err)
	}
	if len(messages) != 4 {
		t.Fatalf("messages len = %d, want 4", len(messages))
	}

	systemJSON, err := json.Marshal(messages[0])
	if err != nil {
		t.Fatalf("json.Marshal(system message) error = %v", err)
	}
	if body := string(systemJSON); !strings.Contains(body, `"role":"system"`) {
		t.Fatalf("system message = %s, want system role", body)
	}

	userJSON, err := json.Marshal(messages[1])
	if err != nil {
		t.Fatalf("json.Marshal(user message) error = %v", err)
	}
	userBody := string(userJSON)
	for _, want := range []string{
		`"role":"user"`,
		systemOnlyFollowupText,
	} {
		if !strings.Contains(userBody, want) {
			t.Fatalf("bootstrap message missing %q: %s", want, userBody)
		}
	}

	assistantJSON, err := json.Marshal(messages[2])
	if err != nil {
		t.Fatalf("json.Marshal(assistant message) error = %v", err)
	}
	assistantBody := string(assistantJSON)
	for _, want := range []string{
		`"role":"assistant"`,
		`"tool_calls":[`,
		`"arguments":"{\"path\":\"/memory/working/WORKING_MEMORY.md\"}"`,
	} {
		if !strings.Contains(assistantBody, want) {
			t.Fatalf("assistant replay missing %q: %s", want, assistantBody)
		}
	}

	toolJSON, err := json.Marshal(messages[3])
	if err != nil {
		t.Fatalf("json.Marshal(tool message) error = %v", err)
	}
	toolBody := string(toolJSON)
	for _, want := range []string{
		`"role":"tool"`,
		`"tool_call_id":"call-1"`,
	} {
		if !strings.Contains(toolBody, want) {
			t.Fatalf("tool replay missing %q: %s", want, toolBody)
		}
	}
}

func TestWithPromptProfileInsertsOpenAICompatibleProfileAfterLeadingSystemPrefix(t *testing.T) {
	base := []conversation.Message{
		conversation.SystemMessage("base"),
		conversation.UserMessage("hello"),
	}

	tuned := withPromptProfile(base)
	if len(tuned) != len(base)+1 {
		t.Fatalf("tuned len = %d, want %d", len(tuned), len(base)+1)
	}
	if got := conversation.TextValue(base[0]); got != "base" {
		t.Fatalf("base input mutated = %q, want %q", got, "base")
	}
	if got := conversation.TextValue(base[1]); got != "hello" {
		t.Fatalf("user input mutated = %q, want %q", got, "hello")
	}
	if got := conversation.TextValue(tuned[0]); got != "base" {
		t.Fatalf("tuned[0] = %q, want %q", got, "base")
	}
	if tuned[1].Role != conversation.SystemRole {
		t.Fatalf("tuned[1].Role = %q, want system", tuned[1].Role)
	}
	if got := conversation.TextValue(tuned[2]); got != "hello" {
		t.Fatalf("tuned[2] = %q, want %q", got, "hello")
	}
	lastText := conversation.TextValue(tuned[1])
	for _, want := range []string{
		`provider="openai-compatible"`,
		"does not preserve assistant commentary disposition metadata",
	} {
		if !strings.Contains(lastText, want) {
			t.Fatalf("profile missing %q:\n%s", want, lastText)
		}
	}
	if strings.Contains(lastText, `model="`) {
		t.Fatalf("profile should not depend on model name:\n%s", lastText)
	}
}

func TestMapMessagesDoesNotMutateInput(t *testing.T) {
	input := []conversation.Message{
		conversation.SystemMessage("base"),
		conversation.UserMessage("hello"),
	}
	want := conversation.CloneMessages(input)

	if _, err := mapMessages(input, nil); err != nil {
		t.Fatalf("mapMessages() error = %v", err)
	}
	if !reflect.DeepEqual(input, want) {
		t.Fatalf("input mutated:\n got %#v\nwant %#v", input, want)
	}
}

func mustStoreTestImage(t *testing.T) (*q15media.FileStore, string, []byte) {
	t.Helper()

	store, err := q15media.NewFileStore(filepath.Join(t.TempDir(), "media"))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	rawImage := []byte{
		0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n',
		0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R',
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
		0x89, 0x00, 0x00, 0x00, 0x0d, 'I', 'D', 'A', 'T',
		0x78, 0x9c, 0x63, 0xf8, 0xcf, 0xc0, 0x00, 0x00,
		0x03, 0x01, 0x01, 0x00, 0xc9, 0xfe, 0x92, 0xef,
		0x00, 0x00, 0x00, 0x00, 'I', 'E', 'N', 'D',
		0xae, 0x42, 0x60, 0x82,
	}
	imagePath := filepath.Join(t.TempDir(), "image.png")
	if err := os.WriteFile(imagePath, rawImage, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	ref, err := store.Store(imagePath, q15media.Meta{ContentType: "image/png"}, "test")
	if err != nil {
		t.Fatalf("Store() error = %v", err)
	}
	return store, ref, rawImage
}
