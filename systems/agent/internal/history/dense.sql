-- name: SearchDense768 :many
WITH digest_hits AS (
  SELECT 'digest'::text AS kind, d.turn_id AS unit_id, d.turn_id, d.body, t.created_at,
    (1 - (e.embedding::vector(768) <=> sqlc.arg(query_embedding)::vector(768)))::real AS score
  FROM memory.digest_embeddings e JOIN memory.turn_digests d ON d.turn_id=e.turn_id
  JOIN memory.turns t ON t.id=d.turn_id
  WHERE t.agent_id=sqlc.arg(agent_id) AND t.id <> ALL(sqlc.arg(exclude_turn_ids)::bigint[]) AND e.model_id=sqlc.arg(model_id) AND e.dimensions=768
  ORDER BY e.embedding::vector(768) <=> sqlc.arg(query_embedding)::vector(768) LIMIT sqlc.arg(result_limit)
), chunk_hits AS (
  SELECT 'chunk'::text AS kind, c.id AS unit_id, t.id AS turn_id, c.body, t.created_at,
    (1 - (e.embedding::vector(768) <=> sqlc.arg(query_embedding)::vector(768)))::real AS score
  FROM memory.chunk_embeddings e JOIN memory.chunks c ON c.id=e.chunk_id
  JOIN memory.chunk_runs r ON r.id=c.run_id JOIN memory.parts p ON p.id=r.part_id
  JOIN memory.messages m ON m.id=p.message_id JOIN memory.turns t ON t.id=m.turn_id
  WHERE NOT sqlc.arg(summaries_only)::boolean AND t.agent_id=sqlc.arg(agent_id) AND t.id <> ALL(sqlc.arg(exclude_turn_ids)::bigint[]) AND e.model_id=sqlc.arg(model_id) AND e.dimensions=768
  ORDER BY e.embedding::vector(768) <=> sqlc.arg(query_embedding)::vector(768) LIMIT sqlc.arg(result_limit)
) SELECT * FROM (SELECT * FROM digest_hits UNION ALL SELECT * FROM chunk_hits) hits
ORDER BY score DESC, turn_id DESC, kind, unit_id LIMIT sqlc.arg(result_limit);

-- name: SearchDense1024 :many
WITH digest_hits AS (
  SELECT 'digest'::text AS kind, d.turn_id AS unit_id, d.turn_id, d.body, t.created_at,
    (1 - (e.embedding::vector(1024) <=> sqlc.arg(query_embedding)::vector(1024)))::real AS score
  FROM memory.digest_embeddings e JOIN memory.turn_digests d ON d.turn_id=e.turn_id
  JOIN memory.turns t ON t.id=d.turn_id
  WHERE t.agent_id=sqlc.arg(agent_id) AND t.id <> ALL(sqlc.arg(exclude_turn_ids)::bigint[]) AND e.model_id=sqlc.arg(model_id) AND e.dimensions=1024
  ORDER BY e.embedding::vector(1024) <=> sqlc.arg(query_embedding)::vector(1024) LIMIT sqlc.arg(result_limit)
), chunk_hits AS (
  SELECT 'chunk'::text AS kind, c.id AS unit_id, t.id AS turn_id, c.body, t.created_at,
    (1 - (e.embedding::vector(1024) <=> sqlc.arg(query_embedding)::vector(1024)))::real AS score
  FROM memory.chunk_embeddings e JOIN memory.chunks c ON c.id=e.chunk_id
  JOIN memory.chunk_runs r ON r.id=c.run_id JOIN memory.parts p ON p.id=r.part_id
  JOIN memory.messages m ON m.id=p.message_id JOIN memory.turns t ON t.id=m.turn_id
  WHERE NOT sqlc.arg(summaries_only)::boolean AND t.agent_id=sqlc.arg(agent_id) AND t.id <> ALL(sqlc.arg(exclude_turn_ids)::bigint[]) AND e.model_id=sqlc.arg(model_id) AND e.dimensions=1024
  ORDER BY e.embedding::vector(1024) <=> sqlc.arg(query_embedding)::vector(1024) LIMIT sqlc.arg(result_limit)
) SELECT * FROM (SELECT * FROM digest_hits UNION ALL SELECT * FROM chunk_hits) hits
ORDER BY score DESC, turn_id DESC, kind, unit_id LIMIT sqlc.arg(result_limit);
