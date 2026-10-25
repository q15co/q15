-- name: EnsureAgent :one
INSERT INTO memory.agents (name) VALUES ($1)
ON CONFLICT (name) DO UPDATE SET name = excluded.name RETURNING id;

-- name: ReserveTurn :one
INSERT INTO memory.turns (agent_id) VALUES ($1) RETURNING id;

-- name: CompleteTurn :execrows
UPDATE memory.turns SET completed_at = clock_timestamp()
WHERE id = $1 AND agent_id = $2 AND completed_at IS NULL;

-- name: LockTurn :one
SELECT
    id,
    agent_id,
    created_at,
    completed_at
FROM memory.turns
WHERE id = $1 AND agent_id = $2 FOR UPDATE;

-- Neighboring summaries need foreign-key key-share locks on this immutable turn.
-- name: LockDigestTurn :one
SELECT
    id,
    agent_id,
    created_at,
    completed_at
FROM memory.turns
WHERE id = $1 AND agent_id = $2 FOR NO KEY UPDATE;

-- name: InsertImportedTurn :execrows
INSERT INTO memory.turns (id, agent_id, created_at) OVERRIDING SYSTEM VALUE VALUES ($1, $2, $3)
ON CONFLICT (id) DO NOTHING;

-- name: InsertMessage :one
INSERT INTO memory.messages (turn_id, position, role) VALUES ($1, $2, $3) RETURNING id;

-- name: InsertClock :exec
INSERT INTO memory.message_clocks (
    message_id, instant, utc_offset_seconds, since_previous_ns
) VALUES ($1, $2, $3, $4);

-- name: InsertDelivery :exec
INSERT INTO memory.scheduled_deliveries (message_id, job_id, run_id, channel, chat_id, delivered_at)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: InsertPart :one
INSERT INTO memory.parts (message_id, position, kind) VALUES ($1, $2, $3) RETURNING id;

-- name: InsertText :exec
INSERT INTO memory.part_text (part_id, body, raw_body, disposition) VALUES ($1, $2, $3, $4);

-- name: InsertReasoning :exec
INSERT INTO memory.part_reasoning (part_id, body, raw_body, replay, raw_replay) VALUES (
    $1, $2, $3, $4, $5
);

-- name: InsertToolCall :exec
INSERT INTO memory.part_tool_calls (part_id, call_id, tool_name, arguments, raw_arguments) VALUES (
    $1, $2, $3, $4, $5
);

-- name: InsertToolResult :exec
INSERT INTO memory.part_tool_results (part_id, call_id, body, raw_body, is_error) VALUES (
    $1, $2, $3, $4, $5
);

-- name: InsertMedia :exec
INSERT INTO memory.part_media (part_id, media_kind, media_ref, data_url) VALUES ($1, $2, $3, $4);

-- name: ListTurnHeaders :many
SELECT
    id,
    created_at
FROM memory.turns
WHERE
    agent_id = $1
    AND completed_at IS NOT NULL
    AND (sqlc.arg('before_id')::bigint = 0 OR id < sqlc.arg('before_id'))
ORDER BY id DESC LIMIT sqlc.arg('page_limit');


-- name: ReadTurnHeader :one
SELECT
    id,
    created_at
FROM memory.turns
WHERE id = $1 AND agent_id = $2 AND completed_at IS NOT NULL;

-- name: ReadHead :one
SELECT
    coalesce(max(id), 0)::bigint AS id,
    coalesce(max(created_at), 'epoch'::timestamptz)::timestamptz AS updated_at
FROM memory.turns
WHERE agent_id = $1;

-- name: ReadMessages :many
SELECT
    m.id,
    m.turn_id,
    m.position,
    m.role,
    c.instant,
    c.utc_offset_seconds,
    c.since_previous_ns,
    d.job_id,
    d.run_id,
    d.channel,
    d.chat_id,
    d.delivered_at
FROM memory.messages AS m
LEFT JOIN memory.message_clocks AS c ON m.id = c.message_id
LEFT JOIN memory.scheduled_deliveries AS d ON m.id = d.message_id
WHERE m.turn_id = any(sqlc.arg('turn_ids')::bigint[])
ORDER BY m.turn_id, m.position;

-- name: ReadParts :many
SELECT
    p.id,
    p.message_id,
    p.position,
    p.kind,
    t.body AS text_body,
    t.raw_body AS raw_text_body,
    t.disposition,
    r.body AS reasoning_body,
    r.raw_body AS raw_reasoning_body,
    r.replay,
    r.raw_replay,
    c.call_id,
    c.tool_name,
    c.arguments,
    c.raw_arguments,
    o.call_id AS result_call_id,
    o.body AS result_body,
    o.raw_body AS raw_result_body,
    o.is_error,
    v.media_kind,
    v.media_ref,
    v.data_url
FROM memory.parts AS p
INNER JOIN memory.messages AS m ON p.message_id = m.id
LEFT JOIN memory.part_text AS t ON p.id = t.part_id
LEFT JOIN memory.part_reasoning AS r ON p.id = r.part_id
LEFT JOIN memory.part_tool_calls AS c ON p.id = c.part_id
LEFT JOIN memory.part_tool_results AS o ON p.id = o.part_id
LEFT JOIN memory.part_media AS v ON p.id = v.part_id
WHERE m.turn_id = any(sqlc.arg('turn_ids')::bigint[])
ORDER BY m.turn_id, m.position, p.position;

-- name: LastUserClock :one
SELECT
    c.instant,
    c.utc_offset_seconds
FROM memory.message_clocks AS c
INNER JOIN memory.messages AS m ON c.message_id = m.id
INNER JOIN memory.turns AS t ON m.turn_id = t.id
WHERE
    t.agent_id = $1 AND t.completed_at IS NOT NULL
    AND (
        sqlc.arg('before_turn')::bigint = 0
        OR (t.id, m.position) < (sqlc.arg('before_turn'), sqlc.arg('before_position')::integer)
    )
ORDER BY t.id DESC, m.position DESC LIMIT 1;

-- name: DeleteTurn :execrows
DELETE FROM memory.turns
WHERE id = $1 AND agent_id = $2;

-- name: CountTurns :one
SELECT count(*) FROM memory.turns
WHERE agent_id = $1 AND completed_at IS NOT NULL;

-- name: FindDelivery :one
SELECT m.turn_id FROM memory.scheduled_deliveries AS d
INNER JOIN memory.messages AS m ON d.message_id = m.id
INNER JOIN memory.turns AS t ON m.turn_id = t.id
WHERE t.agent_id = $1 AND d.job_id = $2 AND d.run_id = $3 AND d.channel = $4 AND d.chat_id = $5;

-- name: AdvanceTurnIdentity :one
WITH identity_state AS (
    SELECT
        greatest(
            sqlc.arg('imported_head')::bigint, coalesce((SELECT max(id) FROM memory.turns), 0)
        ) AS target_value,
        coalesce(
            pg_sequence_last_value(pg_get_serial_sequence('memory.turns', 'id')::regclass), 0
        ) AS current_value
)

SELECT CASE
    WHEN
        target_value > current_value
        THEN setval(pg_get_serial_sequence('memory.turns', 'id'), target_value, TRUE)
    ELSE current_value
END::bigint FROM identity_state;

-- name: EnsureGenerationModel :one
INSERT INTO memory.generation_models (provider, model) VALUES ($1, $2)
ON CONFLICT (provider, model) DO UPDATE SET model = excluded.model RETURNING id;

-- name: EnsureEmbeddingModel :one
INSERT INTO memory.embedding_models (provider, model, dimensions, prefix_version) VALUES (
    $1, $2, $3, $4
)
ON CONFLICT (provider, model, dimensions, prefix_version) DO UPDATE
    SET model = excluded.model
RETURNING id;

-- name: PendingChunkParts :many
SELECT
    p.id,
    p.kind,
    t.body AS text_body,
    t.raw_body AS raw_text_body,
    c.tool_name,
    c.arguments,
    c.raw_arguments,
    o.body AS result_body,
    o.raw_body AS raw_result_body
FROM memory.parts AS p INNER JOIN memory.messages AS m ON p.message_id = m.id
INNER JOIN memory.turns AS turn ON m.turn_id = turn.id
LEFT JOIN memory.part_text AS t ON p.id = t.part_id
LEFT JOIN memory.part_tool_calls AS c ON p.id = c.part_id
LEFT JOIN memory.part_tool_results AS o ON p.id = o.part_id
LEFT JOIN memory.chunk_runs AS r ON p.id = r.part_id
WHERE
    turn.agent_id = $1 AND turn.completed_at IS NOT NULL
    AND p.kind IN ('text', 'tool_call', 'tool_result')
    AND (r.id IS NULL OR r.chunker_version <> $2)
ORDER BY p.id LIMIT sqlc.arg('batch_limit');

-- name: DeleteChunkRun :exec
DELETE FROM memory.chunk_runs
WHERE part_id = $1;

-- name: InsertChunkRun :one
INSERT INTO memory.chunk_runs (part_id, chunker_version) VALUES ($1, $2) RETURNING id;

-- name: InsertChunk :one
INSERT INTO memory.chunks (run_id, position, body) VALUES ($1, $2, $3) RETURNING id;

-- name: PendingChunkEmbeddings :many
SELECT
    c.id,
    c.body
FROM memory.chunks AS c INNER JOIN memory.chunk_runs AS r ON c.run_id = r.id
INNER JOIN memory.parts AS p ON r.part_id = p.id
INNER JOIN memory.messages AS m ON p.message_id = m.id
INNER JOIN memory.turns AS t ON m.turn_id = t.id
WHERE
    t.agent_id = $1 AND NOT EXISTS (
        SELECT 1 FROM memory.chunk_embeddings AS e
        WHERE e.chunk_id = c.id AND e.model_id = $2
    )
ORDER BY c.id LIMIT sqlc.arg('batch_limit');

-- name: InsertChunkEmbedding :exec
INSERT INTO memory.chunk_embeddings (chunk_id, model_id, embedding) VALUES (
    $1, $2, sqlc.arg('embedding')::vector
)
ON CONFLICT (chunk_id, model_id) DO NOTHING;

-- name: PendingDigestEmbeddings :many
SELECT
    d.turn_id,
    d.body
FROM memory.turn_digests AS d INNER JOIN memory.turns AS t ON d.turn_id = t.id
WHERE
    t.agent_id = $1 AND NOT EXISTS (
        SELECT 1 FROM memory.digest_embeddings AS e
        WHERE e.turn_id = d.turn_id AND e.model_id = $2
    )
ORDER BY d.turn_id LIMIT sqlc.arg('batch_limit');

-- name: InsertDigestEmbedding :exec
INSERT INTO memory.digest_embeddings (turn_id, model_id, embedding) VALUES (
    $1, $2, sqlc.arg('embedding')::vector
)
ON CONFLICT (turn_id, model_id) DO NOTHING;

-- name: ReadDigest :one
SELECT
    turn_id,
    body,
    prompt_version,
    created_at
FROM memory.turn_digests
WHERE turn_id = $1;

-- name: ReadDigests :many
SELECT
    d.turn_id,
    d.body,
    t.created_at
FROM memory.turn_digests AS d
INNER JOIN memory.turns AS t ON d.turn_id = t.id
WHERE t.agent_id = $1 AND d.turn_id = any(sqlc.arg('turn_ids')::bigint[])
ORDER BY d.turn_id;

-- name: PendingDigests :many
SELECT
    t.id,
    t.created_at
FROM memory.turns AS t
LEFT JOIN memory.turn_digests AS d ON t.id = d.turn_id
LEFT JOIN memory.digest_failures AS f ON t.id = f.turn_id
WHERE
    t.agent_id = $1 AND t.completed_at IS NOT NULL
    AND (d.turn_id IS NULL OR d.prompt_version <> $2)
    AND (f.turn_id IS NULL OR f.next_attempt_at <= clock_timestamp())
ORDER BY t.id LIMIT sqlc.arg('batch_limit');

-- name: DeleteDigest :exec
DELETE FROM memory.turn_digests
WHERE turn_id = $1;

-- name: InsertDigest :exec
INSERT INTO memory.turn_digests (
    turn_id, body, model_id, prompt_version, input_hash, input_tokens, output_tokens
)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: InsertDigestContext :exec
INSERT INTO memory.digest_context (digest_turn_id, position, context_turn_id) VALUES ($1, $2, $3);

-- name: RecordDigestFailure :exec
INSERT INTO memory.digest_failures (turn_id, attempts, next_attempt_at)
VALUES ($1, 1, clock_timestamp() + interval '30 seconds')
ON CONFLICT (turn_id) DO UPDATE SET
    attempts = memory.digest_failures.attempts + 1,
    next_attempt_at = clock_timestamp()
    + interval '30 seconds' * power(2, least(6, memory.digest_failures.attempts))::integer;

-- name: ClearDigestFailure :exec
DELETE FROM memory.digest_failures
WHERE turn_id = $1;

-- name: ReadIndexStatus :one
SELECT
    (
        SELECT count(*) FROM memory.turns AS base
        WHERE base.agent_id = $1 AND base.completed_at IS NOT NULL
    )::bigint AS turns,
    (
        SELECT count(*)
        FROM memory.turn_digests AS d
        INNER JOIN memory.turns AS t ON d.turn_id = t.id
        WHERE t.agent_id = $1
    )::bigint AS digests,
    (
        SELECT count(*)
        FROM memory.chunks AS c
        INNER JOIN memory.chunk_runs AS r ON c.run_id = r.id
        INNER JOIN memory.parts AS p ON r.part_id = p.id
        INNER JOIN memory.messages AS m ON p.message_id = m.id
        INNER JOIN memory.turns AS t ON m.turn_id = t.id
        WHERE t.agent_id = $1
    )::bigint AS chunks,
    (
        SELECT count(*)
        FROM memory.digest_embeddings AS e
        INNER JOIN memory.turns AS t ON e.turn_id = t.id
        WHERE t.agent_id = $1 AND e.model_id = $2
    )::bigint AS embedded_digests,
    (
        SELECT count(*)
        FROM memory.chunk_embeddings AS e
        INNER JOIN memory.chunks AS c ON e.chunk_id = c.id
        INNER JOIN memory.chunk_runs AS r ON c.run_id = r.id
        INNER JOIN memory.parts AS p ON r.part_id = p.id
        INNER JOIN memory.messages AS m ON p.message_id = m.id
        INNER JOIN memory.turns AS t ON m.turn_id = t.id
        WHERE t.agent_id = $1 AND e.model_id = $2
    )::bigint AS embedded_chunks;

-- name: SearchLexical :many
WITH q AS (SELECT websearch_to_tsquery('simple', sqlc.arg('query')::text) AS terms),

hits AS (
    SELECT
        'digest'::text AS kind,
        d.turn_id AS unit_id,
        d.turn_id,
        d.body,
        t.created_at,
        ts_rank_cd(to_tsvector('simple', d.body), q.terms)::real AS score
    FROM memory.turn_digests AS d INNER JOIN memory.turns AS t ON d.turn_id = t.id CROSS JOIN q
    WHERE
        t.agent_id = sqlc.arg('agent_id')
        AND t.id <> all(sqlc.arg('exclude_turn_ids')::bigint[])
        AND to_tsvector('simple', d.body) @@ q.terms
    UNION ALL
    SELECT
        'chunk'::text AS kind,
        c.id AS unit_id,
        t.id AS turn_id,
        c.body,
        t.created_at,
        ts_rank_cd(to_tsvector('simple', c.body), q.terms)::real AS score
    FROM memory.chunks AS c
    INNER JOIN memory.chunk_runs AS r ON c.run_id = r.id
    INNER JOIN memory.parts AS p ON r.part_id = p.id
    INNER JOIN memory.messages AS m ON p.message_id = m.id
    INNER JOIN memory.turns AS t ON m.turn_id = t.id
    CROSS JOIN q
    WHERE
        NOT sqlc.arg('summaries_only')::boolean
        AND t.agent_id = sqlc.arg('agent_id')
        AND t.id <> all(sqlc.arg('exclude_turn_ids')::bigint[])
        AND to_tsvector('simple', c.body) @@ q.terms
)

SELECT *
FROM hits
ORDER BY score DESC, turn_id DESC, kind ASC, unit_id ASC
LIMIT sqlc.arg('result_limit');

-- name: SearchDense :many
WITH digest_hits AS (
    SELECT
        'digest'::text AS kind,
        d.turn_id AS unit_id,
        d.turn_id,
        d.body,
        t.created_at,
        (1 - (e.embedding <=> sqlc.arg('query_embedding')::vector))::real AS score
    FROM memory.digest_embeddings AS e INNER JOIN memory.turn_digests AS d ON e.turn_id = d.turn_id
    INNER JOIN memory.turns AS t ON d.turn_id = t.id
    WHERE
        t.agent_id = sqlc.arg('agent_id')
        AND t.id <> all(sqlc.arg('exclude_turn_ids')::bigint[])
        AND e.model_id = sqlc.arg('model_id')
    ORDER BY e.embedding <=> sqlc.arg('query_embedding')::vector LIMIT sqlc.arg('result_limit')
),

chunk_hits AS (
    SELECT
        'chunk'::text AS kind,
        c.id AS unit_id,
        t.id AS turn_id,
        c.body,
        t.created_at,
        (1 - (e.embedding <=> sqlc.arg('query_embedding')::vector))::real AS score
    FROM memory.chunk_embeddings AS e INNER JOIN memory.chunks AS c ON e.chunk_id = c.id
    INNER JOIN memory.chunk_runs AS r ON c.run_id = r.id
    INNER JOIN memory.parts AS p ON r.part_id = p.id
    INNER JOIN memory.messages AS m ON p.message_id = m.id
    INNER JOIN memory.turns AS t ON m.turn_id = t.id
    WHERE
        NOT sqlc.arg('summaries_only')::boolean
        AND t.agent_id = sqlc.arg('agent_id')
        AND t.id <> all(sqlc.arg('exclude_turn_ids')::bigint[])
        AND e.model_id = sqlc.arg('model_id')
    ORDER BY e.embedding <=> sqlc.arg('query_embedding')::vector LIMIT sqlc.arg('result_limit')
)

SELECT * FROM (
    SELECT * FROM digest_hits
    UNION ALL
    SELECT * FROM chunk_hits
) AS hits
ORDER BY score DESC, turn_id DESC, kind ASC, unit_id ASC LIMIT sqlc.arg('result_limit');


-- name: ReadAgent :one
SELECT id FROM memory.agents
WHERE name = $1;

-- name: RecordImport :exec
INSERT INTO memory.bootstrap (agent_id, source_fingerprint, source_head, source_turns)
VALUES ($1, $2, $3, $4)
ON CONFLICT (agent_id) DO UPDATE SET
    source_fingerprint = excluded.source_fingerprint,
    source_head = excluded.source_head,
    source_turns = excluded.source_turns,
    verified_at = clock_timestamp(),
    prepared_at = CASE
        WHEN
            memory.bootstrap.source_fingerprint IS NOT DISTINCT FROM excluded.source_fingerprint
            AND memory.bootstrap.source_head = excluded.source_head
            AND memory.bootstrap.source_turns = excluded.source_turns
            THEN memory.bootstrap.prepared_at
    END,
    embedding_model_id = CASE
        WHEN
            memory.bootstrap.source_fingerprint IS NOT DISTINCT FROM excluded.source_fingerprint
            AND memory.bootstrap.source_head = excluded.source_head
            AND memory.bootstrap.source_turns = excluded.source_turns
            THEN memory.bootstrap.embedding_model_id
    END,
    digest_prompt_version = CASE
        WHEN
            memory.bootstrap.source_fingerprint IS NOT DISTINCT FROM excluded.source_fingerprint
            AND memory.bootstrap.source_head = excluded.source_head
            AND memory.bootstrap.source_turns = excluded.source_turns
            THEN memory.bootstrap.digest_prompt_version
    END;

-- name: InitializeEmpty :exec
INSERT INTO memory.bootstrap (agent_id, source_head, source_turns)
SELECT
    $1 AS agent_id,
    0 AS source_head,
    0 AS source_turns
WHERE
    NOT EXISTS (
        SELECT 1 FROM memory.turns
        WHERE agent_id = $1
    )
ON CONFLICT (agent_id) DO NOTHING;

-- name: ReadBootstrap :one
SELECT
    agent_id,
    source_fingerprint,
    source_head,
    source_turns,
    verified_at,
    prepared_at,
    embedding_model_id,
    digest_prompt_version
FROM memory.bootstrap
WHERE agent_id = $1;

-- name: MarkPrepared :execrows
UPDATE
    memory.bootstrap SET
    prepared_at = clock_timestamp(), embedding_model_id = $2,
    digest_prompt_version = $3
WHERE agent_id = $1;

-- name: CountMissingDigests :one
SELECT count(*) FROM memory.turns AS t LEFT JOIN memory.turn_digests AS d ON t.id = d.turn_id
WHERE
    t.agent_id = $1 AND t.completed_at IS NOT NULL
    AND (d.turn_id IS NULL OR d.prompt_version <> $2);

-- name: LockChunkPart :one
SELECT
    p.id,
    coalesce(r.chunker_version, '')::text AS chunker_version
FROM memory.parts AS p
LEFT JOIN memory.chunk_runs AS r ON p.id = r.part_id
WHERE p.id = $1 FOR UPDATE OF p;


-- name: ReadCoverage :one
SELECT
    (
        SELECT count(*) FROM memory.turns AS rt
        WHERE rt.agent_id = $1 AND rt.completed_at IS NOT NULL
    )::bigint AS turns,
    (
        SELECT count(*)
        FROM memory.turn_digests AS d
        INNER JOIN memory.turns AS t ON d.turn_id = t.id
        WHERE t.agent_id = $1
    )::bigint AS digests,
    (
        SELECT count(*)
        FROM memory.digest_failures AS f
        INNER JOIN memory.turns AS t ON f.turn_id = t.id
        WHERE t.agent_id = $1
    )::bigint AS digest_failures,
    (
        SELECT count(*)
        FROM memory.chunks AS c
        INNER JOIN memory.chunk_runs AS r ON c.run_id = r.id
        INNER JOIN memory.parts AS p ON r.part_id = p.id
        INNER JOIN memory.messages AS m ON p.message_id = m.id
        INNER JOIN memory.turns AS t ON m.turn_id = t.id
        WHERE t.agent_id = $1
    )::bigint AS chunks,
    (
        SELECT count(*)
        FROM memory.digest_embeddings AS de
        INNER JOIN memory.turns AS t ON de.turn_id = t.id
        WHERE t.agent_id = $1 AND de.model_id = b.embedding_model_id
    )::bigint AS embedded_digests,
    (
        SELECT count(*)
        FROM memory.chunk_embeddings AS ce
        INNER JOIN memory.chunks AS c ON ce.chunk_id = c.id
        INNER JOIN memory.chunk_runs AS r ON c.run_id = r.id
        INNER JOIN memory.parts AS p ON r.part_id = p.id
        INNER JOIN memory.messages AS m ON p.message_id = m.id
        INNER JOIN memory.turns AS t ON m.turn_id = t.id
        WHERE t.agent_id = $1 AND ce.model_id = b.embedding_model_id
    )::bigint AS embedded_chunks,
    b.source_head,
    b.source_turns,
    b.verified_at,
    b.prepared_at,
    b.digest_prompt_version,
    e.provider AS embedding_provider,
    e.model AS embedding_model,
    e.dimensions
FROM memory.agents AS a LEFT JOIN memory.bootstrap AS b ON a.id = b.agent_id
LEFT JOIN memory.embedding_models AS e ON b.embedding_model_id = e.id
WHERE a.id = $1;


-- name: ReadLastAssistantHeader :one
SELECT
    t.id,
    t.created_at
FROM memory.turns AS t
WHERE
    t.agent_id = $1 AND t.completed_at IS NOT NULL
    AND EXISTS (
        SELECT 1 FROM memory.messages AS m
        WHERE m.turn_id = t.id AND m.role = 'assistant'
    )
ORDER BY t.id DESC LIMIT 1;

-- name: ReadSourcePage :one
WITH selected AS (
    SELECT
        p.id AS part_id,
        p.position AS part_position,
        p.kind,
        m.position AS message_position,
        m.role,
        t.created_at,
        cl.instant,
        cl.utc_offset_seconds,
        cl.since_previous_ns,
        coalesce(tc.call_id, tr.call_id, '')::text AS call_id,
        coalesce(tc.tool_name, '')::text AS tool_name,
        coalesce(tr.is_error, FALSE)::boolean AS is_error,
        coalesce(pt.disposition, '')::text AS disposition,
        sd.job_id,
        sd.run_id,
        sd.channel,
        sd.chat_id,
        sd.delivered_at,
        (re.replay IS NOT NULL OR re.raw_replay IS NOT NULL)::boolean AS has_replay,
        CASE
            WHEN sqlc.arg('field')::text = 'replay' THEN re.replay::text
            ELSE coalesce(
                pt.body, re.body, tr.body, tc.arguments::text,
                CASE
                    WHEN
                        pm.part_id IS NOT NULL
                        THEN
                            json_build_object(
                                'media_kind',
                                pm.media_kind,
                                'media_ref',
                                pm.media_ref,
                                'data_url',
                                pm.data_url
                            )::text
                END,
                ''
            )
        END AS body,
        CASE
            WHEN sqlc.arg('field')::text = 'replay' THEN re.raw_replay
            ELSE coalesce(pt.raw_body, re.raw_body, tr.raw_body, tc.raw_arguments)
        END AS raw_body,
        coalesce(
            (
                SELECT p2.id
                FROM memory.parts AS p2
                INNER JOIN memory.messages AS m2 ON p2.message_id = m2.id
                WHERE
                    m2.turn_id = t.id AND (m2.position, p2.position) > (m.position, p.position)
                ORDER BY m2.position, p2.position LIMIT 1
            ), 0
        )::bigint AS next_part_id
    FROM memory.parts AS p
    INNER JOIN memory.messages AS m ON p.message_id = m.id
    INNER JOIN memory.turns AS t ON m.turn_id = t.id
    LEFT JOIN memory.message_clocks AS cl ON m.id = cl.message_id
    LEFT JOIN memory.scheduled_deliveries AS sd ON m.id = sd.message_id
    LEFT JOIN memory.part_text AS pt ON p.id = pt.part_id
    LEFT JOIN memory.part_reasoning AS re ON p.id = re.part_id
    LEFT JOIN memory.part_tool_calls AS tc ON p.id = tc.part_id
    LEFT JOIN memory.part_tool_results AS tr ON p.id = tr.part_id
    LEFT JOIN memory.part_media AS pm ON p.id = pm.part_id
    WHERE
        t.id = sqlc.arg('turn_id')
        AND t.agent_id = sqlc.arg('agent_id')
        AND t.completed_at IS NOT NULL
        AND (sqlc.arg('part_id')::bigint = 0 OR (m.position, p.position) >= (
            SELECT
                cm.position AS message_position,
                cp.position AS part_position
            FROM memory.parts AS cp INNER JOIN memory.messages AS cm ON cp.message_id = cm.id
            WHERE cp.id = sqlc.arg('part_id') AND cm.turn_id = t.id
        ))
    ORDER BY m.position, p.position LIMIT 1
)

SELECT
    part_id,
    part_position,
    kind,
    message_position,
    role,
    created_at,
    instant,
    utc_offset_seconds,
    since_previous_ns,
    call_id,
    tool_name,
    is_error,
    disposition,
    job_id,
    run_id,
    channel,
    chat_id,
    delivered_at,
    has_replay,
    next_part_id,
    coalesce(
        substring(
            body FROM sqlc.arg('page_offset')::integer + 1 FOR sqlc.arg('page_limit')::integer + 1
        ),
        ''
    )::text AS source,
    substring(
        raw_body FROM sqlc.arg('page_offset')::integer + 1 FOR sqlc.arg('page_limit')::integer + 1
    )::bytea AS raw_source
FROM selected;

-- name: ReadConversationPartIDs :many
WITH eligible AS (
    SELECT
        p.id,
        m.position AS message_position,
        p.position AS part_position,
        m.role
    FROM memory.parts AS p INNER JOIN memory.messages AS m ON p.message_id = m.id
    INNER JOIN memory.turns AS t ON m.turn_id = t.id
    INNER JOIN memory.part_text AS pt ON p.id = pt.part_id
    WHERE
        t.id = sqlc.arg('turn_id')
        AND t.agent_id = sqlc.arg('agent_id')
        AND t.completed_at IS NOT NULL
        AND (
            m.role = 'user'
            OR (m.role = 'assistant' AND coalesce(pt.disposition, '') <> 'commentary')
        )
),

conversation AS (
    (
        SELECT * FROM eligible
        WHERE role = 'user'
        ORDER BY message_position, part_position LIMIT 4
    )
    UNION ALL
    (
        SELECT candidate.* FROM eligible AS candidate
        WHERE
            candidate.role = 'assistant'
            AND candidate.message_position = (
                SELECT max(latest.message_position) FROM eligible AS latest
                WHERE latest.role = 'assistant'
            )
        ORDER BY candidate.part_position LIMIT 4
    )
)

SELECT id FROM conversation
ORDER BY message_position, part_position;

-- name: ReadToolEvidence :many
SELECT
    p.id AS part_id,
    coalesce(matched_call.tool_name, 'unknown')::text AS tool_name,
    left(coalesce(tr.body, '[binary tool result]'), 120)::text AS preview
FROM memory.parts AS p INNER JOIN memory.messages AS m ON p.message_id = m.id
INNER JOIN memory.turns AS t ON m.turn_id = t.id
INNER JOIN memory.part_tool_results AS tr ON p.id = tr.part_id
LEFT JOIN LATERAL (
    SELECT tc.tool_name FROM memory.part_tool_calls AS tc
    INNER JOIN memory.parts AS cp ON tc.part_id = cp.id
    INNER JOIN memory.messages AS cm ON cp.message_id = cm.id
    WHERE cm.turn_id = t.id AND tc.call_id = tr.call_id AND cm.position < m.position
    ORDER BY cm.position DESC, cp.position DESC LIMIT 1
) AS matched_call ON TRUE
WHERE
    t.id = sqlc.arg('turn_id') AND t.agent_id = sqlc.arg('agent_id') AND t.completed_at IS NOT NULL
ORDER BY m.position, p.position LIMIT sqlc.arg('page_limit') OFFSET sqlc.arg('page_offset');

-- name: ReadChunkSourceRefs :many
SELECT
    c.id AS chunk_id,
    r.part_id
FROM memory.chunks AS c INNER JOIN memory.chunk_runs AS r ON c.run_id = r.id
INNER JOIN memory.parts AS p ON r.part_id = p.id
INNER JOIN memory.messages AS m ON p.message_id = m.id
INNER JOIN memory.turns AS t ON m.turn_id = t.id
WHERE c.id = any(sqlc.arg('chunk_ids')::bigint[]) AND t.agent_id = sqlc.arg('agent_id');
