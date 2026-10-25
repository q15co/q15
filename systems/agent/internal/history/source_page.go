package history

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/history/historydb"
)

const sourcePageCharacters = 4096
const recallResultBytes = 8192

type sourceCursor struct {
	TurnID int64  `json:"turn_id,omitempty"`
	PartID int64  `json:"part_id,omitempty"`
	Field  string `json:"field,omitempty"`
	Offset int    `json:"offset,omitempty"`
}

type sourcePage struct {
	TurnID          int64                               `json:"turn_id"`
	PartID          int64                               `json:"part_id"`
	MessagePosition int32                               `json:"message_position"`
	PartPosition    int32                               `json:"part_position"`
	Role            string                              `json:"role"`
	Kind            string                              `json:"kind"`
	Field           string                              `json:"field"`
	Offset          int                                 `json:"offset"`
	Encoding        string                              `json:"encoding"`
	Source          string                              `json:"source"`
	CreatedAt       time.Time                           `json:"created_at"`
	Disposition     string                              `json:"disposition,omitempty"`
	Delivery        *conversation.ExternalEventMetadata `json:"external_event,omitempty"`
	CallID          string                              `json:"call_id,omitempty"`
	ToolName        string                              `json:"tool_name,omitempty"`
	IsError         bool                                `json:"is_error,omitempty"`
	Clock           *conversation.UserTemporalMetadata  `json:"user_temporal,omitempty"`
	Next            *sourceCursor                       `json:"next"`
}

func (t recallTool) readSourcePage(ctx context.Context, args readArguments) (sourcePage, error) {
	row, err := historydb.New(t.indexer.store.pool).
		ReadSourcePage(ctx, historydb.ReadSourcePageParams{
			TurnID: args.TurnID, AgentID: t.indexer.store.agentID, PartID: args.PartID, Field: args.Field,
			PageOffset: int32(args.Offset), PageLimit: int32(args.Limit),
		})
	if errors.Is(err, pgx.ErrNoRows) {
		return sourcePage{}, fmt.Errorf(
			"history_read: no source part %d in completed turn %d for this agent; start with turn_id alone and copy the returned read arguments instead of guessing part IDs",
			args.PartID,
			args.TurnID,
		)
	}
	if err != nil {
		return sourcePage{}, err
	}
	if args.Field == "replay" && !row.HasReplay {
		return sourcePage{}, fmt.Errorf(
			"history_read: part %d has no provider replay; use field=body",
			row.PartID,
		)
	}
	content, encoding := row.Source, "utf8"
	more := utf8.RuneCountInString(content) > args.Limit
	if row.RawSource != nil {
		encoding = "base64"
		more = len(row.RawSource) > args.Limit
		content = base64.StdEncoding.EncodeToString(
			row.RawSource[:min(len(row.RawSource), args.Limit)],
		)
	} else if more {
		content = string([]rune(content)[:args.Limit])
	}
	var next *sourceCursor
	switch {
	case more:
		next = &sourceCursor{
			TurnID: args.TurnID,
			PartID: row.PartID,
			Field:  args.Field,
			Offset: args.Offset + args.Limit,
		}
	case args.Field == "body" && row.HasReplay:
		next = &sourceCursor{TurnID: args.TurnID, PartID: row.PartID, Field: "replay"}
	case row.NextPartID > 0:
		next = &sourceCursor{TurnID: args.TurnID, PartID: row.NextPartID, Field: "body"}
	}
	page := sourcePage{
		TurnID: args.TurnID, PartID: row.PartID, CreatedAt: row.CreatedAt.Time, Disposition: row.Disposition,
		MessagePosition: row.MessagePosition, PartPosition: row.PartPosition, Offset: args.Offset,
		Role: string(row.Role), Kind: string(row.Kind), Field: args.Field, Encoding: encoding,
		Source: content, CallID: row.CallID, ToolName: row.ToolName, IsError: row.IsError, Next: next,
	}
	if row.DeliveredAt.Valid {
		page.Delivery = &conversation.ExternalEventMetadata{
			Source: conversation.ExternalEventSourceScheduledJob, JobID: value(row.JobID), RunID: value(row.RunID),
			Channel: value(
				row.Channel,
			), ChatID: value(row.ChatID), DeliveredAt: row.DeliveredAt.Time,
		}
	}
	if row.Instant.Valid && row.UtcOffsetSeconds != nil {
		page.Clock = &conversation.UserTemporalMetadata{
			TimeLocal: row.Instant.Time.In(time.FixedZone("", int(*row.UtcOffsetSeconds))),
		}
		if row.SincePreviousNs != nil {
			page.Clock.SincePrevUserMessage = conversation.NewDuration(
				time.Duration(*row.SincePreviousNs),
			)
		}
	}
	return page, nil
}

func (page *sourcePage) shorten() bool {
	var count int
	if page.Encoding == "base64" {
		body, err := base64.StdEncoding.DecodeString(page.Source)
		if err != nil || len(body) <= 1 {
			return false
		}
		count = len(body) / 2
		page.Source = base64.StdEncoding.EncodeToString(body[:count])
	} else {
		body := []rune(page.Source)
		if len(body) <= 1 {
			return false
		}
		count = len(body) / 2
		page.Source = string(body[:count])
	}
	page.Next = &sourceCursor{
		TurnID: page.TurnID,
		PartID: page.PartID,
		Field:  page.Field,
		Offset: page.Offset + count,
	}
	return true
}
