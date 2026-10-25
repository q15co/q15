-- name: SearchDense768 :many
WITH digest_hits AS (
    SELECT
        'digest'::text AS kind,
        d.turn_id AS unit_id,
        d.turn_id,
        d.body,
        t.created_at,
        (1 - (e.embedding::vector(768) <=> sqlc.arg('query_embedding')::vector(768)))::real AS score
    FROM memory.digest_embeddings AS e INNER JOIN memory.turn_digests AS d ON e.turn_id = d.turn_id
    INNER JOIN memory.turns AS t ON d.turn_id = t.id
    WHERE
        t.agent_id = sqlc.arg('agent_id')
        AND t.id <> all(sqlc.arg('exclude_turn_ids')::bigint[])
        AND e.model_id = sqlc.arg('model_id')
        AND e.dimensions = 768
    ORDER BY e.embedding::vector(768) <=> sqlc.arg('query_embedding')::vector(768)
    LIMIT sqlc.arg('result_limit')
),

chunk_hits AS (
    SELECT
        'chunk'::text AS kind,
        c.id AS unit_id,
        t.id AS turn_id,
        c.body,
        t.created_at,
        (1 - (e.embedding::vector(768) <=> sqlc.arg('query_embedding')::vector(768)))::real AS score
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
        AND e.dimensions = 768
    ORDER BY e.embedding::vector(768) <=> sqlc.arg('query_embedding')::vector(768)
    LIMIT sqlc.arg('result_limit')
)

SELECT * FROM (
    SELECT * FROM digest_hits
    UNION ALL
    SELECT * FROM chunk_hits
) AS hits
ORDER BY score DESC, turn_id DESC, kind ASC, unit_id ASC LIMIT sqlc.arg('result_limit');

-- name: SearchDense1024 :many
WITH digest_hits AS (
    SELECT
        'digest'::text AS kind,
        d.turn_id AS unit_id,
        d.turn_id,
        d.body,
        t.created_at,
        (1 - (e.embedding::vector(1024) <=> sqlc.arg('query_embedding')::vector(1024)))::real
            AS score
    FROM memory.digest_embeddings AS e INNER JOIN memory.turn_digests AS d ON e.turn_id = d.turn_id
    INNER JOIN memory.turns AS t ON d.turn_id = t.id
    WHERE
        t.agent_id = sqlc.arg('agent_id')
        AND t.id <> all(sqlc.arg('exclude_turn_ids')::bigint[])
        AND e.model_id = sqlc.arg('model_id')
        AND e.dimensions = 1024
    ORDER BY e.embedding::vector(1024) <=> sqlc.arg('query_embedding')::vector(1024)
    LIMIT sqlc.arg('result_limit')
),

chunk_hits AS (
    SELECT
        'chunk'::text AS kind,
        c.id AS unit_id,
        t.id AS turn_id,
        c.body,
        t.created_at,
        (1 - (e.embedding::vector(1024) <=> sqlc.arg('query_embedding')::vector(1024)))::real
            AS score
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
        AND e.dimensions = 1024
    ORDER BY e.embedding::vector(1024) <=> sqlc.arg('query_embedding')::vector(1024)
    LIMIT sqlc.arg('result_limit')
)

SELECT * FROM (
    SELECT * FROM digest_hits
    UNION ALL
    SELECT * FROM chunk_hits
) AS hits
ORDER BY score DESC, turn_id DESC, kind ASC, unit_id ASC LIMIT sqlc.arg('result_limit');
