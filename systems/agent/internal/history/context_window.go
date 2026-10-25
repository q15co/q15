package history

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/history/historydb"
)

func (s *Store) contextWindow(
	ctx context.Context,
	recent int,
) ([]conversation.Turn, map[int64]string, error) {
	tx, err := s.pool.BeginTx(
		ctx,
		pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
	)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := historydb.New(tx)
	headers, err := q.ListTurnHeaders(
		ctx,
		historydb.ListTurnHeadersParams{AgentID: s.agentID, PageLimit: int32(recent)},
	)
	if err != nil {
		return nil, nil, err
	}
	assistant, err := q.ReadLastAssistantHeader(ctx, s.agentID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, err
	}
	if assistant.ID != 0 && (len(headers) == 0 || assistant.ID < headers[len(headers)-1].ID) {
		headers = append(headers, historydb.ListTurnHeadersRow(assistant))
	}
	ids := make([]int64, len(headers))
	for n, header := range headers {
		ids[n] = header.ID
	}
	rows, err := q.ReadDigests(ctx, historydb.ReadDigestsParams{AgentID: s.agentID, TurnIds: ids})
	if err != nil {
		return nil, nil, err
	}
	digests := make(map[int64]string, len(rows))
	for _, row := range rows {
		digests[row.TurnID] = row.Body
	}
	turns, err := readTurns(ctx, q, headers)
	if err != nil {
		return nil, nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, nil, err
	}
	return turns, digests, nil
}
