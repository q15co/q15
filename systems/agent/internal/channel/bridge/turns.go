package bridge

import (
	"github.com/q15co/q15/libs/chat-contract/chatpb"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/memory"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// turnsToProto translates one transcript page into its contract response.
func turnsToProto(page memory.TurnPage) *chatpb.ListTurnsResponse {
	response := &chatpb.ListTurnsResponse{
		Turns:   make([]*chatpb.Turn, 0, len(page.Turns)),
		HeadSeq: page.HeadSeq,
		HasMore: page.HasMore,
	}
	for _, turn := range page.Turns {
		response.Turns = append(response.Turns, turnToProto(turn))
	}
	return response
}

// turnToProto translates one canonical transcript turn.
func turnToProto(turn memory.Turn) *chatpb.Turn {
	out := &chatpb.Turn{
		Seq:      turn.Seq,
		Messages: make([]*chatpb.Message, 0, len(turn.Messages)),
	}
	if !turn.CreatedAt.IsZero() {
		out.CreatedAt = timestamppb.New(turn.CreatedAt)
	}
	for i, message := range turn.Messages {
		out.Messages = append(out.Messages, messageToProto(i, message))
	}
	return out
}

// messageToProto translates one canonical message. Ordinal is the message's
// index within its turn: the transcript has no per-message identifier, so the
// ordinals are the message identity.
func messageToProto(ordinal int, message conversation.Message) *chatpb.Message {
	out := &chatpb.Message{
		Ordinal: int32(ordinal),
		Role:    string(message.Role),
		Parts:   make([]*chatpb.MessagePart, 0, len(message.Parts)),
	}
	for i, part := range message.Parts {
		out.Parts = append(out.Parts, partToProto(i, part))
	}
	return out
}

// partToProto translates one canonical part. The contract is deliberately
// flat: only the fields the part's type selects are populated. Data URLs and
// reasoning replay metadata have no contract field and are dropped here; the
// media ref and the reasoning text stay the source of truth.
func partToProto(ordinal int, part conversation.Part) *chatpb.MessagePart {
	out := &chatpb.MessagePart{
		Ordinal:  int32(ordinal),
		PartType: string(part.Type),
	}
	switch part.Type {
	case conversation.TextPartType, conversation.ReasoningPartType:
		out.Text = part.Text
		out.Disposition = string(part.Disposition)
	case conversation.MediaPartType:
		out.MediaKind = string(part.MediaKind)
		out.MediaRef = part.MediaRef
	case conversation.ToolCallPartType:
		out.ToolCall = &chatpb.ToolCall{
			Id:        part.ID,
			Name:      part.Name,
			Arguments: part.Arguments,
		}
	case conversation.ToolResultPartType:
		out.ToolCallId = part.ToolCallID
		out.Content = part.Content
		out.IsError = part.IsError
	}
	return out
}
