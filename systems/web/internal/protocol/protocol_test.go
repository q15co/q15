package protocol

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/q15co/q15/libs/chat-contract/chatpb"
)

func TestFrameGoldens(t *testing.T) {
	ts := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	msg := MessageID{Turn: 42, Ordinal: -1}
	var frames []Frame
	for _, value := range []struct {
		kind    string
		payload any
	}{
		{Hello, Cursor{Cursor: 41}}, {Sync, Cursor{Cursor: 41}}, {Send, SendRequest{ClientMsgID: "client-1", Text: "hello"}},
		{Abort, AbortRequest{Turn: 42}}, {Ack, AckRequest{Seq: 7}}, {Status, struct{}{}}, {Presence, PresenceRequest{FG: true}}, {Ping, struct{}{}},
		{Ready, ReadyPayload{HeadSeq: 42, Cursor: 41, DeviceID: "device-1"}}, {TurnStart, TurnStartPayload{Turn: 42, Msg: msg}},
		{Delta, ProgressPayload{Msg: msg, Seq: 7, Kind: "reasoning", Text: "thinking"}},
		{Snapshot, ProgressPayload{Msg: msg, Seq: 7, Kind: "text", Text: "answer"}},
		{Final, FinalPayload{Msg: msg, FullText: "answer", Status: "completed", ModelRef: "model-1"}},
		{Status, StatusPayload{Turn: 42, State: "queued", ClientMsgID: "client-1", Queued: true}},
		{Notice, NoticePayload{Code: "resync", Text: "refresh history"}}, {Pong, struct{}{}},
		{Error, ErrorPayload{Code: "resync_from_head", Ref: "request-1", HeadSeq: 42}},
	} {
		frames = append(frames, New(value.kind, "request-1", 7, ts, value.payload))
	}
	assertGolden(t, "testdata/frames.json", frames)
}

func assertGolden(t *testing.T, path string, value any) {
	t.Helper()
	golden, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var expected bytes.Buffer
	if err := json.Compact(&expected, golden); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, expected.Bytes()) {
		t.Fatalf("%s\ngot: %s\nwant: %s", path, got, expected.Bytes())
	}
}

func TestPagePreservesIdentityAndParts(t *testing.T) {
	page := FromPage(
		&chatpb.ListTurnsResponse{
			HeadSeq: 9007199254740993,
			Turns: []*chatpb.Turn{
				{
					Seq: 9007199254740992,
					Messages: []*chatpb.Message{
						{Ordinal: 0, Role: "assistant", Parts: []*chatpb.MessagePart{
							{
								Ordinal:  0,
								PartType: "reasoning",
								Text:     "thinking",
							}, {Ordinal: 1, PartType: "tool_call", ToolCall: &chatpb.ToolCall{Id: "call-1", Name: "bash", Arguments: `{"command":"pwd"}`}},
							{
								Ordinal:    2,
								PartType:   "tool_result",
								ToolCallId: "call-1",
								Content:    "/workspace",
								IsError:    true,
							},
							{
								Ordinal:   3,
								PartType:  "media",
								MediaKind: "image",
								MediaRef:  "media://sha256/hash",
							}, {Ordinal: 4, PartType: "text", Text: "answer", Disposition: "final"},
						}},
					},
				},
			},
		},
	)
	assertGolden(t, "testdata/parts.json", page)
}

func TestUnknownEventDoesNotInventAFrame(t *testing.T) {
	if _, ok := FromEvent(&chatpb.SessionEvent{}); ok {
		t.Fatal("unknown event rendered")
	}
}
