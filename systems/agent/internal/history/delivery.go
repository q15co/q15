package history

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/history/historydb"
)

var errDeliveryExists = errors.New("delivery already recorded")

// RecordDeliveredAssistantEvent records an acknowledged delivery exactly once.
func (s *Store) RecordDeliveredAssistantEvent(
	ctx context.Context,
	event conversation.DeliveredAssistantEvent,
) error {
	event.Metadata = conversation.NormalizeExternalEventMetadata(event.Metadata)
	if strings.TrimSpace(event.Text) == "" || !event.Metadata.Valid() {
		return errors.New("delivered event requires text and valid delivery metadata")
	}
	if err := s.writeDelivery(ctx, event); err != nil {
		var constraint *pgconn.PgError
		if errors.Is(err, errDeliveryExists) ||
			(errors.As(err, &constraint) && constraint.Code == "23505") {
			return s.verifyDelivery(ctx, event)
		}
		return err
	}
	return nil
}

func (s *Store) writeDelivery(
	ctx context.Context,
	event conversation.DeliveredAssistantEvent,
) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := historydb.New(tx)
	params := historydb.FindDeliveryParams{
		AgentID: s.agentID,
		JobID:   event.Metadata.JobID,
		RunID:   event.Metadata.RunID,
		Channel: event.Metadata.Channel,
		ChatID:  event.Metadata.ChatID,
	}
	if _, err := q.FindDelivery(ctx, params); err == nil {
		return errDeliveryExists
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	id, err := q.ReserveTurn(ctx, s.agentID)
	if err != nil {
		return err
	}
	if err := writeMessages(ctx, q, id, []conversation.Message{conversation.DeliveredAssistantEventMessage(event)}); err != nil {
		return err
	}
	if _, err := q.CompleteTurn(ctx, historydb.CompleteTurnParams{ID: id, AgentID: s.agentID}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) verifyDelivery(
	ctx context.Context,
	event conversation.DeliveredAssistantEvent,
) error {
	id, err := historydb.New(s.pool).
		FindDelivery(ctx, historydb.FindDeliveryParams{AgentID: s.agentID, JobID: event.Metadata.JobID, RunID: event.Metadata.RunID, Channel: event.Metadata.Channel, ChatID: event.Metadata.ChatID})
	if err != nil {
		return err
	}
	turn, err := s.ReadTurn(ctx, id)
	if err != nil {
		return err
	}
	if len(turn.Messages) != 1 || conversation.TextValue(turn.Messages[0]) != event.Text {
		return fmt.Errorf("delivery already belongs to different content in turn %d", id)
	}
	return nil
}
