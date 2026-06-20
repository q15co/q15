package telegram

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mymmrac/telego"
	ta "github.com/mymmrac/telego/telegoapi"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	q15media "github.com/q15co/q15/systems/agent/internal/media"
)

var testTelegramJPEGBytes = []byte{
	0xff, 0xd8, 0xff, 0xdb, 0x00, 0x43, 0x00,
	0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07,
	0xff, 0xd9,
}

var testTelegramMP3Bytes = []byte("ID3\x03\x00\x00\x00\x00\x00\x00q15 mp3 fixture")

func TestIssue108InboundMediaStorageTypedPartsAndAdaptiveDowngrade(t *testing.T) {
	tests := []struct {
		name            string
		telegramResult  string
		downloadBytes   []byte
		message         telego.Message
		wantCaption     string
		wantFilename    string
		wantContentType string
		wantPartType    conversation.PartType
		wantHintKind    string
	}{
		{
			name: "png photo",
			telegramResult: `{"file_id":"png-high","file_unique_id":"u-png",` +
				`"file_path":"photos/issue-108.png"}`,
			downloadBytes: testTelegramPNGBytes,
			message: telego.Message{
				Caption: "describe png",
				Photo:   []telego.PhotoSize{{FileID: "png-low"}, {FileID: "png-high"}},
			},
			wantCaption:     "describe png",
			wantFilename:    "issue-108.png",
			wantContentType: "image/png",
			wantPartType:    conversation.ImagePartType,
			wantHintKind:    "[Media: image]",
		},
		{
			name: "jpeg photo",
			telegramResult: `{"file_id":"jpg-high","file_unique_id":"u-jpg",` +
				`"file_path":"photos/issue-108.jpg"}`,
			downloadBytes: testTelegramJPEGBytes,
			message: telego.Message{
				Caption: "describe jpeg",
				Photo:   []telego.PhotoSize{{FileID: "jpg-high"}},
			},
			wantCaption:     "describe jpeg",
			wantFilename:    "issue-108.jpg",
			wantContentType: "image/jpeg",
			wantPartType:    conversation.ImagePartType,
			wantHintKind:    "[Media: image]",
		},
		{
			name: "ogg opus voice",
			telegramResult: `{"file_id":"voice","file_unique_id":"u-voice",` +
				`"file_path":"voice/issue-108.ogg"}`,
			downloadBytes: testTelegramOGGOpusBytes,
			message: telego.Message{
				Caption: "transcribe ogg",
				Voice:   &telego.Voice{FileID: "voice"},
			},
			wantCaption:     "transcribe ogg",
			wantFilename:    "issue-108.ogg",
			wantContentType: "application/ogg",
			wantPartType:    conversation.AudioPartType,
			wantHintKind:    "[Media: audio]",
		},
		{
			name: "mp3 audio",
			telegramResult: `{"file_id":"mp3","file_unique_id":"u-mp3",` +
				`"file_path":"audio/issue-108.mp3"}`,
			downloadBytes: testTelegramMP3Bytes,
			message: telego.Message{
				Caption: "transcribe mp3",
				Audio:   &telego.Audio{FileID: "mp3"},
			},
			wantCaption:     "transcribe mp3",
			wantFilename:    "issue-108.mp3",
			wantContentType: "audio/mpeg",
			wantPartType:    conversation.AudioPartType,
			wantHintKind:    "[Media: audio]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, err := q15media.NewFileStore(filepath.Join(t.TempDir(), "media"))
			if err != nil {
				t.Fatalf("NewFileStore() error = %v", err)
			}

			caller := &mockAPICaller{
				responses: []*ta.Response{{Ok: true, Result: []byte(tt.telegramResult)}},
			}
			ch := newTestChannelWithCaller(t, caller)
			ch.mediaStore = store
			ch.downloadClient = &http.Client{
				Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: http.StatusOK,
						Status:     "200 OK",
						Body:       io.NopCloser(bytes.NewReader(tt.downloadBytes)),
						Header:     make(http.Header),
					}, nil
				}),
			}

			var got IncomingMessage
			ch.onMessage = func(msg IncomingMessage) {
				got = msg
			}
			msg := tt.message
			msg.MessageID = 108
			msg.Chat = telego.Chat{ID: 123}
			msg.From = &telego.User{ID: 7}

			if err := ch.handleMessage(context.Background(), &msg); err != nil {
				t.Fatalf("handleMessage() error = %v", err)
			}

			if !strings.Contains(got.Text, tt.wantCaption) {
				t.Fatalf("Text = %q, want caption %q", got.Text, tt.wantCaption)
			}
			if len(got.Attachments) != 1 || got.Attachments[0].Type != tt.wantPartType {
				t.Fatalf("Attachments = %#v, want one %s part", got.Attachments, tt.wantPartType)
			}

			mediaRef := extractMediaRef(t, got.Text)
			if got.Attachments[0].MediaRef != mediaRef {
				t.Fatalf(
					"attachment MediaRef = %q, want notice ref %q",
					got.Attachments[0].MediaRef,
					mediaRef,
				)
			}
			localPath, meta, err := store.Resolve(mediaRef)
			if err != nil {
				t.Fatalf("Resolve(%q) error = %v", mediaRef, err)
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
			if !strings.Contains(got.Text, localPath) {
				t.Fatalf("Text = %q, want stored path %q", got.Text, localPath)
			}

			adapted := q15media.AdaptMediaToCapabilities(
				[]conversation.Message{conversation.UserMessageParts(
					append(
						[]conversation.Part{conversation.Text(got.Text, "")},
						got.Attachments...)...,
				)},
				q15media.Support{},
				store,
			)
			if messageHasPartType(adapted[0], conversation.ImagePartType) ||
				messageHasPartType(adapted[0], conversation.AudioPartType) {
				t.Fatalf("adapted message retained inline media: %#v", adapted[0].Parts)
			}
			hint := messageTextContaining(adapted[0], tt.wantHintKind)
			if hint == "" {
				t.Fatalf(
					"adapted parts = %#v, want downgrade hint %q",
					adapted[0].Parts,
					tt.wantHintKind,
				)
			}
			for _, want := range []string{mediaRef, localPath} {
				if !strings.Contains(hint, want) {
					t.Fatalf("downgrade hint = %q, want %q", hint, want)
				}
			}
		})
	}
}

func messageHasPartType(message conversation.Message, partType conversation.PartType) bool {
	for _, part := range message.Parts {
		if part.Type == partType {
			return true
		}
	}
	return false
}

func messageTextContaining(message conversation.Message, needle string) string {
	for _, part := range message.Parts {
		if part.Type == conversation.TextPartType && strings.Contains(part.Text, needle) {
			return part.Text
		}
	}
	return ""
}
