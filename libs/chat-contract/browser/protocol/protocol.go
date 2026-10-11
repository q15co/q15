// Package protocol defines the versioned browser contract, independent of gRPC.
package protocol

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/q15co/q15/libs/chat-contract/chatpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Version changes when message identity or part semantics change.
const Version = 3

// Frame types are shared with the browser's golden fixtures.
const (
	Hello     = "hello"
	Sync      = "sync"
	Send      = "msg.send"
	Abort     = "msg.abort"
	Ack       = "msg.ack"
	Status    = "msg.status"
	Presence  = "presence"
	Ping      = "ping"
	Ready     = "ready"
	TurnStart = "turn.start"
	Delta     = "delta"
	Snapshot  = "snapshot"
	Final     = "msg.final"
	Notice    = "notice"
	Pong      = "pong"
	Error     = "error"
	Key       = "key"
	History   = "history"
	MediaPut  = "media.put"
	MediaGet  = "media.get"
	MediaDone = "media.done"
)

// Frame is the common envelope. Int64 cursors are decimal strings so JavaScript
// cannot round transcript identities above Number.MAX_SAFE_INTEGER.
type Frame struct {
	V       int             `json:"v"`
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	TS      time.Time       `json:"ts"`
	Seq     int64           `json:"seq,string"`
	Payload json.RawMessage `json:"payload"`
}

// New wraps a typed payload. All callers supply JSON-compatible contract types.
func New(kind, id string, seq int64, ts time.Time, payload any) Frame {
	data, err := json.Marshal(payload)
	if err != nil {
		panic(fmt.Sprintf("browser contract payload: %v", err))
	}
	return Frame{V: Version, ID: id, Type: kind, TS: ts.UTC(), Seq: seq, Payload: data}
}

// Cursor requests durable replay, never event-index replay.
type Cursor struct {
	Cursor int64 `json:"cursor,string"`
}

// SendRequest identifies one optimistic browser send.
type SendRequest struct {
	ClientMsgID string       `json:"client_msg_id"`
	Text        string       `json:"text"`
	Parts       []Attachment `json:"parts,omitempty"`
}

// Attachment carries send-time display metadata; transcripts retain canonical refs.
type Attachment struct {
	PartType    string `json:"part_type"`
	MediaKind   string `json:"media_kind"`
	MediaRef    string `json:"media_ref"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
}

// MediaFile describes bytes following the bounded, length-prefixed JSON header.
type MediaFile struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Size        int    `json:"size"`
}

// MediaResult is encrypted with the requesting channel's content key.
type MediaResult struct {
	Parts []Attachment `json:"parts"`
}

// AbortRequest names the run rather than accepting a client-chosen session.
type AbortRequest struct {
	Turn int64 `json:"turn,string"`
}

// AckRequest acknowledges a session event index.
type AckRequest struct {
	Seq int64 `json:"seq,string"`
}

// PresenceRequest is ephemeral device presence; it has no durable state.
type PresenceRequest struct {
	FG bool `json:"fg"`
}

// ReadyPayload advertises an allocated head bound and the completed replay cursor.
type ReadyPayload struct {
	HeadSeq  int64  `json:"head_seq,string"`
	Cursor   int64  `json:"cursor,string"`
	DeviceID string `json:"device_id"`
}

// MessageID identifies a canonical message. Ordinal -1 is the ephemeral run
// draft, replaced by canonical message/part ordinals when history is fetched.
type MessageID struct {
	Turn    int64 `json:"turn,string"`
	Ordinal int32 `json:"ordinal"`
}

// ProgressPayload preserves text, reasoning, tool calls and tool results.
type ProgressPayload struct {
	Msg       MessageID `json:"msg"`
	Seq       int64     `json:"seq,string"`
	Kind      string    `json:"kind"`
	Text      string    `json:"text"`
	Call      *ToolCall `json:"call,omitempty"`
	IsError   bool      `json:"is_error,omitempty"`
	ModelRef  string    `json:"model_ref,omitempty"`
	LoopTurn  int32     `json:"loop_turn,omitempty"`
	Reasoning string    `json:"reasoning,omitempty"`
}

// TurnStartPayload binds an event stream to its durable turn.
type TurnStartPayload struct {
	Turn int64     `json:"turn,string"`
	Msg  MessageID `json:"msg"`
}

// FinalPayload is self-contained on completion, cancellation and failure.
type FinalPayload struct {
	Msg      MessageID `json:"msg"`
	FullText string    `json:"full_text"`
	Status   string    `json:"status"`
	ModelRef string    `json:"model_ref,omitempty"`
	Message  *Message  `json:"message,omitempty"`
}

// StatusPayload correlates acceptance to the optimistic send and reports state.
type StatusPayload struct {
	Turn        int64  `json:"turn,string"`
	State       string `json:"state"`
	ClientMsgID string `json:"client_msg_id,omitempty"`
	Queued      bool   `json:"queued"`
}

// NoticePayload carries progress notices and text-only proactive output.
type NoticePayload struct {
	Code string `json:"code"`
	Text string `json:"text"`
}

// ErrorPayload gives clients a stable code without exposing internal errors.
type ErrorPayload struct {
	Code    string `json:"code"`
	Ref     string `json:"ref"`
	HeadSeq int64  `json:"head_seq,string,omitempty"`
}

// Page is the stable GET /api/turns response, newest first.
type Page struct {
	Turns   []Turn `json:"turns"`
	HeadSeq int64  `json:"head_seq,string"`
	HasMore bool   `json:"has_more"`
}

// Turn is one complete immutable transcript record.
type Turn struct {
	Seq       int64     `json:"seq,string"`
	CreatedAt time.Time `json:"created_at"`
	Messages  []Message `json:"messages"`
}

// Message preserves the canonical role and identity.
type Message struct {
	Ordinal int32  `json:"ordinal"`
	Role    string `json:"role"`
	Parts   []Part `json:"parts"`
}

// Part preserves every chat-contract part, including non-text parts.
type Part struct {
	Ordinal     int32     `json:"ordinal"`
	PartType    string    `json:"part_type"`
	Text        string    `json:"text,omitempty"`
	Disposition string    `json:"disposition,omitempty"`
	MediaKind   string    `json:"media_kind,omitempty"`
	MediaRef    string    `json:"media_ref,omitempty"`
	ToolCall    *ToolCall `json:"tool_call,omitempty"`
	ToolCallID  string    `json:"tool_call_id,omitempty"`
	Content     string    `json:"content,omitempty"`
	IsError     bool      `json:"is_error,omitempty"`
}

// ToolCall keeps raw arguments rather than reparsing provider JSON.
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// FromPage converts protobuf into the browser contract without proto JSON rules.
func FromPage(page *chatpb.ListTurnsResponse) Page {
	out := Page{Turns: []Turn{}, HeadSeq: page.GetHeadSeq(), HasMore: page.GetHasMore()}
	for _, turn := range page.GetTurns() {
		t := Turn{
			Seq:       turn.GetSeq(),
			CreatedAt: timestamp(turn.GetCreatedAt()),
			Messages:  []Message{},
		}
		for _, message := range turn.GetMessages() {
			m := Message{Ordinal: message.GetOrdinal(), Role: message.GetRole(), Parts: []Part{}}
			for _, part := range message.GetParts() {
				m.Parts = append(
					m.Parts,
					Part{Ordinal: part.GetOrdinal(), PartType: part.GetPartType(),
						Text: part.GetText(), Disposition: part.GetDisposition(), MediaKind: part.GetMediaKind(),
						MediaRef: part.GetMediaRef(), ToolCall: fromCall(part.GetToolCall()),
						ToolCallID: part.GetToolCallId(), Content: part.GetContent(), IsError: part.GetIsError()},
				)
			}
			t.Messages = append(t.Messages, m)
		}
		out.Turns = append(out.Turns, t)
	}
	return out
}

func timestamp(ts *timestamppb.Timestamp) time.Time {
	if ts == nil {
		return time.Time{}
	}
	return ts.AsTime().UTC()
}

func fromCall(call *chatpb.ToolCall) *ToolCall {
	if call == nil {
		return nil
	}
	return &ToolCall{ID: call.GetId(), Name: call.GetName(), Arguments: call.GetArguments()}
}

// FromEvent translates every frozen bridge event without conflating its cursors.
func FromEvent(event *chatpb.SessionEvent) (Frame, bool) {
	seq, turn := event.GetEventIndex(), event.GetTurnSeq()
	id := fmt.Sprintf("event:%d", seq)
	ts := timestamp(event.GetOccurredAt())
	msg := MessageID{Turn: turn, Ordinal: -1}
	progress := ProgressPayload{Msg: msg, Seq: seq}
	switch value := event.GetEvent().(type) {
	case *chatpb.SessionEvent_SessionOpened:
		return New(Status, id, seq, ts, StatusPayload{State: "idle"}), true
	case *chatpb.SessionEvent_RunStarted:
		return New(TurnStart, id, seq, ts, TurnStartPayload{Turn: turn, Msg: msg}), true
	case *chatpb.SessionEvent_ModelTurnStarted:
		progress.Kind, progress.ModelRef, progress.LoopTurn = "model_start", value.ModelTurnStarted.GetModelRef(), value.ModelTurnStarted.GetLoopTurn()
		return New(Snapshot, id, seq, ts, progress), true
	case *chatpb.SessionEvent_ModelTurnDelta:
		progress.Kind, progress.Text = "text", value.ModelTurnDelta.GetDelta()
	case *chatpb.SessionEvent_ModelReasoningDelta:
		progress.Kind, progress.Text = "reasoning", value.ModelReasoningDelta.GetDelta()
	case *chatpb.SessionEvent_ToolStarted:
		progress.Kind, progress.Call = "tool_call", fromCall(value.ToolStarted.GetCall())
	case *chatpb.SessionEvent_ToolFinished:
		progress.Kind, progress.Call = "tool_result", fromCall(value.ToolFinished.GetCall())
		progress.Text, progress.IsError = value.ToolFinished.GetOutput(), value.ToolFinished.GetIsError()
	case *chatpb.SessionEvent_Snapshot:
		progress.Kind, progress.Text = "text", value.Snapshot.GetText()
		return New(Snapshot, id, seq, ts, progress), true
	case *chatpb.SessionEvent_RunFinished:
		return New(Final, id, seq, ts, FinalPayload{Msg: msg, FullText: value.RunFinished.GetFullText(),
			Status: runStatus(value.RunFinished.GetStatus()), ModelRef: value.RunFinished.GetModelRef()}), true
	case *chatpb.SessionEvent_RunFailed:
		return New(Final, id, seq, ts, FinalPayload{Msg: msg, FullText: value.RunFailed.GetFullText(),
			Status: runStatus(value.RunFailed.GetStatus())}), true
	case *chatpb.SessionEvent_Notice:
		return New(Notice, id, seq, ts, NoticePayload{Code: value.Notice.GetCode(), Text: value.Notice.GetText()}), true
	default:
		return Frame{}, false
	}
	return New(Delta, id, seq, ts, progress), true
}

func runStatus(value chatpb.RunStatus) string {
	switch value {
	case chatpb.RunStatus_RUN_STATUS_COMPLETED:
		return "completed"
	case chatpb.RunStatus_RUN_STATUS_ABORTED:
		return "aborted"
	default:
		return "failed"
	}
}
