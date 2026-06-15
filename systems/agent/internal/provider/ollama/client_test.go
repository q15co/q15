package ollama

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	ollamaapi "github.com/ollama/ollama/api"
	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	q15media "github.com/q15co/q15/systems/agent/internal/media"
)

func TestMapMessagesBuildsNativeOllamaMessagesWithReasoningToolsAndToolResults(
	t *testing.T,
) {
	messages, err := mapMessages([]conversation.Message{
		conversation.UserMessage("hello"),
		conversation.AssistantMessage(
			conversation.Reasoning("portable thinking", nil),
			conversation.Text("checking", ""),
			conversation.ToolCall("call-1", "read_file", `{"path":"README.md"}`),
		),
		conversation.ToolResultMessage("call-1", "file contents", false),
	}, nil)
	if err != nil {
		t.Fatalf("mapMessages() error = %v", err)
	}
	if len(messages) != 3 {
		t.Fatalf("messages len = %d, want 3", len(messages))
	}

	assistant := messages[1]
	if assistant.Role != "assistant" {
		t.Fatalf("assistant role = %q, want assistant", assistant.Role)
	}
	if assistant.Content != "checking" {
		t.Fatalf("assistant content = %q, want checking", assistant.Content)
	}
	if assistant.Thinking != "portable thinking" {
		t.Fatalf("assistant thinking = %q, want portable thinking", assistant.Thinking)
	}
	if len(assistant.ToolCalls) != 1 {
		t.Fatalf("tool calls len = %d, want 1", len(assistant.ToolCalls))
	}
	call := assistant.ToolCalls[0]
	if call.ID != "call-1" || call.Function.Name != "read_file" ||
		call.Function.Arguments.String() != `{"path":"README.md"}` {
		t.Fatalf("tool call = %#v", call)
	}

	tool := messages[2]
	if tool.Role != "tool" || tool.ToolCallID != "call-1" || tool.ToolName != "read_file" ||
		tool.Content != "file contents" {
		t.Fatalf("tool message = %#v", tool)
	}
}

func TestMapMessagesBuildsUserMessageWithImageBytes(t *testing.T) {
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
	if messages[0].Role != "user" || messages[0].Content != "what is this?" {
		t.Fatalf("user message = %#v", messages[0])
	}
	if len(messages[0].Images) != 1 {
		t.Fatalf("images len = %d, want 1", len(messages[0].Images))
	}
	if got := []byte(messages[0].Images[0]); !reflect.DeepEqual(got, rawImage) {
		t.Fatalf("image bytes = %v, want %v", got, rawImage)
	}
}

func TestMapMessagesSupportsInlineImageDataURL(t *testing.T) {
	rawImage := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(rawImage)

	messages, err := mapMessages([]conversation.Message{
		conversation.UserMessageParts(conversation.Image("", dataURL)),
	}, nil)
	if err != nil {
		t.Fatalf("mapMessages() error = %v", err)
	}
	if len(messages) != 1 || len(messages[0].Images) != 1 {
		t.Fatalf("messages = %#v, want one image", messages)
	}
	if got := []byte(messages[0].Images[0]); !reflect.DeepEqual(got, rawImage) {
		t.Fatalf("image bytes = %v, want %v", got, rawImage)
	}
}

func TestMapMessagesAddsFollowupsAndBootstrapForImagesAndSystemOnlyRequests(t *testing.T) {
	store, ref, _ := mustStoreTestImage(t)

	t.Run("assistant image", func(t *testing.T) {
		messages, err := mapMessages([]conversation.Message{
			conversation.AssistantMessage(
				conversation.Text("sent this", ""),
				conversation.Image(ref, ""),
			),
		}, store)
		if err != nil {
			t.Fatalf("mapMessages() error = %v", err)
		}
		if len(messages) != 2 {
			t.Fatalf("messages len = %d, want 2", len(messages))
		}
		if messages[1].Role != "user" ||
			!strings.Contains(messages[1].Content, assistantImageFollowupText) ||
			len(messages[1].Images) != 1 {
			t.Fatalf("followup message = %#v", messages[1])
		}
	})

	t.Run("tool image", func(t *testing.T) {
		messages, err := mapMessages([]conversation.Message{{
			Role: conversation.ToolRole,
			Parts: []conversation.Part{
				conversation.ToolResult("call-1", "captured screenshot", false),
				conversation.Image(ref, ""),
			},
		}}, store)
		if err != nil {
			t.Fatalf("mapMessages() error = %v", err)
		}
		if len(messages) != 2 {
			t.Fatalf("messages len = %d, want 2", len(messages))
		}
		if messages[1].Role != "user" ||
			!strings.Contains(messages[1].Content, toolImageFollowupText) ||
			len(messages[1].Images) != 1 {
			t.Fatalf("followup message = %#v", messages[1])
		}
	})

	t.Run("system only", func(t *testing.T) {
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
		if messages[2].Role != "user" || messages[2].Content != systemOnlyFollowupText {
			t.Fatalf("bootstrap message = %#v", messages[2])
		}
	})
}

func TestMapMessagesPrependsTemporalMetadataTag(t *testing.T) {
	location := time.FixedZone("UTC+2", 2*60*60)
	metadata := &conversation.UserTemporalMetadata{
		TimeLocal:            time.Date(2026, time.April, 12, 10, 11, 12, 0, location),
		SincePrevUserMessage: conversation.NewDuration(3*time.Minute + 42*time.Second),
	}

	messages, err := mapMessages([]conversation.Message{{
		Role:         conversation.UserRole,
		Parts:        []conversation.Part{conversation.Text("hello", "")},
		UserTemporal: metadata,
	}}, nil)
	if err != nil {
		t.Fatalf("mapMessages() error = %v", err)
	}
	if !strings.Contains(
		messages[0].Content,
		`message_meta day_of_week_local="Sunday" timestamp_local="20260412T101112+0200" since_prev_user_message="3m42s"/`,
	) {
		t.Fatalf("user message missing metadata prefix: %q", messages[0].Content)
	}
	if !strings.Contains(messages[0].Content, "\n\nhello") {
		t.Fatalf("user message missing original text: %q", messages[0].Content)
	}
}

func TestMapToolsBuildsOllamaToolDefinitions(t *testing.T) {
	tools, err := mapTools([]agent.ToolDefinition{{
		Name:        "web_search",
		Description: "Search the web",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Search query",
				},
				"mode": map[string]any{
					"type": "string",
					"enum": []string{"fast", "deep"},
				},
			},
			"required": []string{"query"},
		},
	}})
	if err != nil {
		t.Fatalf("mapTools() error = %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("tools len = %d, want 1", len(tools))
	}

	data, err := json.Marshal(tools[0])
	if err != nil {
		t.Fatalf("json.Marshal(tool) error = %v", err)
	}
	body := string(data)
	for _, want := range []string{
		`"type":"function"`,
		`"name":"web_search"`,
		`"description":"Search the web"`,
		`"query"`,
		`"required":["query"]`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("serialized tool missing %q: %s", want, body)
		}
	}
}

func TestParseAssistantMessageExtractsThinkingTextAndToolCalls(t *testing.T) {
	var args ollamaapi.ToolCallFunctionArguments
	if err := json.Unmarshal([]byte(`{"path":"README.md"}`), &args); err != nil {
		t.Fatalf("json.Unmarshal(args) error = %v", err)
	}

	got, err := parseAssistantMessage(ollamaapi.Message{
		Role:     "assistant",
		Thinking: "portable thinking",
		Content:  "reading",
		ToolCalls: []ollamaapi.ToolCall{{
			Function: ollamaapi.ToolCallFunction{
				Name:      "read_file",
				Arguments: args,
			},
		}},
	})
	if err != nil {
		t.Fatalf("parseAssistantMessage() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("messages len = %d, want 1", len(got))
	}
	parts := got[0].Parts
	if len(parts) != 3 {
		t.Fatalf("parts len = %d, want 3: %#v", len(parts), parts)
	}
	if parts[0].Type != conversation.ReasoningPartType || parts[0].Text != "portable thinking" {
		t.Fatalf("reasoning part = %#v", parts[0])
	}
	if string(parts[0].Replay[ollamaReplayKey]) != `{"thinking":"portable thinking"}` {
		t.Fatalf("reasoning replay = %s", parts[0].Replay[ollamaReplayKey])
	}
	if parts[1].Type != conversation.TextPartType || parts[1].Text != "reading" {
		t.Fatalf("text part = %#v", parts[1])
	}
	if parts[2].Type != conversation.ToolCallPartType || parts[2].ID != "ollama-call-1" ||
		parts[2].Name != "read_file" || parts[2].Arguments != `{"path":"README.md"}` {
		t.Fatalf("tool call part = %#v", parts[2])
	}
}

func TestBearerChatClientSendsAPIKeyAndParsesStreamingResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Fatalf("request path = %q, want /api/chat", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer ollama-key" {
			t.Fatalf("Authorization = %q, want bearer key", got)
		}
		var req ollamaapi.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Model != "gpt-oss:20b" {
			t.Fatalf("request model = %q, want gpt-oss:20b", req.Model)
		}
		if req.Stream == nil || !*req.Stream {
			t.Fatalf("request stream = %v, want true", req.Stream)
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write(
			[]byte(
				`{"message":{"role":"assistant","thinking":"considering","content":"hel"},"done":false}` + "\n",
			),
		)
		_, _ = w.Write(
			[]byte(
				`{"message":{"role":"assistant","content":"lo"},"done":true,"done_reason":"stop"}` + "\n",
			),
		)
	}))
	defer server.Close()

	base, err := normalizeBaseURL(server.URL)
	if err != nil {
		t.Fatalf("normalizeBaseURL() error = %v", err)
	}
	client := &Client{
		chat: newBearerChatClient(base, "ollama-key", server.Client()),
	}

	result, err := client.Complete(
		context.Background(),
		"gpt-oss:20b",
		[]conversation.Message{conversation.UserMessage("hello")},
		nil,
	)
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if result.FinishReason != "stop" {
		t.Fatalf("FinishReason = %q, want stop", result.FinishReason)
	}
	if len(result.Messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(result.Messages))
	}
	parts := result.Messages[0].Parts
	if len(parts) != 2 {
		t.Fatalf("parts len = %d, want 2: %#v", len(parts), parts)
	}
	if parts[0].Type != conversation.ReasoningPartType || parts[0].Text != "considering" {
		t.Fatalf("reasoning part = %#v", parts[0])
	}
	if parts[1].Type != conversation.TextPartType || parts[1].Text != "hello" {
		t.Fatalf("text part = %#v", parts[1])
	}
}

func TestNormalizeBaseURLDefaultsAndTrimsAPIPath(t *testing.T) {
	local, err := normalizeBaseURL("")
	if err != nil {
		t.Fatalf("normalizeBaseURL(empty) error = %v", err)
	}
	if got := local.String(); got != defaultBaseURL {
		t.Fatalf("default base URL = %q, want %q", got, defaultBaseURL)
	}

	cloud, err := normalizeBaseURL("https://ollama.com/api/")
	if err != nil {
		t.Fatalf("normalizeBaseURL(cloud) error = %v", err)
	}
	if got := cloud.String(); got != "https://ollama.com" {
		t.Fatalf("cloud base URL = %q, want https://ollama.com", got)
	}
}

func TestWithPromptProfileInsertsOllamaProfileAfterLeadingSystemPrefix(t *testing.T) {
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
	if got := conversation.TextValue(tuned[0]); got != "base" {
		t.Fatalf("tuned[0] = %q, want base", got)
	}
	if tuned[1].Role != conversation.SystemRole {
		t.Fatalf("tuned[1].Role = %q, want system", tuned[1].Role)
	}
	if got := conversation.TextValue(tuned[2]); got != "hello" {
		t.Fatalf("tuned[2] = %q, want hello", got)
	}
	profile := conversation.TextValue(tuned[1])
	for _, want := range []string{
		`provider="ollama"`,
		"Ollama responses are replayed through q15",
	} {
		if !strings.Contains(profile, want) {
			t.Fatalf("profile missing %q:\n%s", want, profile)
		}
	}
}

func TestOllamaLocalIntegration(t *testing.T) {
	model := strings.TrimSpace(os.Getenv("Q15_OLLAMA_INTEGRATION_MODEL"))
	if model == "" {
		t.Skip("set Q15_OLLAMA_INTEGRATION_MODEL to run live local/cloud Ollama integration")
	}

	client, err := NewClient(
		os.Getenv("Q15_OLLAMA_INTEGRATION_BASE_URL"),
		os.Getenv("OLLAMA_API_KEY"),
		nil,
	)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	result, err := client.Complete(
		context.Background(),
		model,
		[]conversation.Message{conversation.UserMessage("Reply with exactly: ok")},
		nil,
	)
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if len(result.Messages) == 0 ||
		strings.TrimSpace(conversation.FinalAnswer(result.Messages)) == "" {
		t.Fatalf("integration response is empty: %#v", result.Messages)
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
