# Postgres conversation history cutover

This is one cutover. The new runtime reads and writes conversation history only in Postgres. Qdrant
library search keeps its existing collections and embedding model bindings. Authored identity and
skills remain files. All old cognition jobs, including verification review, are removed.

## Database and models

Use a dedicated database for each stack. PostgreSQL 17 with pgvector 0.8.7 is covered by the
integration tests. The existing AGE-enabled container can be used: migrations create only the
`memory` schema, pgvector when absent, and `public.q15_memory_schema_version`. Other schemas and
extensions are left alone. Native full-text search needs no additional extension. Runtime startup
applies versioned, embedded Goose migrations under a Postgres advisory lock; sqlc generates the
typed queries.

Provide `Q15_DATABASE_URL` or `Q15_DATABASE_URL_FILE`. Alternatively, configure `PGHOST`, `PGUSER`,
`PGDATABASE`, `PGPASSWORD` or `PGPASSWORD_FILE`, and `PGSSLMODE`; the shipped container network uses
`disable`, while a remote connection should set its appropriate TLS mode. Compose and Quadlet share
one password secret between Postgres and the agent. Database credentials belong only to the agent
and Postgres, not to exec or web. Back up the database independently of authored files.

The generic local installer profile pins summaries to its initial local Ollama model. Changing the
interactive selection does not change that summary binding. Hosted profiles without a native Ollama
chat provider add an explicit Ollama Cloud summary provider and require its `OLLAMA_API_KEY` secret.
For Hermes and the history backfill, use the existing Cloud subscription and MiniMax binding below.

Example agent settings, alongside an existing `ollama-cloud` provider and its credential:

```yaml
agent:
  name: Q15
  history:
    database_url_env: Q15_DATABASE_URL
    embedding_provider: ollama
    embedding_base_url: http://q15-ollama:11434
    embedding_model: embeddinggemma-2:740m
    embedding_dimensions: 768
    embedding_batch_size: 8
    summary_provider: ollama-cloud
    summary_model: minimax-m3.1-flash
    working_budget_bytes: 262144
    model_budget_bytes:
      ollama-cloud/deepseek-v4.1-flash: 131072
    recent_turns: 8
    horizon_turns: 40
    retrieval_limit: 6
    min_similarity: 0.35
```

The per-model budget above is an example to tune, not a measured quality recommendation. The runtime
uses the actual selected model's catalog context/output limits again on every provider attempt and
tool step. Working budgets bound encoded canonical requests including tool definitions; they are
conservative byte allowances, not exact tokenizer counts. A model's advertised context ceiling does
not establish the point where its recall or reasoning quality degrades. No cloud recall benchmark is
included. Start with a modest working budget and adjust from normal usage.

For existing TEI deployments, `embedding_provider: openai` with an explicit TEI endpoint, model and
dimensions is supported independently of the library binding. A different embedding model, dimension
or input prefix is a different space; backfill and prepare it before switching runtime
configuration. The 768- and 1024-dimensional spaces have partial HNSW indexes. Other supported
widths use exact vector distance. Lexical indexes remain available while embeddings are offline.

Ollama must support the chosen model. EmbeddingGemma 2 requires Ollama 0.40.4 or newer; shipped
Ollama container definitions use 0.40.7. Pull the model explicitly before preparation. Confirm GPU
execution and measure warm query/batch latency on the deployment host before choosing it for
production. A version refusal is not CUDA evidence. Do not change the library's embedding collection
to test conversation embeddings.

## Import and prepare

Build the matching management binary with `make project-setup` and `make build-q15`. Run it where
the frozen source snapshot, database network and required provider credentials are accessible. With
a container deployment this can be a separate management container or an operator shell with
equivalent network access. Do not start a second agent runtime for migration.

To reduce downtime, the bulk import and backfill can first run against a frozen, consistent copy
while the old agent continues serving. Postgres remains a preparation database at that point. At the
final cutover, stop the old agent, take a fresh frozen snapshot, repeat import and verification, and
backfill the remaining turns before preparation. There is still only one serving cutover and no dual
write path. Never import a directory that the old agent is actively changing.

1. Stop the old agent so neither conversations nor old cognition jobs can write. Preserve a backup
   of its complete memory root and database. Take a frozen filesystem snapshot of the legacy root,
   including `history/state/head.json` and every file under `history/turns/`. Keep private snapshots
   outside the source repository. A committed Git archive is also suitable if it includes every
   completed turn and the corresponding head state.

1. Provide the target database credential through the environment or a secret file. Import the
   snapshot with the exact configured agent name:

   ```bash
   q15 memory import --agent Q15 --memory-root /private/frozen-memory
   q15 memory import --agent Q15 --memory-root /private/frozen-memory --verify
   ```

   Import accepts the current legacy schema 5, maps its numeric sequence to the database identity,
   and discards the redundant padded ID. Positions preserve message/part order. It imports each turn
   atomically, verifies every stored turn against the source, rejects conflicting rows or unexpected
   extra completed turns, and checks that the source fingerprint stayed unchanged. Repeating it
   skips identical records; it never overwrites a mismatch. Reserved gaps are retained in the
   identity high-water mark. JSONB values are compared semantically without losing numeric
   precision; timestamps are compared at Postgres microsecond precision, including the original
   local offset. Unsupported text/JSON payloads are retained as bytes. Any mismatch blocks
   preparation.

1. Configure summaries to use the existing Ollama Cloud provider and subscription credential, with
   the explicit `minimax-m3.1-flash` model. It does not inherit or fall back to an interactive
   model. Backfill a small batch first, then finish all history:

   ```bash
   q15 memory backfill --agent Q15 --config /private/new-agent-config.yaml --batches 1
   q15 memory backfill --agent Q15 --config /private/new-agent-config.yaml --prepare
   q15 memory status --agent Q15
   ```

   Each batch saves completed work. Errors and interruptions can be retried with the same command. A
   digest receives only its target turn and up to two earlier turns. Its source excerpts are
   explicitly bounded, and the model has a 2,048-token output cap. Original messages remain
   searchable when summaries omit details. Provider-reported token usage is stored with each digest.
   Subscription usage is governed by [Ollama's current pricing](https://ollama.com/pricing); no free
   introductory period is assumed. Preparation requires all original text chunked, every turn
   summarized with the current prompt version, and all digest/chunk vectors present in the selected
   embedding space.

1. Remove old `memory_recent_turns`, `memory_working_budget_bytes` and
   `memory_open_loops_budget_bytes` configuration keys. Archive the old `working`, `cognition`,
   `history` and extracted semantic layers outside the active authored root after their backup and
   import have been verified. Retain desired authored identity, skills and ordinary notes. The new
   runtime neither loads nor maintains the archived layers.

1. Start the new agent with the prepared database and matching history settings. It refuses an
   unprepared cutover, including an empty database beside an existing legacy head file. Only a
   genuinely fresh stack with no legacy history and no database turns can bootstrap empty. Confirm
   chat history, a new saved reply, an old-history search, and a short follow-up such as “yes”.

The operator owns Hermes deployment. The PR does not restart services or import into its live
Postgres. A downgrade to the file-store runtime cannot consume new Postgres turns; retain the
Postgres database and resolve an application problem forward.

## Temporary migration code

`history/import.go`, `history/json.go`, their source-format tests and the `q15 memory import`
command are temporary migration tooling. Normal runtime persistence, context assembly and search do
not call that path. Remove the importer after the production import is confirmed and the Postgres
runtime is stable. Keep ordinary schema migrations, index maintenance and database backups.

## Validation

`make memory-generate` regenerates typed queries; `make memory-check` checks generated files for
staleness and is included in `make verify`. `make test-history-postgres` starts and removes its own
isolated pgvector container. CI supplies the same Postgres version to `make verify`, so relational
constraints, importer behavior, deletion and indexing tests run against a real database.

For a private full-history verification, set `Q15_IMPORT_SNAPSHOT_ROOT` to a frozen snapshot and
run:

```bash
make test-history-postgres TEST_FLAGS='-run TestImportFrozenSnapshot -v -timeout 20m'
```

The fixture prints counts and timing, never transcript content. Keep that environment variable out
of ordinary shared CI and never commit the snapshot.
