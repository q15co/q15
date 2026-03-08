package telegram

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/mymmrac/telego"
	ta "github.com/mymmrac/telego/telegoapi"
)

type apiCall struct {
	url  string
	body map[string]any
}

type mockAPICaller struct {
	calls     []apiCall
	responses []*ta.Response
	callErr   error
}

func (m *mockAPICaller) Call(
	_ context.Context,
	url string,
	data *ta.RequestData,
) (*ta.Response, error) {
	if m.callErr != nil {
		return nil, m.callErr
	}

	bodyRaw := data.BodyRaw
	if len(bodyRaw) == 0 && data.BodyStream != nil {
		b, err := io.ReadAll(data.BodyStream)
		if err != nil {
			return nil, err
		}
		bodyRaw = b
	}

	body := map[string]any{}
	if len(bodyRaw) > 0 {
		if err := json.Unmarshal(bodyRaw, &body); err != nil {
			return nil, err
		}
	}
	m.calls = append(m.calls, apiCall{
		url:  url,
		body: body,
	})

	if len(m.responses) == 0 {
		return &ta.Response{
			Ok:     true,
			Result: []byte(`{}`),
		}, nil
	}

	resp := m.responses[0]
	m.responses = m.responses[1:]
	return resp, nil
}

func newTestChannelWithCaller(t *testing.T, caller ta.Caller) *Channel {
	t.Helper()

	const token = "123456789:abcdefghijklmnopqrstuvwxyzABCDE1234"
	bot, err := telego.NewBot(
		token,
		telego.WithAPICaller(caller),
		telego.WithDiscardLogger(),
	)
	if err != nil {
		t.Fatalf("telego.NewBot() error = %v", err)
	}

	return &Channel{bot: bot}
}

func TestSendText_UsesHTMLParseMode(t *testing.T) {
	caller := &mockAPICaller{
		responses: []*ta.Response{
			{
				Ok:     true,
				Result: []byte(`{}`),
			},
		},
	}
	ch := newTestChannelWithCaller(t, caller)

	err := ch.SendText(t.Context(), "12345", "**bold**")
	if err != nil {
		t.Fatalf("SendText() error = %v", err)
	}

	if len(caller.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(caller.calls))
	}
	if !strings.HasSuffix(caller.calls[0].url, "/sendMessage") {
		t.Fatalf("first URL = %q, want suffix /sendMessage", caller.calls[0].url)
	}
	if got := caller.calls[0].body["parse_mode"]; got != telego.ModeHTML {
		t.Fatalf("parse_mode = %#v, want %q", got, telego.ModeHTML)
	}
	if got := caller.calls[0].body["text"]; got != "<b>bold</b>" {
		t.Fatalf("text = %#v, want %q", got, "<b>bold</b>")
	}
}

func TestSendText_SplitsLongMessages(t *testing.T) {
	prevLimit := telegramTextChunkRunes
	telegramTextChunkRunes = 8
	defer func() {
		telegramTextChunkRunes = prevLimit
	}()

	caller := &mockAPICaller{}
	ch := newTestChannelWithCaller(t, caller)

	err := ch.SendText(t.Context(), "12345", "alpha beta")
	if err != nil {
		t.Fatalf("SendText() error = %v", err)
	}

	if len(caller.calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(caller.calls))
	}
	if got := caller.calls[0].body["text"]; got != "alpha" {
		t.Fatalf("first chunk text = %#v, want %q", got, "alpha")
	}
	if got := caller.calls[1].body["text"]; got != "beta" {
		t.Fatalf("second chunk text = %#v, want %q", got, "beta")
	}
}

func TestSendText_FallbacksToPlainText(t *testing.T) {
	caller := &mockAPICaller{
		responses: []*ta.Response{
			{
				Ok: false,
				Error: &ta.Error{
					ErrorCode:   400,
					Description: "Bad Request: can't parse entities",
				},
			},
			{
				Ok:     true,
				Result: []byte(`{}`),
			},
		},
	}
	ch := newTestChannelWithCaller(t, caller)
	original := "**bold** <raw>"

	err := ch.SendText(t.Context(), "99", original)
	if err != nil {
		t.Fatalf("SendText() error = %v", err)
	}

	if len(caller.calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(caller.calls))
	}

	first := caller.calls[0].body
	if got := first["parse_mode"]; got != telego.ModeHTML {
		t.Fatalf("first parse_mode = %#v, want %q", got, telego.ModeHTML)
	}
	if got := first["text"]; got != "<b>bold</b> &lt;raw&gt;" {
		t.Fatalf("first text = %#v, want %q", got, "<b>bold</b> &lt;raw&gt;")
	}

	second := caller.calls[1].body
	if _, ok := second["parse_mode"]; ok {
		t.Fatalf("second call parse_mode should be omitted, got %#v", second["parse_mode"])
	}
	if got := second["text"]; got != original {
		t.Fatalf("second text = %#v, want %q", got, original)
	}
}

func TestSendText_ReturnsErrorWhenBothAttemptsFail(t *testing.T) {
	caller := &mockAPICaller{
		responses: []*ta.Response{
			{
				Ok: false,
				Error: &ta.Error{
					ErrorCode:   400,
					Description: "Bad Request: can't parse entities",
				},
			},
			{
				Ok: false,
				Error: &ta.Error{
					ErrorCode:   500,
					Description: "Internal Server Error",
				},
			},
		},
	}
	ch := newTestChannelWithCaller(t, caller)

	err := ch.SendText(t.Context(), "99", "**bold**")
	if err == nil {
		t.Fatal("SendText() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "send telegram message:") {
		t.Fatalf("error = %q, want wrapped send telegram message error", err.Error())
	}
	if len(caller.calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(caller.calls))
	}
}

func TestEditText_FallbacksToPlainText(t *testing.T) {
	caller := &mockAPICaller{
		responses: []*ta.Response{
			{
				Ok: false,
				Error: &ta.Error{
					ErrorCode:   400,
					Description: "Bad Request: can't parse entities",
				},
			},
			{
				Ok:     true,
				Result: []byte(`{}`),
			},
		},
	}
	ch := newTestChannelWithCaller(t, caller)

	err := ch.EditText(t.Context(), "123", "456", "**bold**")
	if err != nil {
		t.Fatalf("EditText() error = %v", err)
	}

	if len(caller.calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(caller.calls))
	}
	if !strings.HasSuffix(caller.calls[0].url, "/editMessageText") {
		t.Fatalf("first URL = %q, want suffix /editMessageText", caller.calls[0].url)
	}
	if got := caller.calls[1].body["text"]; got != "**bold**" {
		t.Fatalf("plain fallback text = %#v, want %q", got, "**bold**")
	}
}

func TestStartTyping_SendsImmediateChatAction(t *testing.T) {
	prevInterval := telegramTypingKeepaliveInterval
	telegramTypingKeepaliveInterval = 10 * time.Millisecond
	defer func() {
		telegramTypingKeepaliveInterval = prevInterval
	}()

	caller := &mockAPICaller{}
	ch := newTestChannelWithCaller(t, caller)

	stop, err := ch.StartTyping(t.Context(), "123")
	if err != nil {
		t.Fatalf("StartTyping() error = %v", err)
	}
	defer stop()

	time.Sleep(15 * time.Millisecond)

	if len(caller.calls) == 0 {
		t.Fatal("expected at least one typing call")
	}
	if !strings.HasSuffix(caller.calls[0].url, "/sendChatAction") {
		t.Fatalf("first URL = %q, want suffix /sendChatAction", caller.calls[0].url)
	}
	if got := caller.calls[0].body["action"]; got != telego.ChatActionTyping {
		t.Fatalf("action = %#v, want %q", got, telego.ChatActionTyping)
	}
}

func TestSetReactionAndClearReaction(t *testing.T) {
	caller := &mockAPICaller{}
	ch := newTestChannelWithCaller(t, caller)

	if err := ch.SetReaction(t.Context(), "123", "456", "👀"); err != nil {
		t.Fatalf("SetReaction() error = %v", err)
	}
	if err := ch.ClearReaction(t.Context(), "123", "456"); err != nil {
		t.Fatalf("ClearReaction() error = %v", err)
	}

	if len(caller.calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(caller.calls))
	}
	if !strings.HasSuffix(caller.calls[0].url, "/setMessageReaction") {
		t.Fatalf("first URL = %q, want suffix /setMessageReaction", caller.calls[0].url)
	}

	reaction, ok := caller.calls[0].body["reaction"].([]any)
	if !ok || len(reaction) != 1 {
		t.Fatalf("reaction body = %#v, want one entry", caller.calls[0].body["reaction"])
	}
	if _, ok := caller.calls[1].body["reaction"]; ok {
		t.Fatalf(
			"clear reaction should omit reaction body, got %#v",
			caller.calls[1].body["reaction"],
		)
	}
}

func TestHandleMessage_IncludesMessageID(t *testing.T) {
	var got IncomingMessage
	ch := &Channel{
		onMessage: func(msg IncomingMessage) {
			got = msg
		},
	}

	err := ch.handleMessage(context.Background(), &telego.Message{
		MessageID: 42,
		Text:      "hello",
		Chat: telego.Chat{
			ID: 123,
		},
		From: &telego.User{
			ID: 7,
		},
	})
	if err != nil {
		t.Fatalf("handleMessage() error = %v", err)
	}

	if got.MessageID != "42" {
		t.Fatalf("MessageID = %q, want %q", got.MessageID, "42")
	}
	if got.ChatID != "123" {
		t.Fatalf("ChatID = %q, want %q", got.ChatID, "123")
	}
	if got.UserID != "7" {
		t.Fatalf("UserID = %q, want %q", got.UserID, "7")
	}
}

func TestSendText_ValidationErrors(t *testing.T) {
	ch := &Channel{}

	tests := []struct {
		name   string
		chatID string
		text   string
		want   string
	}{
		{
			name:   "missing chat id",
			chatID: "",
			text:   "hello",
			want:   "chat id is required",
		},
		{
			name:   "invalid chat id",
			chatID: "abc",
			text:   "hello",
			want:   `invalid chat id "abc"`,
		},
		{
			name:   "missing text",
			chatID: "123",
			text:   "   ",
			want:   "text is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ch.SendText(t.Context(), tt.chatID, tt.text)
			if err == nil {
				t.Fatal("SendText() error = nil, want non-nil")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tt.want)
			}
		})
	}
}
