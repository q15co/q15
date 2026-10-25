package history

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/history/historydb"
)

func writeMessages(
	ctx context.Context,
	q *historydb.Queries,
	turnID int64,
	messages []conversation.Message,
) error {
	for position, message := range messages {
		messageID, err := q.InsertMessage(
			ctx,
			historydb.InsertMessageParams{
				TurnID:   turnID,
				Position: int32(position),
				Role:     historydb.MemoryMessageRole(message.Role),
			},
		)
		if err != nil {
			return err
		}
		if err := writeMessageMetadata(ctx, q, messageID, message); err != nil {
			return err
		}
		for index, part := range message.Parts {
			partID, err := q.InsertPart(
				ctx,
				historydb.InsertPartParams{
					MessageID: messageID,
					Position:  int32(index),
					Kind:      historydb.MemoryPartKind(part.Type),
				},
			)
			if err != nil {
				return err
			}
			if err := writePart(ctx, q, partID, part); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeMessageMetadata(
	ctx context.Context,
	q *historydb.Queries,
	id int64,
	message conversation.Message,
) error {
	if temporal := message.UserTemporal; temporal != nil {
		_, offset := temporal.TimeLocal.Zone()
		var elapsed *int64
		if temporal.SincePrevUserMessage != nil {
			ns := int64(temporal.SincePrevUserMessage.Duration())
			elapsed = &ns
		}
		if err := q.InsertClock(ctx, historydb.InsertClockParams{MessageID: id, Instant: timestamp(temporal.TimeLocal), UtcOffsetSeconds: int32(offset), SincePreviousNs: elapsed}); err != nil {
			return err
		}
	}
	if origin := message.ExternalEvent; origin != nil {
		if origin.Source != conversation.ExternalEventSourceScheduledJob {
			return fmt.Errorf("unsupported delivered event source %q", origin.Source)
		}
		return q.InsertDelivery(
			ctx,
			historydb.InsertDeliveryParams{
				MessageID:   id,
				JobID:       origin.JobID,
				RunID:       origin.RunID,
				Channel:     origin.Channel,
				ChatID:      origin.ChatID,
				DeliveredAt: timestamp(origin.DeliveredAt),
			},
		)
	}
	return nil
}

func writePart(ctx context.Context, q *historydb.Queries, id int64, part conversation.Part) error {
	switch part.Type {
	case conversation.TextPartType:
		body, raw := splitText(part.Text)
		var disposition *string
		if part.Disposition != "" {
			value := string(part.Disposition)
			disposition = &value
		}
		return q.InsertText(
			ctx,
			historydb.InsertTextParams{
				PartID:      id,
				Body:        body,
				RawBody:     raw,
				Disposition: disposition,
			},
		)
	case conversation.ReasoningPartType:
		body, raw := splitText(part.Text)
		var replay, rawReplay []byte
		if len(part.Replay) > 0 {
			encoded, err := json.Marshal(part.Replay)
			if err != nil {
				return fmt.Errorf("encode reasoning replay: %w", err)
			}
			replay, rawReplay = splitJSON(encoded)
		}
		return q.InsertReasoning(
			ctx,
			historydb.InsertReasoningParams{
				PartID:    id,
				Body:      body,
				RawBody:   raw,
				Replay:    replay,
				RawReplay: rawReplay,
			},
		)
	case conversation.ToolCallPartType:
		arguments, raw := splitJSON([]byte(part.Arguments))
		return q.InsertToolCall(
			ctx,
			historydb.InsertToolCallParams{
				PartID:       id,
				CallID:       part.ID,
				ToolName:     part.Name,
				Arguments:    arguments,
				RawArguments: raw,
			},
		)
	case conversation.ToolResultPartType:
		body, raw := splitText(part.Content)
		return q.InsertToolResult(
			ctx,
			historydb.InsertToolResultParams{
				PartID:  id,
				CallID:  part.ToolCallID,
				Body:    body,
				RawBody: raw,
				IsError: part.IsError,
			},
		)
	case conversation.MediaPartType:
		return q.InsertMedia(
			ctx,
			historydb.InsertMediaParams{
				PartID:    id,
				MediaKind: historydb.MemoryMediaKind(part.MediaKind),
				MediaRef:  optionalString(part.MediaRef),
				DataUrl:   optionalString(part.DataURL),
			},
		)
	default:
		return fmt.Errorf("unsupported transcript part kind %q", part.Type)
	}
}

func splitText(body string) (*string, []byte) {
	if strings.IndexByte(body, 0) >= 0 || !utf8.ValidString(body) {
		return nil, []byte(body)
	}
	return &body, nil
}

func splitJSON(body []byte) ([]byte, []byte) {
	if !json.Valid(body) || bytes.Contains(body, []byte(`\u0000`)) || !utf8.Valid(body) {
		return nil, bytes.Clone(body)
	}
	return body, nil
}

func readTurns(
	ctx context.Context,
	q *historydb.Queries,
	headers []historydb.ListTurnHeadersRow,
) ([]conversation.Turn, error) {
	turns := make([]conversation.Turn, len(headers))
	ids := make([]int64, len(headers))
	turnIndex := make(map[int64]int, len(headers))
	for i, header := range headers {
		ids[i] = header.ID
		turnIndex[header.ID] = i
		turns[i] = conversation.Turn{Seq: header.ID, CreatedAt: header.CreatedAt.Time}
	}
	if len(ids) == 0 {
		return turns, nil
	}
	messages, err := q.ReadMessages(ctx, ids)
	if err != nil {
		return nil, err
	}
	type location struct{ turn, message int }
	messageIndex := make(map[int64]location, len(messages))
	for _, row := range messages {
		i := turnIndex[row.TurnID]
		message, err := readMessage(row)
		if err != nil {
			return nil, err
		}
		messageIndex[row.ID] = location{turn: i, message: len(turns[i].Messages)}
		turns[i].Messages = append(turns[i].Messages, message)
	}
	parts, err := q.ReadParts(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, row := range parts {
		part, err := readPart(row)
		if err != nil {
			return nil, err
		}
		position := messageIndex[row.MessageID]
		message := &turns[position.turn].Messages[position.message]
		message.Parts = append(message.Parts, part)
	}
	return turns, nil
}

func readMessage(row historydb.ReadMessagesRow) (conversation.Message, error) {
	message := conversation.Message{Role: conversation.Role(row.Role)}
	if row.Instant.Valid {
		if row.UtcOffsetSeconds == nil {
			return message, fmt.Errorf("user clock %d has no local offset", row.ID)
		}
		message.UserTemporal = &conversation.UserTemporalMetadata{
			TimeLocal: row.Instant.Time.In(time.FixedZone("", int(*row.UtcOffsetSeconds))),
		}
		if row.SincePreviousNs != nil {
			message.UserTemporal.SincePrevUserMessage = conversation.NewDuration(
				time.Duration(*row.SincePreviousNs),
			)
		}
	}
	if row.DeliveredAt.Valid {
		message.ExternalEvent = &conversation.ExternalEventMetadata{
			Source:      conversation.ExternalEventSourceScheduledJob,
			JobID:       value(row.JobID),
			RunID:       value(row.RunID),
			Channel:     value(row.Channel),
			ChatID:      value(row.ChatID),
			DeliveredAt: row.DeliveredAt.Time,
		}
	}
	return message, nil
}

func readPart(row historydb.ReadPartsRow) (conversation.Part, error) {
	part := conversation.Part{Type: conversation.PartType(row.Kind)}
	switch part.Type {
	case conversation.TextPartType:
		part.Text = joinText(row.TextBody, row.RawTextBody)
		part.Disposition = conversation.TextDisposition(value(row.Disposition))
	case conversation.ReasoningPartType:
		part.Text = joinText(row.ReasoningBody, row.RawReasoningBody)
		replay := row.Replay
		if row.RawReplay != nil {
			replay = row.RawReplay
		}
		if len(replay) > 0 {
			if err := json.Unmarshal(replay, &part.Replay); err != nil {
				return part, fmt.Errorf("decode reasoning replay for part %d: %w", row.ID, err)
			}
		}
	case conversation.ToolCallPartType:
		part.ID, part.Name = value(row.CallID), value(row.ToolName)
		part.Arguments = string(row.Arguments)
		if row.RawArguments != nil {
			part.Arguments = string(row.RawArguments)
		}
	case conversation.ToolResultPartType:
		part.ToolCallID, part.Content = value(
			row.ResultCallID,
		), joinText(
			row.ResultBody,
			row.RawResultBody,
		)
		part.IsError = row.IsError != nil && *row.IsError
	case conversation.MediaPartType:
		if row.MediaKind == nil {
			return part, fmt.Errorf("media part %d has no kind", row.ID)
		}
		part.MediaKind, part.MediaRef = conversation.MediaKind(*row.MediaKind), value(row.MediaRef)
		part.DataURL = value(row.DataUrl)
	}
	return part, nil
}

func joinText(body *string, raw []byte) string {
	if body != nil {
		return *body
	}
	return string(raw)
}

func value[T any](pointer *T) (result T) {
	if pointer != nil {
		return *pointer
	}
	return result
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
