// Package history owns the relational transcript and its derived search data.
package history

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // Goose uses pgx through database/sql.
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/history/historydb"
	"github.com/q15co/q15/systems/agent/internal/turnreply"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Store persists one agent's transcript using generated database identities.
type Store struct {
	pool    *pgxpool.Pool
	agentID int64
}

var _ agent.ConversationStore = (*Store)(nil)

// Open applies the owned schema migrations and opens the configured agent's history.
func Open(ctx context.Context, dsn, name string) (*Store, error) {
	if strings.TrimSpace(name) == "" || strings.TrimSpace(dsn) == "" {
		return nil, errors.New("postgres connection settings and agent name are required")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("invalid Postgres connection settings")
	}
	if err := migrate(ctx, dsn); err != nil {
		return nil, fmt.Errorf("migrate conversation history: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open conversation history: %w", err)
	}
	agentID, err := historydb.New(pool).EnsureAgent(ctx, strings.TrimSpace(name))
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("initialize history agent: %w", err)
	}
	return &Store{pool: pool, agentID: agentID}, nil
}

func migrate(ctx context.Context, dsn string) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return errors.New("open Postgres migration connection")
	}
	defer db.Close()
	files, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		return err
	}
	locker, err := lock.NewPostgresSessionLocker(lock.WithLockID(0x713135396d656d))
	if err != nil {
		return err
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, files,
		goose.WithTableName("public.q15_memory_schema_version"), goose.WithSessionLocker(locker))
	if err != nil {
		return err
	}
	_, err = provider.Up(ctx)
	return err
}

// Close releases the store's database connections.
func (s *Store) Close() { s.pool.Close() }

// ReserveTurnSeq durably reserves a generated identity before a run starts.
func (s *Store) ReserveTurnSeq(ctx context.Context) (int64, error) {
	id, err := historydb.New(s.pool).ReserveTurn(ctx, s.agentID)
	if err != nil {
		return 0, fmt.Errorf("reserve transcript turn: %w", err)
	}
	return id, nil
}

// AppendTurnAtSeq atomically completes a previously reserved turn.
func (s *Store) AppendTurnAtSeq(ctx context.Context, id int64, input []conversation.Message) error {
	messages := turnreply.NewExtractor(nil).Canonicalize(conversation.NormalizeMessages(input))
	for i := range messages {
		messages[i].ExternalEvent = nil
	}
	if len(messages) == 0 {
		return errors.New("a completed transcript turn requires messages")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := historydb.New(tx)
	turn, err := q.LockTurn(ctx, historydb.LockTurnParams{ID: id, AgentID: s.agentID})
	if err != nil {
		return fmt.Errorf("load reserved transcript turn %d: %w", id, err)
	}
	if turn.CompletedAt.Valid {
		return fmt.Errorf("transcript turn %d is already complete", id)
	}
	if err := writeMessages(ctx, q, id, messages); err != nil {
		return fmt.Errorf("persist transcript turn %d: %w", id, err)
	}
	if _, err := q.CompleteTurn(ctx, historydb.CompleteTurnParams{ID: id, AgentID: s.agentID}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// LoadHead reports the latest reserved identity, including an unfinished run.
func (s *Store) LoadHead(ctx context.Context) (int64, time.Time, error) {
	head, err := historydb.New(s.pool).ReadHead(ctx, s.agentID)
	return head.ID, head.UpdatedAt.Time, err
}

// LoadLastUserTimestamp returns the last completed user message's original local clock.
func (s *Store) LoadLastUserTimestamp(ctx context.Context) (time.Time, bool, error) {
	clock, err := historydb.New(s.pool).
		LastUserClock(ctx, historydb.LastUserClockParams{AgentID: s.agentID})
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	return clock.Instant.Time.In(time.FixedZone("", int(clock.UtcOffsetSeconds))), true, nil
}

// ListTurns pages completed history newest first from a consistent database snapshot.
func (s *Store) ListTurns(
	ctx context.Context,
	beforeID int64,
	limit int,
) (conversation.TurnPage, error) {
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 500)
	tx, err := s.pool.BeginTx(
		ctx,
		pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
	)
	if err != nil {
		return conversation.TurnPage{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := historydb.New(tx)
	head, err := q.ReadHead(ctx, s.agentID)
	if err != nil {
		return conversation.TurnPage{}, err
	}
	headers, err := q.ListTurnHeaders(
		ctx,
		historydb.ListTurnHeadersParams{
			AgentID:   s.agentID,
			BeforeID:  beforeID,
			PageLimit: int32(limit + 1),
		},
	)
	if err != nil {
		return conversation.TurnPage{}, err
	}
	hasMore := len(headers) > limit
	headers = headers[:min(len(headers), limit)]
	turns, err := readTurns(ctx, q, headers)
	if err != nil {
		return conversation.TurnPage{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return conversation.TurnPage{}, err
	}
	return conversation.TurnPage{Turns: turns, HeadSeq: head.ID, HasMore: hasMore}, nil
}

// LoadRecentMessages returns complete recent turns in chronological order.
func (s *Store) LoadRecentMessages(ctx context.Context, count int) ([]conversation.Message, error) {
	if count <= 0 {
		return nil, nil
	}
	page, err := s.ListTurns(ctx, 0, count)
	if err != nil {
		return nil, err
	}
	var messages []conversation.Message
	for _, turn := range slices.Backward(page.Turns) {
		messages = append(messages, turn.Messages...)
	}
	return messages, nil
}

// ReadTurn returns a complete source turn for explicit recall.
func (s *Store) ReadTurn(ctx context.Context, id int64) (conversation.Turn, error) {
	tx, err := s.pool.BeginTx(
		ctx,
		pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
	)
	if err != nil {
		return conversation.Turn{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := historydb.New(tx)
	header, err := q.ReadTurnHeader(ctx, historydb.ReadTurnHeaderParams{ID: id, AgentID: s.agentID})
	if err != nil {
		return conversation.Turn{}, err
	}
	turns, err := readTurns(
		ctx,
		q,
		[]historydb.ListTurnHeadersRow{{ID: header.ID, CreatedAt: header.CreatedAt}},
	)
	if err != nil {
		return conversation.Turn{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return conversation.Turn{}, err
	}
	return turns[0], nil
}

// DeleteTurn removes the source turn and all search data derived from it.
func (s *Store) DeleteTurn(ctx context.Context, id int64) error {
	_, err := historydb.New(s.pool).
		DeleteTurn(ctx, historydb.DeleteTurnParams{ID: id, AgentID: s.agentID})
	return err
}

func timestamp(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}
