package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mymmrac/telego"
	ta "github.com/mymmrac/telego/telegoapi"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	q15media "github.com/q15co/q15/systems/agent/internal/media"
)

type apiCall struct {
	url       string
	body      map[string]any
	files     map[string]int
	fileNames map[string]string
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
	files := map[string]int{}
	fileNames := map[string]string{}
	if len(bodyRaw) > 0 {
		if strings.HasPrefix(strings.ToLower(data.ContentType), "multipart/form-data") {
			var err error
			body, files, fileNames, err = parseMultipartAPICall(bodyRaw, data.ContentType)
			if err != nil {
				return nil, err
			}
		} else if err := json.Unmarshal(bodyRaw, &body); err != nil {
			return nil, err
		}
	}
	m.calls = append(m.calls, apiCall{
		url:       url,
		body:      body,
		files:     files,
		fileNames: fileNames,
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

func parseMultipartAPICall(
	bodyRaw []byte,
	contentType string,
) (map[string]any, map[string]int, map[string]string, error) {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return nil, nil, nil, err
	}
	if mediaType != "multipart/form-data" {
		return nil, nil, nil, nil
	}

	reader := multipart.NewReader(bytes.NewReader(bodyRaw), params["boundary"])
	body := make(map[string]any)
	files := make(map[string]int)
	fileNames := make(map[string]string)
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			return body, files, fileNames, nil
		}
		if err != nil {
			return nil, nil, nil, err
		}
		data, err := io.ReadAll(part)
		if err != nil {
			return nil, nil, nil, err
		}
		if name := part.FormName(); name != "" {
			if part.FileName() != "" {
				files[name] = len(data)
				fileNames[name] = part.FileName()
			} else {
				body[name] = string(data)
			}
		}
	}
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

type closingThenUpdateCaller struct {
	mu    sync.Mutex
	calls int
}

func (c *closingThenUpdateCaller) Call(
	_ context.Context,
	_ string,
	_ *ta.RequestData,
) (*ta.Response, error) {
	c.mu.Lock()
	c.calls++
	call := c.calls
	c.mu.Unlock()

	if call == 1 {
		return nil, context.Canceled
	}
	if call == 2 {
		return &ta.Response{
			Ok: true,
			Result: []byte(`[
				{
					"update_id": 42,
					"message": {
						"message_id": 7,
						"date": 1710000000,
						"chat": {"id": 123, "type": "private"},
						"from": {"id": 456, "is_bot": false, "first_name": "Ada"},
						"text": "after timeout"
					}
				}
			]`),
		}, nil
	}
	return &ta.Response{
		Ok:     true,
		Result: []byte(`[]`),
	}, nil
}

func (c *closingThenUpdateCaller) Calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func mustStoreMediaRef(
	t *testing.T,
	content []byte,
	meta q15media.Meta,
) (*q15media.FileStore, string) {
	t.Helper()

	store, err := q15media.NewFileStore(filepath.Join(t.TempDir(), "media"))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	sourcePath := filepath.Join(t.TempDir(), "source")
	if err := os.WriteFile(sourcePath, content, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	ref, err := store.Store(sourcePath, meta, "test-scope")
	if err != nil {
		t.Fatalf("Store() error = %v", err)
	}
	return store, ref
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

func TestStartRestartsWhenTelegoLongPollingStops(t *testing.T) {
	prevRestartDelay := telegramLongPollRestartDelay
	telegramLongPollRestartDelay = time.Millisecond
	t.Cleanup(func() {
		telegramLongPollRestartDelay = prevRestartDelay
	})

	caller := &closingThenUpdateCaller{}
	ch := newTestChannelWithCaller(t, caller)
	got := make(chan IncomingMessage, 1)
	ch.onMessage = func(msg IncomingMessage) {
		got <- msg
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := ch.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	select {
	case msg := <-got:
		cancel()
		if msg.ChatID != "123" {
			t.Fatalf("ChatID = %q, want 123", msg.ChatID)
		}
		if msg.UserID != "456" {
			t.Fatalf("UserID = %q, want 456", msg.UserID)
		}
		if msg.MessageID != "7" {
			t.Fatalf("MessageID = %q, want 7", msg.MessageID)
		}
		if msg.Text != "after timeout" {
			t.Fatalf("Text = %q, want after timeout", msg.Text)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for recovered Telegram update")
	}

	if gotCalls := caller.Calls(); gotCalls < 2 {
		t.Fatalf("calls = %d, want at least 2", gotCalls)
	}
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

func TestSendPhoto_UsesHTMLCaptionAndMultipart(t *testing.T) {
	store, ref := mustStoreMediaRef(t, testTelegramPNGBytes, q15media.Meta{
		ContentType: "image/png",
	})
	caller := &mockAPICaller{}
	ch := newTestChannelWithCaller(t, caller)
	ch.mediaStore = store

	err := ch.SendPhoto(t.Context(), "12345", ref, "**bold**")
	if err != nil {
		t.Fatalf("SendPhoto() error = %v", err)
	}

	if len(caller.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(caller.calls))
	}
	if !strings.HasSuffix(caller.calls[0].url, "/sendPhoto") {
		t.Fatalf("first URL = %q, want suffix /sendPhoto", caller.calls[0].url)
	}
	if got := caller.calls[0].body["caption"]; got != "<b>bold</b>" {
		t.Fatalf("caption = %#v, want %q", got, "<b>bold</b>")
	}
	if got := caller.calls[0].body["parse_mode"]; got != telego.ModeHTML {
		t.Fatalf("parse_mode = %#v, want %q", got, telego.ModeHTML)
	}
	if got := caller.calls[0].files["photo"]; got != len(testTelegramPNGBytes) {
		t.Fatalf("photo bytes = %d, want %d", got, len(testTelegramPNGBytes))
	}
}

func TestSendAudio_UsesHTMLCaptionAndMultipart(t *testing.T) {
	audioBytes := []byte("audio bytes")
	store, ref := mustStoreMediaRef(t, audioBytes, q15media.Meta{
		Filename:    "song.mp3",
		ContentType: "audio/mpeg",
	})
	caller := &mockAPICaller{}
	ch := newTestChannelWithCaller(t, caller)
	ch.mediaStore = store

	err := ch.SendAudio(t.Context(), "12345", ref, "**bold**")
	if err != nil {
		t.Fatalf("SendAudio() error = %v", err)
	}

	if len(caller.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(caller.calls))
	}
	if !strings.HasSuffix(caller.calls[0].url, "/sendAudio") {
		t.Fatalf("first URL = %q, want suffix /sendAudio", caller.calls[0].url)
	}
	if got := caller.calls[0].body["caption"]; got != "<b>bold</b>" {
		t.Fatalf("caption = %#v, want %q", got, "<b>bold</b>")
	}
	if got := caller.calls[0].body["parse_mode"]; got != telego.ModeHTML {
		t.Fatalf("parse_mode = %#v, want %q", got, telego.ModeHTML)
	}
	if got := caller.calls[0].files["audio"]; got != len(audioBytes) {
		t.Fatalf("audio bytes = %d, want %d", got, len(audioBytes))
	}
	if got := caller.calls[0].fileNames["audio"]; got != "song.mp3" {
		t.Fatalf("audio filename = %q, want song.mp3", got)
	}
}

func TestSendAudio_SendsOggAsTelegramVoice(t *testing.T) {
	voiceBytes := []byte("ogg opus bytes")
	store, ref := mustStoreMediaRef(t, voiceBytes, q15media.Meta{
		Filename:    "voice.ogg",
		ContentType: "audio/ogg",
	})
	caller := &mockAPICaller{}
	ch := newTestChannelWithCaller(t, caller)
	ch.mediaStore = store

	err := ch.SendAudio(t.Context(), "12345", ref, "**bold**")
	if err != nil {
		t.Fatalf("SendAudio() error = %v", err)
	}

	if len(caller.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(caller.calls))
	}
	if !strings.HasSuffix(caller.calls[0].url, "/sendVoice") {
		t.Fatalf("first URL = %q, want suffix /sendVoice", caller.calls[0].url)
	}
	if got := caller.calls[0].body["caption"]; got != "<b>bold</b>" {
		t.Fatalf("caption = %#v, want %q", got, "<b>bold</b>")
	}
	if got := caller.calls[0].files["voice"]; got != len(voiceBytes) {
		t.Fatalf("voice bytes = %d, want %d", got, len(voiceBytes))
	}
	if got := caller.calls[0].fileNames["voice"]; got != "voice.ogg" {
		t.Fatalf("voice filename = %q, want voice.ogg", got)
	}
}

func TestSendPhoto_FallbacksToPlainCaption(t *testing.T) {
	store, ref := mustStoreMediaRef(t, testTelegramPNGBytes, q15media.Meta{
		ContentType: "image/png",
	})
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
	ch.mediaStore = store

	err := ch.SendPhoto(t.Context(), "99", ref, "**bold**")
	if err != nil {
		t.Fatalf("SendPhoto() error = %v", err)
	}

	if len(caller.calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(caller.calls))
	}
	first := caller.calls[0]
	if got := first.body["caption"]; got != "<b>bold</b>" {
		t.Fatalf("first caption = %#v, want %q", got, "<b>bold</b>")
	}
	if got := first.body["parse_mode"]; got != telego.ModeHTML {
		t.Fatalf("first parse_mode = %#v, want %q", got, telego.ModeHTML)
	}
	second := caller.calls[1]
	if got := second.body["caption"]; got != "**bold**" {
		t.Fatalf("second caption = %#v, want %q", got, "**bold**")
	}
	if _, ok := second.body["parse_mode"]; ok {
		t.Fatalf("second parse_mode should be omitted, got %#v", second.body["parse_mode"])
	}
}

func TestSendPhoto_RejectsMissingMediaStore(t *testing.T) {
	ch := newTestChannelWithCaller(t, &mockAPICaller{})

	err := ch.SendPhoto(t.Context(), "123", "media://sha256/abc", "")
	if err == nil {
		t.Fatal("SendPhoto() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "media store") {
		t.Fatalf("error = %q, want media store failure", err.Error())
	}
}

func TestSendPhoto_RejectsNonImageRef(t *testing.T) {
	store, ref := mustStoreMediaRef(t, []byte("not an image"), q15media.Meta{
		ContentType: "text/plain",
	})
	ch := newTestChannelWithCaller(t, &mockAPICaller{})
	ch.mediaStore = store

	err := ch.SendPhoto(t.Context(), "123", ref, "")
	if err == nil {
		t.Fatal("SendPhoto() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "not an image") {
		t.Fatalf("error = %q, want non-image failure", err.Error())
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
		Date:      1_775_989_872,
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
	if want := time.Unix(1_775_989_872, 0).In(time.Local); !got.SentAt.Equal(want) {
		t.Fatalf("SentAt = %s, want %s", got.SentAt, want)
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

	if !strings.Contains(got.Text, "describe this") {
		t.Fatalf("Text = %q, want caption preserved", got.Text)
	}
	if len(got.Attachments) != 1 || !got.Attachments[0].IsMedia(conversation.MediaKindImage) {
		t.Fatalf(
			"Attachments = %#v, want 1 image part (capability-adaptive rendering)",
			got.Attachments,
		)
	}
	if !strings.Contains(got.Text, "Media-Ref: media://sha256/") {
		t.Fatalf("Text = %q, want media ref in notice", got.Text)
	}
	if !strings.Contains(got.Text, "Call load_image") {
		t.Fatalf("Text = %q, want load_image hint", got.Text)
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

	mediaRef := extractMediaRef(t, got.Text)
	localPath, meta, err := store.Resolve(mediaRef)
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
	if !strings.Contains(got.Text, localPath) {
		t.Fatalf("Text = %q, want notice to contain exec path %q", got.Text, localPath)
	}

	if err := store.ReleaseAll("telegram:123:42"); err != nil {
		t.Fatalf("ReleaseAll() error = %v", err)
	}
	if _, _, err := store.Resolve(mediaRef); err == nil {
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

	if !strings.Contains(got.Text, "Media-Ref: media://sha256/") {
		t.Fatalf("Text = %q, want media ref notice", got.Text)
	}
	if len(got.Attachments) != 1 || !got.Attachments[0].IsMedia(conversation.MediaKindImage) {
		t.Fatalf(
			"Attachments = %#v, want 1 image part (capability-adaptive rendering)",
			got.Attachments,
		)
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
				StatusCode: http.StatusNotFound,
				Status:     "404 Not Found",
				Body:       io.NopCloser(strings.NewReader("")),
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

// testTelegramOGGOpusBytes is a minimal OGG header so
// http.DetectContentType returns application/ogg.
var testTelegramOGGOpusBytes = []byte{
	'O', 'g', 'g', 'S', // OGG capture pattern
	0x00,                   // stream structure version (0 for sniff)
	0x02,                   // header type flag
	0x00, 0x00, 0x00, 0x00, // granule position
	0x00, 0x00, 0x00, 0x00,
	0x00, 0x00, 0x00, 0x00, // serial number
	0x00, 0x00, 0x00, 0x00, // page sequence
	0x00, 0x00, 0x00, 0x00, // checksum
	0x01, // segment count
	0x13, // segment size (OpusHead magic)
	'O', 'p', 'u', 's', 'H', 'e', 'a', 'd',
}

// extractMediaRef pulls the media:// ref from a generated attachment notice.
func extractMediaRef(t *testing.T, text string) string {
	t.Helper()
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Media-Ref: ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "Media-Ref: "))
		}
	}
	t.Fatalf("text %q does not contain a Media-Ref line", text)
	return ""
}

func TestHandleMessage_VoiceStoresMediaProducesTextNoticeOnly(t *testing.T) {
	store, err := q15media.NewFileStore(filepath.Join(t.TempDir(), "media"))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	caller := &mockAPICaller{
		responses: []*ta.Response{
			{
				Ok: true,
				Result: []byte(
					`{"file_id":"voice","file_unique_id":"u1","file_path":"voice/file_1.ogg"}`,
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
				Body:       io.NopCloser(bytes.NewReader(testTelegramOGGOpusBytes)),
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
		Caption:   "transcribe this",
		Voice:     &telego.Voice{FileID: "voice"},
		Chat:      telego.Chat{ID: 123},
		From:      &telego.User{ID: 7},
	})
	if err != nil {
		t.Fatalf("handleMessage() error = %v", err)
	}

	if len(got.Attachments) != 1 || !got.Attachments[0].IsMedia(conversation.MediaKindAudio) {
		t.Fatalf("Attachments = %#v, want 1 audio part", got.Attachments)
	}
	if !strings.Contains(got.Text, "transcribe this") {
		t.Fatalf("Text = %q, want caption preserved", got.Text)
	}
	if !strings.Contains(got.Text, "Media-Ref: media://sha256/") {
		t.Fatalf("Text = %q, want media ref", got.Text)
	}
	if !strings.Contains(got.Text, "usable from exec") {
		t.Fatalf("Text = %q, want exec hint", got.Text)
	}
	if !strings.Contains(got.Text, "voice attachment") {
		t.Fatalf("Text = %q, want voice kind in notice", got.Text)
	}

	mediaRef := extractMediaRef(t, got.Text)
	localPath, meta, err := store.Resolve(mediaRef)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if meta.Source != "telegram" {
		t.Fatalf("meta.Source = %q, want telegram", meta.Source)
	}
	if !strings.HasPrefix(meta.ContentType, "audio/") && meta.ContentType != "application/ogg" {
		t.Fatalf("meta.ContentType = %q, want audio/* or application/ogg", meta.ContentType)
	}
	if !strings.Contains(got.Text, localPath) {
		t.Fatalf("Text = %q, want notice to contain exec path %q", got.Text, localPath)
	}
	if err := store.ReleaseAll("telegram:123:42"); err != nil {
		t.Fatalf("ReleaseAll() error = %v", err)
	}
}

func TestHandleMessage_AudioStoresMediaProducesTextNoticeOnly(t *testing.T) {
	store, err := q15media.NewFileStore(filepath.Join(t.TempDir(), "media"))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	caller := &mockAPICaller{
		responses: []*ta.Response{
			{
				Ok: true,
				Result: []byte(
					`{"file_id":"audio","file_unique_id":"u1","file_path":"audio/file_1.mp3"}`,
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
				// ID3 tag header makes http.DetectContentType return audio/mpeg
				Body: io.NopCloser(
					bytes.NewReader([]byte("ID3\x03\x00\x00\x00\x00\x00\x00audio")),
				),
				Header: make(http.Header),
			}, nil
		}),
	}

	var got IncomingMessage
	ch.onMessage = func(msg IncomingMessage) {
		got = msg
	}

	err = ch.handleMessage(context.Background(), &telego.Message{
		MessageID: 42,
		Audio:     &telego.Audio{FileID: "audio"},
		Chat:      telego.Chat{ID: 123},
	})
	if err != nil {
		t.Fatalf("handleMessage() error = %v", err)
	}

	if len(got.Attachments) != 1 || !got.Attachments[0].IsMedia(conversation.MediaKindAudio) {
		t.Fatalf("Attachments = %#v, want 1 audio part", got.Attachments)
	}
	if !strings.Contains(got.Text, "Media-Ref: media://sha256/") {
		t.Fatalf("Text = %q, want media ref", got.Text)
	}
	if !strings.Contains(got.Text, "audio attachment") {
		t.Fatalf("Text = %q, want audio kind in notice", got.Text)
	}
	if !strings.Contains(got.Text, "usable from exec") {
		t.Fatalf("Text = %q, want exec hint", got.Text)
	}

	mediaRef := extractMediaRef(t, got.Text)
	if _, _, err := store.Resolve(mediaRef); err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if err := store.ReleaseAll("telegram:123:42"); err != nil {
		t.Fatalf("ReleaseAll() error = %v", err)
	}
}

func TestHandleMessage_VideoStoresMediaProducesTypedAttachment(t *testing.T) {
	store, err := q15media.NewFileStore(filepath.Join(t.TempDir(), "media"))
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}

	caller := &mockAPICaller{
		responses: []*ta.Response{
			{
				Ok: true,
				Result: []byte(
					`{"file_id":"video","file_unique_id":"u1","file_path":"video/file_1.mp4"}`,
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
				Body:       io.NopCloser(bytes.NewReader([]byte("video bytes here"))),
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
		Video:     &telego.Video{FileID: "video"},
		Chat:      telego.Chat{ID: 123},
	})
	if err != nil {
		t.Fatalf("handleMessage() error = %v", err)
	}

	if len(got.Attachments) != 1 ||
		!got.Attachments[0].IsMedia(conversation.MediaKindVideo) {
		t.Fatalf("Attachments = %#v, want 1 video media part", got.Attachments)
	}
	if !strings.Contains(got.Text, "Media-Ref: media://sha256/") {
		t.Fatalf("Text = %q, want media ref", got.Text)
	}
	if !strings.Contains(got.Text, "video attachment") {
		t.Fatalf("Text = %q, want video kind in notice", got.Text)
	}
	if !strings.Contains(got.Text, "usable from exec") {
		t.Fatalf("Text = %q, want exec hint", got.Text)
	}

	mediaRef := extractMediaRef(t, got.Text)
	if _, _, err := store.Resolve(mediaRef); err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if err := store.ReleaseAll("telegram:123:42"); err != nil {
		t.Fatalf("ReleaseAll() error = %v", err)
	}
}

var testTelegramMP4Bytes = []byte{0x00, 0x00, 0x00, 0x20, 0x66, 0x74, 0x79, 0x70,
	0x69, 0x73, 0x6f, 0x6d, 0x00, 0x00, 0x02, 0x00}

func TestSendVideo_UsesHTMLCaptionAndMultipart(t *testing.T) {
	store, ref := mustStoreMediaRef(t, testTelegramMP4Bytes, q15media.Meta{
		Filename:    "clip.mp4",
		ContentType: "video/mp4",
	})
	caller := &mockAPICaller{}
	ch := newTestChannelWithCaller(t, caller)
	ch.mediaStore = store

	if err := ch.SendVideo(t.Context(), "12345", ref, "**bold**"); err != nil {
		t.Fatalf("SendVideo() error = %v", err)
	}
	if len(caller.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(caller.calls))
	}
	if !strings.HasSuffix(caller.calls[0].url, "/sendVideo") {
		t.Fatalf("URL = %q, want suffix /sendVideo", caller.calls[0].url)
	}
	if got := caller.calls[0].body["caption"]; got != "<b>bold</b>" {
		t.Fatalf("caption = %#v, want %q", got, "<b>bold</b>")
	}
	if got := caller.calls[0].body["parse_mode"]; got != telego.ModeHTML {
		t.Fatalf("parse_mode = %#v, want %q", got, telego.ModeHTML)
	}
	if got := caller.calls[0].files["video"]; got != len(testTelegramMP4Bytes) {
		t.Fatalf("video bytes = %d, want %d", got, len(testTelegramMP4Bytes))
	}
	if got := caller.calls[0].fileNames["video"]; got != "clip.mp4" {
		t.Fatalf("video filename = %q, want clip.mp4", got)
	}
}

func TestSendDocument_UsesHTMLCaptionAndMultipart(t *testing.T) {
	store, ref := mustStoreMediaRef(t, []byte("pdf bytes"), q15media.Meta{
		Filename:    "report.pdf",
		ContentType: "application/pdf",
	})
	caller := &mockAPICaller{}
	ch := newTestChannelWithCaller(t, caller)
	ch.mediaStore = store

	if err := ch.SendDocument(t.Context(), "12345", ref, "**bold**"); err != nil {
		t.Fatalf("SendDocument() error = %v", err)
	}
	if !strings.HasSuffix(caller.calls[0].url, "/sendDocument") {
		t.Fatalf("URL = %q, want suffix /sendDocument", caller.calls[0].url)
	}
	if got := caller.calls[0].body["caption"]; got != "<b>bold</b>" {
		t.Fatalf("caption = %#v, want %q", got, "<b>bold</b>")
	}
	if got := caller.calls[0].files["document"]; got != len([]byte("pdf bytes")) {
		t.Fatalf("document bytes = %d", got)
	}
	if got := caller.calls[0].fileNames["document"]; got != "report.pdf" {
		t.Fatalf("document filename = %q, want report.pdf", got)
	}
}

func TestSendAnimation_UsesHTMLCaptionAndMultipart(t *testing.T) {
	store, ref := mustStoreMediaRef(t, []byte("gif bytes"), q15media.Meta{
		Filename:    "anim.gif",
		ContentType: "image/gif",
	})
	caller := &mockAPICaller{}
	ch := newTestChannelWithCaller(t, caller)
	ch.mediaStore = store

	if err := ch.SendAnimation(t.Context(), "12345", ref, "**bold**"); err != nil {
		t.Fatalf("SendAnimation() error = %v", err)
	}
	if !strings.HasSuffix(caller.calls[0].url, "/sendAnimation") {
		t.Fatalf("URL = %q, want suffix /sendAnimation", caller.calls[0].url)
	}
	if got := caller.calls[0].body["caption"]; got != "<b>bold</b>" {
		t.Fatalf("caption = %#v, want %q", got, "<b>bold</b>")
	}
	if got := caller.calls[0].files["animation"]; got != len([]byte("gif bytes")) {
		t.Fatalf("animation bytes = %d", got)
	}
}

func TestSendVideoNote_UsesMultipartWithoutCaption(t *testing.T) {
	store, ref := mustStoreMediaRef(t, testTelegramMP4Bytes, q15media.Meta{
		Filename:    "note.mp4",
		ContentType: "video/mp4",
	})
	caller := &mockAPICaller{}
	ch := newTestChannelWithCaller(t, caller)
	ch.mediaStore = store

	if err := ch.SendVideoNote(t.Context(), "12345", ref); err != nil {
		t.Fatalf("SendVideoNote() error = %v", err)
	}
	if !strings.HasSuffix(caller.calls[0].url, "/sendVideoNote") {
		t.Fatalf("URL = %q, want suffix /sendVideoNote", caller.calls[0].url)
	}
	if _, ok := caller.calls[0].body["caption"]; ok {
		t.Fatal("body should not contain caption key")
	}
	if _, ok := caller.calls[0].body["parse_mode"]; ok {
		t.Fatal("body should not contain parse_mode key")
	}
	if got := caller.calls[0].files["video_note"]; got != len(testTelegramMP4Bytes) {
		t.Fatalf("video_note bytes = %d, want %d", got, len(testTelegramMP4Bytes))
	}
}

func TestSendSticker_UsesMultipartWithoutCaption(t *testing.T) {
	store, ref := mustStoreMediaRef(t, []byte("webp bytes"), q15media.Meta{
		Filename:    "sticker.webp",
		ContentType: "image/webp",
	})
	caller := &mockAPICaller{}
	ch := newTestChannelWithCaller(t, caller)
	ch.mediaStore = store

	if err := ch.SendSticker(t.Context(), "12345", ref); err != nil {
		t.Fatalf("SendSticker() error = %v", err)
	}
	if !strings.HasSuffix(caller.calls[0].url, "/sendSticker") {
		t.Fatalf("URL = %q, want suffix /sendSticker", caller.calls[0].url)
	}
	if _, ok := caller.calls[0].body["caption"]; ok {
		t.Fatal("body should not contain caption key")
	}
	if _, ok := caller.calls[0].body["parse_mode"]; ok {
		t.Fatal("body should not contain parse_mode key")
	}
	if got := caller.calls[0].files["sticker"]; got != len([]byte("webp bytes")) {
		t.Fatalf("sticker bytes = %d, want %d", got, len([]byte("webp bytes")))
	}
}

func TestSendVideo_FallbacksToPlainCaption(t *testing.T) {
	store, ref := mustStoreMediaRef(t, testTelegramMP4Bytes, q15media.Meta{
		ContentType: "video/mp4",
	})
	caller := &mockAPICaller{
		responses: []*ta.Response{
			{
				Ok:    false,
				Error: &ta.Error{ErrorCode: 400, Description: "Bad Request: can't parse entities"},
			},
			{Ok: true, Result: []byte(`{}`)},
		},
	}
	ch := newTestChannelWithCaller(t, caller)
	ch.mediaStore = store

	if err := ch.SendVideo(t.Context(), "99", ref, "**bold**"); err != nil {
		t.Fatalf("SendVideo() error = %v", err)
	}
	if len(caller.calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(caller.calls))
	}
	if got := caller.calls[0].body["caption"]; got != "<b>bold</b>" {
		t.Fatalf("first caption = %#v, want %q", got, "<b>bold</b>")
	}
	if got := caller.calls[1].body["caption"]; got != "**bold**" {
		t.Fatalf("plain caption = %#v, want %q", got, "**bold**")
	}
	if _, ok := caller.calls[1].body["parse_mode"]; ok {
		t.Fatal("plain retry should not set parse_mode")
	}
}

func TestSendVideo_RejectsNonVideoContentType(t *testing.T) {
	store, ref := mustStoreMediaRef(t, testTelegramPNGBytes, q15media.Meta{
		ContentType: "image/png",
	})
	caller := &mockAPICaller{}
	ch := newTestChannelWithCaller(t, caller)
	ch.mediaStore = store

	if err := ch.SendVideo(t.Context(), "12345", ref, ""); err == nil {
		t.Fatal("SendVideo() error = nil, want rejection for non-video content type")
	}
	if len(caller.calls) != 0 {
		t.Fatalf("calls = %d, want 0 (should not send)", len(caller.calls))
	}
}

func TestSendVideo_AcceptsEmptyContentType(t *testing.T) {
	store, ref := mustStoreMediaRef(t, testTelegramMP4Bytes, q15media.Meta{})
	caller := &mockAPICaller{}
	ch := newTestChannelWithCaller(t, caller)
	ch.mediaStore = store

	if err := ch.SendVideo(t.Context(), "12345", ref, ""); err != nil {
		t.Fatalf("SendVideo() error = %v, want nil for unknown content type", err)
	}
}

func TestSendVideoNote_RejectsNonVideoContentType(t *testing.T) {
	store, ref := mustStoreMediaRef(t, testTelegramPNGBytes, q15media.Meta{
		ContentType: "image/png",
	})
	caller := &mockAPICaller{}
	ch := newTestChannelWithCaller(t, caller)
	ch.mediaStore = store

	if err := ch.SendVideoNote(t.Context(), "12345", ref); err == nil {
		t.Fatal("SendVideoNote() error = nil, want rejection for non-video content type")
	}
	if len(caller.calls) != 0 {
		t.Fatalf("calls = %d, want 0 (should not send)", len(caller.calls))
	}
}

func TestHandleMessage_StoresGenericMediaAsTypedAttachment(t *testing.T) {
	tests := []struct {
		name            string
		message         telego.Message
		wantKind        conversation.MediaKind
		wantKindLogName string
		wantFilename    string
		wantContentType string
	}{
		{
			name: "document",
			message: telego.Message{
				Document: &telego.Document{
					FileID:   "doc",
					FileName: "file.pdf",
					MimeType: "application/pdf",
				},
			},
			wantKind:        conversation.MediaKindDocument,
			wantKindLogName: "document",
			wantFilename:    "file.pdf",
			wantContentType: "application/pdf",
		},
		{
			name: "animation",
			message: telego.Message{
				Animation: &telego.Animation{
					FileID:   "anim",
					FileName: "anim.mp4",
					MimeType: "video/mp4",
				},
			},
			wantKind:        conversation.MediaKindAnimation,
			wantKindLogName: "animation",
			wantFilename:    "anim.mp4",
			wantContentType: "video/mp4",
		},
		{
			name: "video_note",
			message: telego.Message{
				VideoNote: &telego.VideoNote{FileID: "vnote"},
			},
			wantKind:        conversation.MediaKindVideoNote,
			wantKindLogName: "video note",
			wantFilename:    "file.bin",
			wantContentType: "text/plain; charset=utf-8",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, err := q15media.NewFileStore(filepath.Join(t.TempDir(), "media"))
			if err != nil {
				t.Fatalf("NewFileStore() error = %v", err)
			}
			caller := &mockAPICaller{
				responses: []*ta.Response{{Ok: true, Result: []byte(
					`{"file_id":"` + tt.name + `","file_unique_id":"u1","file_path":"` + tt.name + `/file.bin"}`,
				)}},
			}
			ch := newTestChannelWithCaller(t, caller)
			ch.mediaStore = store
			ch.downloadClient = &http.Client{
				Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(bytes.NewReader([]byte(tt.name + " bytes"))),
						Header:     make(http.Header),
					}, nil
				}),
			}

			var got IncomingMessage
			ch.onMessage = func(msg IncomingMessage) { got = msg }
			msg := tt.message
			msg.MessageID = 42
			msg.Chat = telego.Chat{ID: 123}

			if err := ch.handleMessage(context.Background(), &msg); err != nil {
				t.Fatalf("handleMessage() error = %v", err)
			}
			if len(got.Attachments) != 1 || !got.Attachments[0].IsMedia(tt.wantKind) {
				t.Fatalf("Attachments = %#v, want one %s media part", got.Attachments, tt.wantKind)
			}
			if !strings.Contains(got.Text, tt.wantKindLogName+" attachment") {
				t.Fatalf("Text = %q, want %q in notice", got.Text, tt.wantKindLogName)
			}
			if !strings.Contains(got.Text, "Media-Ref: media://sha256/") {
				t.Fatalf("Text = %q, want media ref", got.Text)
			}

			mediaRef := extractMediaRef(t, got.Text)
			_, meta, err := store.Resolve(mediaRef)
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			if meta.Source != "telegram" {
				t.Fatalf("meta.Source = %q, want telegram", meta.Source)
			}
			if meta.Filename != tt.wantFilename {
				t.Fatalf("meta.Filename = %q, want %q", meta.Filename, tt.wantFilename)
			}
			if meta.ContentType != tt.wantContentType {
				t.Fatalf("meta.ContentType = %q, want %q", meta.ContentType, tt.wantContentType)
			}
		})
	}
}

func TestHandleMessage_StoresStickerSubtypeInferredMetadata(t *testing.T) {
	tests := []struct {
		name            string
		sticker         *telego.Sticker
		wantContentType string
	}{
		{"static", &telego.Sticker{FileID: "st", IsAnimated: false, IsVideo: false}, "image/webp"},
		{
			"animated",
			&telego.Sticker{FileID: "sta", IsAnimated: true, IsVideo: false},
			"application/x-tgsticker",
		},
		{"video", &telego.Sticker{FileID: "stv", IsVideo: true}, "video/webm"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, err := q15media.NewFileStore(filepath.Join(t.TempDir(), "media"))
			if err != nil {
				t.Fatalf("NewFileStore() error = %v", err)
			}
			caller := &mockAPICaller{
				responses: []*ta.Response{{Ok: true, Result: []byte(
					`{"file_id":"` + tt.sticker.FileID + `","file_unique_id":"u1","file_path":"sticker/` + tt.sticker.FileID + `.bin"}`,
				)}},
			}
			ch := newTestChannelWithCaller(t, caller)
			ch.mediaStore = store
			ch.downloadClient = &http.Client{
				Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(bytes.NewReader([]byte("sticker bytes"))),
						Header:     make(http.Header),
					}, nil
				}),
			}

			var got IncomingMessage
			ch.onMessage = func(msg IncomingMessage) { got = msg }
			msg := telego.Message{MessageID: 42, Sticker: tt.sticker, Chat: telego.Chat{ID: 123}}

			if err := ch.handleMessage(context.Background(), &msg); err != nil {
				t.Fatalf("handleMessage() error = %v", err)
			}
			if len(got.Attachments) != 1 ||
				!got.Attachments[0].IsMedia(conversation.MediaKindSticker) {
				t.Fatalf("Attachments = %#v, want sticker media part", got.Attachments)
			}

			mediaRef := extractMediaRef(t, got.Text)
			_, meta, err := store.Resolve(mediaRef)
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			if meta.ContentType != tt.wantContentType {
				t.Fatalf("ContentType = %q, want %q", meta.ContentType, tt.wantContentType)
			}
			if meta.Source != "telegram" {
				t.Fatalf("Source = %q, want telegram", meta.Source)
			}
		})
	}
}
