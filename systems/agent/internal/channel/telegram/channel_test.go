package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mymmrac/telego"
	ta "github.com/mymmrac/telego/telegoapi"
	q15media "github.com/q15co/q15/systems/agent/internal/media"
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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
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

var testTelegramPNGBytes = []byte{
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

func TestHandleMessage_PhotoStoresMediaPreservesCaptionAndUsesExpectedScope(t *testing.T) {
	store, err := q15media.NewFileStore(filepath.Join(t.TempDir(), "media"))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	caller := &mockAPICaller{
		responses: []*ta.Response{
			{
				Ok: true,
				Result: []byte(
					`{"file_id":"high","file_unique_id":"u1","file_path":"photos/high.png"}`,
				),
			},
		},
	}
	ch := newTestChannelWithCaller(t, caller)
	ch.mediaStore = store
	ch.downloadClient = &http.Client{
		Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       io.NopCloser(bytes.NewReader(testTelegramPNGBytes)),
				Header:     make(http.Header),
			}, nil
		}),
	}

	var got IncomingMessage
	ch.onMessage = func(msg IncomingMessage) {
		got = msg
	}

	err = ch.handleMessage(context.Background(), &telego.Message{
		MessageID: 42,
		Caption:   "describe this",
		Photo: []telego.PhotoSize{
			{FileID: "low"},
			{FileID: "high"},
		},
		Chat: telego.Chat{ID: 123},
		From: &telego.User{ID: 7},
	})
	if err != nil {
		t.Fatalf("handleMessage() error = %v", err)
	}

	if got.Text != "describe this" {
		t.Fatalf("Text = %q, want %q", got.Text, "describe this")
	}
	if len(got.Media) != 1 {
		t.Fatalf("Media len = %d, want 1", len(got.Media))
	}
	if !strings.HasPrefix(got.Media[0], "media://sha256/") {
		t.Fatalf("Media[0] = %q, want media ref", got.Media[0])
	}
	if len(caller.calls) != 1 {
		t.Fatalf("API calls = %d, want 1", len(caller.calls))
	}
	if !strings.HasSuffix(caller.calls[0].url, "/getFile") {
		t.Fatalf("first URL = %q, want suffix /getFile", caller.calls[0].url)
	}
	if gotFileID := caller.calls[0].body["file_id"]; gotFileID != "high" {
		t.Fatalf("getFile file_id = %#v, want %q", gotFileID, "high")
	}

	localPath, meta, err := store.Resolve(got.Media[0])
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if meta.Source != "telegram" {
		t.Fatalf("meta.Source = %q, want telegram", meta.Source)
	}
	if meta.ContentType != "image/png" {
		t.Fatalf("meta.ContentType = %q, want image/png", meta.ContentType)
	}
	if meta.Filename != "high.png" {
		t.Fatalf("meta.Filename = %q, want high.png", meta.Filename)
	}
	if !strings.Contains(
		localPath,
		string(filepath.Separator)+"objects"+string(filepath.Separator),
	) {
		t.Fatalf("localPath = %q, want stored object path", localPath)
	}

	if err := store.ReleaseAll("telegram:123:42"); err != nil {
		t.Fatalf("ReleaseAll() error = %v", err)
	}
	if _, _, err := store.Resolve(got.Media[0]); err == nil {
		t.Fatal("Resolve() error = nil after releasing expected scope, want non-nil")
	}
}

func TestHandleMessage_PhotoOnlyProducesMediaOnlyMessage(t *testing.T) {
	store, err := q15media.NewFileStore(filepath.Join(t.TempDir(), "media"))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	caller := &mockAPICaller{
		responses: []*ta.Response{
			{
				Ok:     true,
				Result: []byte(`{"file_id":"photo","file_unique_id":"u1","file_path":"photo.jpg"}`),
			},
		},
	}
	ch := newTestChannelWithCaller(t, caller)
	ch.mediaStore = store
	ch.downloadClient = &http.Client{
		Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       io.NopCloser(bytes.NewReader(testTelegramPNGBytes)),
				Header:     make(http.Header),
			}, nil
		}),
	}

	var got IncomingMessage
	ch.onMessage = func(msg IncomingMessage) {
		got = msg
	}

	err = ch.handleMessage(context.Background(), &telego.Message{
		MessageID: 4,
		Photo:     []telego.PhotoSize{{FileID: "photo"}},
		Chat:      telego.Chat{ID: 321},
	})
	if err != nil {
		t.Fatalf("handleMessage() error = %v", err)
	}

	if got.Text != "" {
		t.Fatalf("Text = %q, want empty", got.Text)
	}
	if len(got.Media) != 1 {
		t.Fatalf("Media len = %d, want 1", len(got.Media))
	}
}

func TestHandleMessage_UnauthorizedUserSkipsMediaIngest(t *testing.T) {
	caller := &mockAPICaller{}
	ch := newTestChannelWithCaller(t, caller)
	if err := WithAllowedUserIDs([]int64{1})(ch); err != nil {
		t.Fatalf("WithAllowedUserIDs() error = %v", err)
	}

	var got bool
	downloadCalls := 0
	ch.onMessage = func(IncomingMessage) {
		got = true
	}
	ch.downloadClient = &http.Client{
		Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			downloadCalls++
			return nil, nil
		}),
	}

	err := ch.handleMessage(context.Background(), &telego.Message{
		MessageID: 1,
		Caption:   "nope",
		Photo:     []telego.PhotoSize{{FileID: "photo"}},
		Chat:      telego.Chat{ID: 123},
		From:      &telego.User{ID: 999},
	})
	if err != nil {
		t.Fatalf("handleMessage() error = %v", err)
	}

	if got {
		t.Fatal("onMessage called, want unauthorized message to be ignored")
	}
	if len(caller.calls) != 0 {
		t.Fatalf("API calls = %d, want 0", len(caller.calls))
	}
	if downloadCalls != 0 {
		t.Fatalf("downloadCalls = %d, want 0", downloadCalls)
	}
}

func TestHandleMessage_UnsupportedAttachmentsBecomeSyntheticTextOnlyMessages(t *testing.T) {
	tests := []struct {
		name    string
		kind    string
		message telego.Message
	}{
		{
			name: "animation",
			kind: "animation",
			message: telego.Message{
				Animation: &telego.Animation{FileID: "anim"},
				Caption:   "check this",
			},
		},
		{
			name: "audio",
			kind: "audio",
			message: telego.Message{
				Audio:   &telego.Audio{FileID: "audio"},
				Caption: "check this",
			},
		},
		{
			name: "document",
			kind: "document",
			message: telego.Message{
				Document: &telego.Document{FileID: "doc"},
				Caption:  "check this",
			},
		},
		{
			name: "sticker",
			kind: "sticker",
			message: telego.Message{
				Sticker: &telego.Sticker{FileID: "sticker"},
			},
		},
		{
			name: "video",
			kind: "video",
			message: telego.Message{
				Video:   &telego.Video{FileID: "video"},
				Caption: "check this",
			},
		},
		{
			name: "video note",
			kind: "video note",
			message: telego.Message{
				VideoNote: &telego.VideoNote{FileID: "video-note"},
				Caption:   "check this",
			},
		},
		{
			name: "voice",
			kind: "voice",
			message: telego.Message{
				Voice:   &telego.Voice{FileID: "voice"},
				Caption: "check this",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got IncomingMessage
			ch := &Channel{
				onMessage: func(msg IncomingMessage) {
					got = msg
				},
			}

			msg := tt.message
			msg.MessageID = 8
			msg.Chat = telego.Chat{ID: 123}

			err := ch.handleMessage(context.Background(), &msg)
			if err != nil {
				t.Fatalf("handleMessage() error = %v", err)
			}

			if len(got.Media) != 0 {
				t.Fatalf("Media = %#v, want empty", got.Media)
			}
			if !strings.Contains(got.Text, "Telegram currently supports photos/images only.") {
				t.Fatalf("Text = %q, want support warning", got.Text)
			}
			if !strings.Contains(got.Text, "must not pretend it saw the attachment") {
				t.Fatalf("Text = %q, want explicit agent warning", got.Text)
			}
			if !strings.Contains(got.Text, tt.kind) {
				t.Fatalf("Text = %q, want attachment kind %q", got.Text, tt.kind)
			}
			if strings.Contains(msg.Caption, "check this") &&
				!strings.Contains(got.Text, "Original user text: check this") {
				t.Fatalf("Text = %q, want original caption preserved", got.Text)
			}
		})
	}
}

func TestHandleMessage_PhotoIngestFailureBecomesSyntheticTextOnlyMessage(t *testing.T) {
	store, err := q15media.NewFileStore(filepath.Join(t.TempDir(), "media"))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	caller := &mockAPICaller{
		responses: []*ta.Response{
			{
				Ok:     true,
				Result: []byte(`{"file_id":"photo","file_unique_id":"u1","file_path":"photo.jpg"}`),
			},
		},
	}
	ch := newTestChannelWithCaller(t, caller)
	ch.mediaStore = store
	ch.downloadClient = &http.Client{
		Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Body:       io.NopCloser(strings.NewReader("not an image")),
				Header:     make(http.Header),
			}, nil
		}),
	}

	var got IncomingMessage
	ch.onMessage = func(msg IncomingMessage) {
		got = msg
	}

	err = ch.handleMessage(context.Background(), &telego.Message{
		MessageID: 9,
		Caption:   "please inspect",
		Photo:     []telego.PhotoSize{{FileID: "photo"}},
		Chat:      telego.Chat{ID: 123},
	})
	if err != nil {
		t.Fatalf("handleMessage() error = %v", err)
	}

	if len(got.Media) != 0 {
		t.Fatalf("Media = %#v, want empty", got.Media)
	}
	if !strings.Contains(got.Text, "could not load it") {
		t.Fatalf("Text = %q, want ingest failure notice", got.Text)
	}
	if !strings.Contains(got.Text, "Original user text: please inspect") {
		t.Fatalf("Text = %q, want original caption preserved", got.Text)
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
