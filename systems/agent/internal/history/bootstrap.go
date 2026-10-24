package history

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/q15co/q15/systems/agent/internal/history/historydb"
)

// OpenReadOnly opens an existing agent without running migrations or creating rows.
func OpenReadOnly(ctx context.Context, dsn, name string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil || dsn == "" || name == "" {
		return nil, errors.New("postgres connection settings and agent name are required")
	}
	cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, errors.New("open read-only conversation history")
	}
	id, err := historydb.New(pool).ReadAgent(ctx, name)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("read history agent: %w", err)
	}
	return &Store{pool: pool, agentID: id}, nil
}

// RequireReady refuses an incomplete cutover. A genuinely new, empty agent can bootstrap itself.
func (i *Indexer) RequireReady(ctx context.Context, memoryRoot string) error {
	q := historydb.New(i.store.pool)
	state, err := q.ReadBootstrap(ctx, i.store.agentID)
	if errors.Is(err, pgx.ErrNoRows) {
		_, statErr := os.Stat(filepath.Join(memoryRoot, "history"))
		if !errors.Is(statErr, os.ErrNotExist) {
			return errors.New(
				"history cutover is unprepared: import and backfill the frozen file history first",
			)
		}
		if err := q.InitializeEmpty(ctx, i.store.agentID); err != nil {
			return err
		}
		return i.PrepareCutover(ctx)
	}
	if err != nil {
		return err
	}
	if !state.PreparedAt.Valid || state.EmbeddingModelID == nil ||
		*state.EmbeddingModelID != i.modelID ||
		state.DigestPromptVersion == nil ||
		*state.DigestPromptVersion != DigestPromptVersion {
		return errors.New(
			"history cutover is unprepared for this embedding space and digest version; run memory backfill --prepare",
		)
	}
	return nil
}

// PrepareCutover records readiness only after every source turn and derived index is populated.
func (i *Indexer) PrepareCutover(ctx context.Context) error {
	q := historydb.New(i.store.pool)
	missing, err := q.CountMissingDigests(
		ctx,
		historydb.CountMissingDigestsParams{
			AgentID:       i.store.agentID,
			PromptVersion: DigestPromptVersion,
		},
	)
	if err != nil {
		return err
	}
	parts, err := q.PendingChunkParts(
		ctx,
		historydb.PendingChunkPartsParams{
			AgentID:        i.store.agentID,
			ChunkerVersion: chunkerVersion,
			BatchLimit:     1,
		},
	)
	if err != nil {
		return err
	}
	status, err := i.Status(ctx)
	if err != nil {
		return err
	}
	if missing != 0 || len(parts) != 0 || status.Digests != status.EmbeddedDigests ||
		status.Chunks != status.EmbeddedChunks {
		return fmt.Errorf(
			"history is incomplete: missing_digests=%d unchunked_sources=%t unembedded_digests=%d unembedded_chunks=%d",
			missing,
			len(parts) > 0,
			status.Digests-status.EmbeddedDigests,
			status.Chunks-status.EmbeddedChunks,
		)
	}
	n, err := q.MarkPrepared(
		ctx,
		historydb.MarkPreparedParams{
			AgentID:             i.store.agentID,
			EmbeddingModelID:    &i.modelID,
			DigestPromptVersion: stringPointer(DigestPromptVersion),
		},
	)
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("history source has not been imported and verified")
	}
	return nil
}

func stringPointer(value string) *string { return &value }

// RunBatch serializes workers for this agent, including CLI backfills, across model calls.
func (i *Indexer) RunBatch(ctx context.Context, limit int) (int, error) {
	conn, err := i.store.pool.Acquire(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Release()
	var locked bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1, $2)", int32(0x71313539), int32(i.store.agentID)).Scan(&locked); err != nil {
		return 0, err
	}
	if !locked {
		return 0, errors.New("another history indexer owns this agent")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if _, err := conn.Exec(cleanup, "SELECT pg_advisory_unlock($1, $2)", int32(0x71313539), int32(i.store.agentID)); err != nil {
			_ = conn.Conn().Close(cleanup)
		}
	}()
	chunks, chunkErr := i.Chunk(ctx, limit)
	digests, digestErr := i.Digest(ctx, limit)
	vectors, vectorErr := i.Embed(ctx, limit)
	return chunks + digests + vectors, errors.Join(chunkErr, digestErr, vectorErr)
}

// Run maintains derived memory without blocking foreground persistence or recall.
func (i *Indexer) Run(ctx context.Context) error {
	for {
		batchCtx, cancel := context.WithTimeout(ctx, 4*time.Minute)
		n, err := i.RunBatch(batchCtx, 8)
		cancel()
		if ctx.Err() != nil {
			return nil
		}
		delay := time.Second
		if err != nil {
			// Provider responses can contain source text; runtime logs expose counts only.
			log.Printf("q15: history event=index_batch_failed completed=%d", n)
			delay = 30 * time.Second
		} else if n == 0 {
			delay = 10 * time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

// Coverage reports preparation state without provider calls or database writes.
func (s *Store) Coverage(ctx context.Context) (historydb.ReadCoverageRow, error) {
	return historydb.New(s.pool).ReadCoverage(ctx, s.agentID)
}
