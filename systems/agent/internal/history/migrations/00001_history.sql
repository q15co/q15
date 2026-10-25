-- +goose Up
CREATE SCHEMA memory;
CREATE EXTENSION IF NOT EXISTS vector;

CREATE TYPE memory.message_role AS ENUM ('user', 'assistant', 'tool');
CREATE TYPE memory.part_kind AS ENUM ('text', 'reasoning', 'tool_call', 'tool_result', 'media');
CREATE TYPE memory.media_kind AS ENUM (
    'image', 'audio', 'video', 'video_note', 'document', 'sticker', 'animation'
);

CREATE TABLE memory.agents (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name text NOT NULL UNIQUE CHECK (name <> '')
);

CREATE TABLE memory.turns (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY CHECK (id > 0),
    agent_id bigint NOT NULL REFERENCES memory.agents (id),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    completed_at timestamptz
);
CREATE INDEX turns_agent_tail ON memory.turns (agent_id, id DESC) WHERE completed_at IS NOT NULL;

CREATE TABLE memory.messages (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    turn_id bigint NOT NULL REFERENCES memory.turns (id) ON DELETE CASCADE,
    position integer NOT NULL CHECK (position >= 0),
    role memory.message_role NOT NULL,
    UNIQUE (turn_id, position),
    UNIQUE (id, role)
);

-- timestamptz preserves the instant, but not the offset the user actually saw.
CREATE TABLE memory.message_clocks (
    message_id bigint PRIMARY KEY,
    role memory.message_role NOT NULL DEFAULT 'user' CHECK (role = 'user'),
    instant timestamptz NOT NULL,
    utc_offset_seconds integer NOT NULL CHECK (utc_offset_seconds BETWEEN -86399 AND 86399),
    since_previous_ns bigint CHECK (since_previous_ns >= 0),
    FOREIGN KEY (message_id, role) REFERENCES memory.messages (id, role) ON DELETE CASCADE
);

CREATE TABLE memory.scheduled_deliveries (
    message_id bigint PRIMARY KEY,
    role memory.message_role NOT NULL DEFAULT 'assistant' CHECK (role = 'assistant'),
    job_id text NOT NULL,
    run_id text NOT NULL,
    channel text NOT NULL,
    chat_id text NOT NULL,
    delivered_at timestamptz NOT NULL,
    FOREIGN KEY (message_id, role) REFERENCES memory.messages (id, role) ON DELETE CASCADE,
    UNIQUE (job_id, run_id, channel, chat_id)
);

CREATE TABLE memory.parts (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    message_id bigint NOT NULL REFERENCES memory.messages (id) ON DELETE CASCADE,
    position integer NOT NULL CHECK (position >= 0),
    kind memory.part_kind NOT NULL,
    UNIQUE (message_id, position),
    UNIQUE (id, kind)
);

-- PostgreSQL text and jsonb reject NUL. Exceptional historical payloads use
-- bytea instead of silently changing the original tool output or arguments.
CREATE TABLE memory.part_text (
    part_id bigint PRIMARY KEY,
    kind memory.part_kind NOT NULL DEFAULT 'text' CHECK (kind = 'text'),
    body text,
    raw_body bytea,
    disposition text CHECK (disposition IN ('commentary', 'final')),
    CHECK ((body IS NULL) <> (raw_body IS NULL)),
    FOREIGN KEY (part_id, kind) REFERENCES memory.parts (id, kind) ON DELETE CASCADE
);

CREATE TABLE memory.part_reasoning (
    part_id bigint PRIMARY KEY,
    kind memory.part_kind NOT NULL DEFAULT 'reasoning' CHECK (kind = 'reasoning'),
    body text,
    raw_body bytea,
    replay jsonb,
    raw_replay bytea,
    CHECK ((body IS NULL) <> (raw_body IS NULL)),
    CHECK (replay IS NULL OR raw_replay IS NULL),
    FOREIGN KEY (part_id, kind) REFERENCES memory.parts (id, kind) ON DELETE CASCADE
);

CREATE TABLE memory.part_tool_calls (
    part_id bigint PRIMARY KEY,
    kind memory.part_kind NOT NULL DEFAULT 'tool_call' CHECK (kind = 'tool_call'),
    call_id text NOT NULL,
    tool_name text NOT NULL,
    arguments jsonb,
    raw_arguments bytea,
    CHECK ((arguments IS NULL) <> (raw_arguments IS NULL)),
    FOREIGN KEY (part_id, kind) REFERENCES memory.parts (id, kind) ON DELETE CASCADE
);
CREATE INDEX tool_calls_call_id ON memory.part_tool_calls (call_id);

CREATE TABLE memory.part_tool_results (
    part_id bigint PRIMARY KEY,
    kind memory.part_kind NOT NULL DEFAULT 'tool_result' CHECK (kind = 'tool_result'),
    call_id text NOT NULL,
    body text,
    raw_body bytea,
    is_error boolean NOT NULL,
    CHECK ((body IS NULL) <> (raw_body IS NULL)),
    FOREIGN KEY (part_id, kind) REFERENCES memory.parts (id, kind) ON DELETE CASCADE
);
CREATE INDEX tool_results_call_id ON memory.part_tool_results (call_id);

CREATE TABLE memory.part_media (
    part_id bigint PRIMARY KEY,
    kind memory.part_kind NOT NULL DEFAULT 'media' CHECK (kind = 'media'),
    media_kind memory.media_kind NOT NULL,
    media_ref text CHECK (media_ref <> ''),
    data_url text CHECK (data_url <> ''),
    CHECK (media_ref IS NOT NULL OR data_url IS NOT NULL),
    CHECK (data_url IS NULL OR media_kind = 'image'),
    FOREIGN KEY (part_id, kind) REFERENCES memory.parts (id, kind) ON DELETE CASCADE
);

CREATE TABLE memory.generation_models (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    provider text NOT NULL CHECK (provider <> ''),
    model text NOT NULL CHECK (model <> ''),
    UNIQUE (provider, model)
);

CREATE TABLE memory.turn_digests (
    turn_id bigint PRIMARY KEY REFERENCES memory.turns (id) ON DELETE CASCADE,
    body text NOT NULL CHECK (btrim(body) <> ''),
    model_id bigint NOT NULL REFERENCES memory.generation_models (id),
    prompt_version text NOT NULL CHECK (prompt_version <> ''),
    input_hash bytea NOT NULL CHECK (octet_length(input_hash) = 32),
    input_tokens bigint NOT NULL CHECK (input_tokens >= 0),
    output_tokens bigint NOT NULL CHECK (output_tokens >= 0),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

-- Record exactly which earlier turns were supplied, including holes and
-- deletions. A deleted source leaves an explicit missing dependency.
CREATE TABLE memory.digest_context (
    digest_turn_id bigint NOT NULL REFERENCES memory.turn_digests (turn_id) ON DELETE CASCADE,
    position integer NOT NULL CHECK (position >= 0),
    context_turn_id bigint REFERENCES memory.turns (id) ON DELETE SET NULL,
    PRIMARY KEY (digest_turn_id, position),
    CHECK (context_turn_id IS NULL OR context_turn_id < digest_turn_id)
);

CREATE INDEX digest_context_source ON memory.digest_context (
    context_turn_id
) WHERE context_turn_id IS NOT NULL;

CREATE TABLE memory.digest_failures (
    turn_id bigint PRIMARY KEY REFERENCES memory.turns (id) ON DELETE CASCADE,
    attempts integer NOT NULL CHECK (attempts > 0),
    next_attempt_at timestamptz NOT NULL
);

CREATE TABLE memory.chunk_runs (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    part_id bigint NOT NULL UNIQUE REFERENCES memory.parts (id) ON DELETE CASCADE,
    chunker_version text NOT NULL CHECK (chunker_version <> ''),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE memory.chunks (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    run_id bigint NOT NULL REFERENCES memory.chunk_runs (id) ON DELETE CASCADE,
    position integer NOT NULL CHECK (position >= 0),
    body text NOT NULL CHECK (btrim(body) <> ''),
    UNIQUE (run_id, position)
);
CREATE INDEX chunks_lexical ON memory.chunks USING gin (to_tsvector('simple', body));
CREATE INDEX digests_lexical ON memory.turn_digests USING gin (to_tsvector('simple', body));

CREATE TABLE memory.embedding_models (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    provider text NOT NULL CHECK (provider <> ''),
    model text NOT NULL CHECK (model <> ''),
    dimensions integer NOT NULL CHECK (dimensions BETWEEN 1 AND 2000),
    prefix_version text NOT NULL CHECK (prefix_version <> ''),
    UNIQUE (provider, model, dimensions, prefix_version),
    UNIQUE (id, dimensions)
);

CREATE TABLE memory.chunk_embeddings (
    chunk_id bigint NOT NULL REFERENCES memory.chunks (id) ON DELETE CASCADE,
    model_id bigint NOT NULL,
    embedding vector NOT NULL,
    dimensions integer GENERATED ALWAYS AS (vector_dims(embedding)) STORED NOT NULL,
    PRIMARY KEY (chunk_id, model_id),
    FOREIGN KEY (model_id, dimensions) REFERENCES memory.embedding_models (id, dimensions)
);

CREATE TABLE memory.digest_embeddings (
    turn_id bigint NOT NULL REFERENCES memory.turn_digests (turn_id) ON DELETE CASCADE,
    model_id bigint NOT NULL,
    embedding vector NOT NULL,
    dimensions integer GENERATED ALWAYS AS (vector_dims(embedding)) STORED NOT NULL,
    PRIMARY KEY (turn_id, model_id),
    FOREIGN KEY (model_id, dimensions) REFERENCES memory.embedding_models (id, dimensions)
);

CREATE INDEX chunk_embeddings_768 ON memory.chunk_embeddings USING hnsw (
    (embedding::vector(768)) vector_cosine_ops
) WHERE dimensions = 768;
CREATE INDEX digest_embeddings_768 ON memory.digest_embeddings USING hnsw (
    (embedding::vector(768)) vector_cosine_ops
) WHERE dimensions = 768;
CREATE INDEX chunk_embeddings_1024 ON memory.chunk_embeddings USING hnsw (
    (embedding::vector(1024)) vector_cosine_ops
) WHERE dimensions = 1024;
CREATE INDEX digest_embeddings_1024 ON memory.digest_embeddings USING hnsw (
    (embedding::vector(1024)) vector_cosine_ops
) WHERE dimensions = 1024;

-- +goose StatementBegin
CREATE FUNCTION memory.check_part_payload() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    target_id bigint;
    target_kind memory.part_kind;
    present boolean;
BEGIN
    IF TG_TABLE_NAME = 'parts' THEN
        target_id := COALESCE(NEW.id, OLD.id);
    ELSE
        target_id := COALESCE(NEW.part_id, OLD.part_id);
    END IF;
    SELECT kind INTO target_kind FROM memory.parts WHERE id = target_id;
    IF NOT FOUND THEN
        RETURN NULL;
    END IF;
    CASE target_kind
        WHEN 'text' THEN
            SELECT EXISTS (SELECT 1 FROM memory.part_text WHERE part_id = target_id) INTO present;
        WHEN 'reasoning' THEN
            SELECT EXISTS (SELECT 1 FROM memory.part_reasoning WHERE part_id = target_id) INTO present;
        WHEN 'tool_call' THEN
            SELECT EXISTS (SELECT 1 FROM memory.part_tool_calls WHERE part_id = target_id) INTO present;
        WHEN 'tool_result' THEN
            SELECT EXISTS (SELECT 1 FROM memory.part_tool_results WHERE part_id = target_id) INTO present;
        WHEN 'media' THEN
            SELECT EXISTS (SELECT 1 FROM memory.part_media WHERE part_id = target_id) INTO present;
    END CASE;
    IF NOT present THEN
        RAISE EXCEPTION 'part % has no % payload', target_id, target_kind USING ERRCODE = '23514';
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE CONSTRAINT TRIGGER part_payload
AFTER INSERT OR UPDATE OR DELETE ON memory.parts
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION memory.check_part_payload();
CREATE CONSTRAINT TRIGGER text_payload
AFTER INSERT OR UPDATE OR DELETE ON memory.part_text
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION memory.check_part_payload();
CREATE CONSTRAINT TRIGGER reasoning_payload
AFTER INSERT OR UPDATE OR DELETE ON memory.part_reasoning
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION memory.check_part_payload();
CREATE CONSTRAINT TRIGGER call_payload
AFTER INSERT OR UPDATE OR DELETE ON memory.part_tool_calls
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION memory.check_part_payload();
CREATE CONSTRAINT TRIGGER result_payload
AFTER INSERT OR UPDATE OR DELETE ON memory.part_tool_results
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION memory.check_part_payload();
CREATE CONSTRAINT TRIGGER media_payload
AFTER INSERT OR UPDATE OR DELETE ON memory.part_media
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION memory.check_part_payload();

-- +goose StatementBegin
CREATE FUNCTION memory.guard_history() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    target_turn bigint;
    target_message bigint;
    target_part bigint;
    finished timestamptz;
BEGIN
    IF TG_OP = 'UPDATE' THEN
        RAISE EXCEPTION 'completed transcript payloads cannot be updated' USING ERRCODE = '23514';
    END IF;
    IF TG_TABLE_NAME = 'messages' THEN
        target_turn := COALESCE(NEW.turn_id, OLD.turn_id);
    ELSIF TG_TABLE_NAME IN ('parts', 'message_clocks', 'scheduled_deliveries') THEN
        target_message := COALESCE(NEW.message_id, OLD.message_id);
        SELECT turn_id INTO target_turn FROM memory.messages WHERE id = target_message;
    ELSE
        target_part := COALESCE(NEW.part_id, OLD.part_id);
        SELECT m.turn_id INTO target_turn FROM memory.parts p
          JOIN memory.messages m ON m.id = p.message_id WHERE p.id = target_part;
    END IF;
    SELECT completed_at INTO finished FROM memory.turns WHERE id = target_turn FOR SHARE;
    IF finished IS NOT NULL THEN
        RAISE EXCEPTION 'turn % is complete; delete the turn to remove its history', target_turn
            USING ERRCODE = '23514';
    END IF;
    IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END;
$$;

CREATE FUNCTION memory.guard_turn() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.completed_at IS NOT NULL OR NEW.completed_at IS NULL
       OR (NEW.id, NEW.agent_id, NEW.created_at) IS DISTINCT FROM (OLD.id, OLD.agent_id, OLD.created_at)
    THEN
        RAISE EXCEPTION 'a turn may only transition from reserved to complete'
            USING ERRCODE = '23514';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM memory.messages WHERE turn_id = NEW.id) THEN
        RAISE EXCEPTION 'a completed turn requires messages' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER immutable_turn
BEFORE UPDATE ON memory.turns
FOR EACH ROW EXECUTE FUNCTION memory.guard_turn();
CREATE TRIGGER immutable_message
BEFORE INSERT OR UPDATE OR DELETE ON memory.messages
FOR EACH ROW EXECUTE FUNCTION memory.guard_history();
CREATE TRIGGER immutable_clock
BEFORE INSERT OR UPDATE OR DELETE ON memory.message_clocks
FOR EACH ROW EXECUTE FUNCTION memory.guard_history();
CREATE TRIGGER immutable_delivery
BEFORE INSERT OR UPDATE OR DELETE ON memory.scheduled_deliveries
FOR EACH ROW EXECUTE FUNCTION memory.guard_history();
CREATE TRIGGER immutable_part
BEFORE INSERT OR UPDATE OR DELETE ON memory.parts
FOR EACH ROW EXECUTE FUNCTION memory.guard_history();
CREATE TRIGGER immutable_text
BEFORE INSERT OR UPDATE OR DELETE ON memory.part_text
FOR EACH ROW EXECUTE FUNCTION memory.guard_history();
CREATE TRIGGER immutable_reasoning
BEFORE INSERT OR UPDATE OR DELETE ON memory.part_reasoning
FOR EACH ROW EXECUTE FUNCTION memory.guard_history();
CREATE TRIGGER immutable_call
BEFORE INSERT OR UPDATE OR DELETE ON memory.part_tool_calls
FOR EACH ROW EXECUTE FUNCTION memory.guard_history();
CREATE TRIGGER immutable_result
BEFORE INSERT OR UPDATE OR DELETE ON memory.part_tool_results
FOR EACH ROW EXECUTE FUNCTION memory.guard_history();
CREATE TRIGGER immutable_media
BEFORE INSERT OR UPDATE OR DELETE ON memory.part_media
FOR EACH ROW EXECUTE FUNCTION memory.guard_history();

CREATE TABLE memory.bootstrap (
    agent_id bigint PRIMARY KEY REFERENCES memory.agents (id) ON DELETE CASCADE,
    source_fingerprint bytea CHECK (octet_length(source_fingerprint) = 32),
    source_head bigint NOT NULL CHECK (source_head >= 0),
    source_turns bigint NOT NULL CHECK (source_turns >= 0),
    verified_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    prepared_at timestamptz,
    embedding_model_id bigint REFERENCES memory.embedding_models (id),
    digest_prompt_version text,
    CHECK (
        (prepared_at IS NULL AND embedding_model_id IS NULL AND digest_prompt_version IS NULL)
        OR (
            prepared_at IS NOT NULL
            AND embedding_model_id IS NOT NULL
            AND digest_prompt_version IS NOT NULL
        )
    )
);

-- +goose Down
DROP SCHEMA memory CASCADE;
