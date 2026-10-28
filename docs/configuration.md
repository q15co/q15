# Configuring q15

The config surface, the canonical storage contract, and the two policy files a stack mounts: agent
config and proxy policy.

## Config Strategy

q15 uses a narrow container-first runtime contract:

- agent config is a mounted YAML file at `/etc/q15/agent/config.yaml`
- proxy policy is a mounted YAML file at `/etc/q15/proxy/policy.yaml`
- auth credentials are a JSON file at `/etc/q15/auth/auth.json` within a mounted auth directory
- provider, Brave, Gemini, Telegram, and proxy secrets come from env vars or `_FILE`
- service topology, ports, and runtime directories are hard-coded

The fixed runtime contract is:

- `q15-agent`
  - reads `/etc/q15/agent/config.yaml`
  - reads `/etc/q15/auth/auth.json`
  - uses `/workspace`, `/memory`, and `/skills`
  - connects to `q15-exec:50051`
  - connects to Qdrant only when embedding tools are configured
- `q15-exec`
  - listens on `:50051`
  - talks to `q15-proxy:50052`
  - uses `/workspace`, `/memory`, and `/skills`
- `q15-proxy`
  - reads `/etc/q15/proxy/policy.yaml`
  - listens on `:50052` and `:18080`
  - advertises `http://q15-proxy:18080`
  - uses `/var/lib/q15/proxy`
- `q15-qdrant`
  - stores dense and sparse embedding collections for `library`, `zettelkasten`, `semantic`, and
    `core`
  - uses persistent Qdrant storage

`q15-exec` intentionally passes `--option build-users-group ""` to `nix shell`. That keeps Nix
builds on the current container user, which is expected to be `root` in the current runtime images,
because common container seccomp defaults can block `execve` after Nix drops to `nixbld` users. The
security boundary for those builds is the container or Kubernetes namespace, not Nix `nixbld` user
isolation inside the container.

This repo no longer treats runtime wiring as a user-facing config surface.

`/workspace` is the durable working state for a q15 stack. It is expected to preserve project files
and in-progress work across restarts and redeployments, and a new deployment may start with an empty
`/workspace`. Pre-populating `/workspace` before first startup is optional, not required.

## Storage Contract (Canonical)

This table is the canonical runtime storage/config/secret contract across Kubernetes, Compose, and
local development. Use this as the source of truth when deciding what must persist, what can be
ephemeral, and what must be provided as config/secret input.

| Runtime path or config/secret location    | Classification | Deployment expectations                                                                                                                                                                                                                                                                                                                                                                                                                                                                               | Owning service                    | Notes/purpose                                                                                                                                                                              |
| ----------------------------------------- | -------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `/workspace`                              | Persistent     | **Kubernetes:** required PVC (`q15-workspace`). **Compose:** required mount (bind or named volume). **Local development:** may start empty and be populated later.                                                                                                                                                                                                                                                                                                                                    | shared (`q15-agent`, `q15-exec`)  | Durable project tree and working files; long-lived stack state. Empty initial state is valid.                                                                                              |
| `/workspace/.q15/embed/sources.json`      | Persistent     | Lives inside `/workspace`; created by `embed_sources`.                                                                                                                                                                                                                                                                                                                                                                                                                                                | `q15-agent`                       | Typed embedding source registry. Sources declare `collection`, `source_type`, and `path`; scanner behavior is never inferred from collection names.                                        |
| `/workspace/.q15/embed/state.jsonl`       | Persistent     | Lives inside `/workspace`; created by `embed_sync`.                                                                                                                                                                                                                                                                                                                                                                                                                                                   | `q15-agent`                       | Dirty-tracking and pruning state for embedding syncs.                                                                                                                                      |
| `/memory`                                 | Persistent     | **Kubernetes:** required PVC (`q15-memory`). **Compose:** required named volume or equivalent mount. **Local development:** persistence is recommended for continuity, but disposable runs are possible.                                                                                                                                                                                                                                                                                              | shared (`q15-agent`, `q15-exec`)  | Authored identity under `core/` and ordinary notes. Conversation state and derived search data live in Postgres.                                                                           |
| `/skills`                                 | Persistent     | **Kubernetes:** required PVC (`q15-skills`). **Compose:** required named volume or equivalent mount. **Local development:** may be reset, but persistence avoids repeated skill bootstrap.                                                                                                                                                                                                                                                                                                            | shared (`q15-agent`, `q15-exec`)  | Installed skill artifacts and related runtime data.                                                                                                                                        |
| `/nix`                                    | Persistent     | **Kubernetes:** required PVC (`q15-exec-nix`). Fresh PVCs must preserve the image-provided bootstrap Nix runtime on first mount, because an empty `/nix` hides the image's own shell, cert bundle, profiles, and `nix` binary. **Compose:** required named volume or equivalent mount for long-running stacks. Docker named volumes copy up the image's `/nix` contents on first use. **Local development:** disposable runs may use ephemeral state, but persistent reuse is the normal expectation. | `q15-exec`                        | Intentionally persistent executor package/store state and fetched store paths; not scratch space.                                                                                          |
| `/var/lib/q15/agent`                      | Persistent     | **Kubernetes:** required PVC (`q15-agent-state`). **Compose:** required named volume (`q15_agent_state`) or equivalent mount. **Local development:** persistence is required when scheduled jobs must survive container replacement.                                                                                                                                                                                                                                                                  | `q15-agent`                       | Agent-owned operational state outside Git-backed memory. Scheduled definitions and run records live under `schedule/`; this volume is not mounted into `q15-exec`.                         |
| `/var/lib/q15/proxy`                      | Persistent     | **Kubernetes:** required PVC (`q15-proxy-state`). **Compose:** required named volume or equivalent mount for durable proxy state. **Local development:** may be ephemeral only when proxy state durability is intentionally not needed.                                                                                                                                                                                                                                                               | `q15-proxy`                       | Proxy-owned durable state directory.                                                                                                                                                       |
| `/var/lib/q15-web`                        | Persistent     | **Compose:** required `q15_web_state` volume, mounted only by `q15-web`; image-owned UID 65532/GID 1000, mode 0700. Preserve across updates. **Kubernetes:** not implemented.                                                                                                                                                                                                                                                                                                                         | `q15-web`                         | Owner WebAuthn public credentials, counters, hashed bounded sessions, session public keys and bounded proof-reuse records. Private admin Unix socket; never mount this state in the agent. |
| Qdrant storage                            | Persistent     | **Compose:** required named volume (`q15_qdrant_storage`) for embedding collections.                                                                                                                                                                                                                                                                                                                                                                                                                  | `q15-qdrant`                      | Dense Gemini vectors plus Qdrant BM25 sparse vectors for opt-in typed sources.                                                                                                             |
| `/etc/q15/agent/config.yaml`              | Config         | **Kubernetes:** required via `ConfigMap/q15-agent-config`. **Compose:** required via compose `configs` or bind mount. **Local development:** required when running `q15-agent`.                                                                                                                                                                                                                                                                                                                       | `q15-agent`                       | Structured agent config (providers, Telegram policy).                                                                                                                                      |
| `/etc/q15/proxy/policy.yaml`              | Config         | **Kubernetes:** required via `ConfigMap/q15-proxy-policy`. **Compose:** required via compose `configs` or bind mount. **Local development:** required when running `q15-proxy`.                                                                                                                                                                                                                                                                                                                       | `q15-proxy`                       | Structured proxy policy (rules, allowed secret aliases, request env mapping).                                                                                                              |
| `/etc/q15/auth/auth.json`                 | Secret         | **Kubernetes:** required via `Secret/q15-agent-auth` (`auth.json` key). **Compose:** required writable directory mount at `/etc/q15/auth` containing `auth.json`; do not bind-mount only the file. **Local development:** may be omitted only when auth-dependent flows are intentionally not used.                                                                                                                                                                                                   | `q15-agent`                       | Auth bootstrap output from `q15 auth login`; consumed at runtime by the agent and updated when OpenAI OAuth refreshes rotate credentials.                                                  |
| Provider/API key secrets (env or `_FILE`) | Secret         | **Kubernetes:** required secret keys in `q15-agent-env` and/or `q15-proxy-env` based on configured providers and policy aliases. **Compose:** required Docker secrets or env files for enabled integrations. **Local development:** optional only for integrations you are not using.                                                                                                                                                                                                                 | shared (`q15-agent`, `q15-proxy`) | Includes provider keys, Telegram token, Brave key (optional), Gemini key (optional), and proxy secret aliases resolved from env or `_FILE`.                                                |

The authored-memory mount is writable in `q15-agent` and read-only in `q15-exec`. File tools
serialize mutations through the repository lease. Postgres is separately persistent and required:
configure `Q15_DATABASE_URL` / `Q15_DATABASE_URL_FILE`, or `PGHOST`, `PGUSER`, `PGDATABASE`,
`PGPASSWORD` / `PGPASSWORD_FILE` and `PGSSLMODE`. The stack uses one shared database password
secret. Connection strings must not be included in logs or command arguments.

### Ephemeral/Scratch Paths

There are currently no contract-required explicit ephemeral mounts.

Transient data naturally lives on each container's writable filesystem layer by default. Do not
downgrade any contract-required persistent paths (`/workspace`, `/memory`, `/skills`, `/nix`,
`/var/lib/q15/agent`, `/var/lib/q15/proxy`) to ephemeral mounts (`emptyDir`, anonymous volumes, or
temporary bind locations) in long-running deployments.

### Agent Config

Keep the agent file focused on identity, providers, the current model refs, optional tools, and
Telegram policy. Provider discovery is mandatory: there is no hand-written `models:` list.

Example:

```yaml
providers:
  - name: openai
    type: openai-codex

  - name: moonshot
    type: openai-compatible
    base_url: https://api.moonshot.ai/v1
    key_env: MOONSHOT_API_KEY
    discovery:
      models_dev: true

  - name: ollama-local
    type: ollama
    discovery:
      models_dev: true

  - name: ollama-cloud
    type: ollama
    base_url: https://ollama.com
    key_env: OLLAMA_API_KEY
    discovery:
      models_dev: true

agent:
  name: Q15
  history:
    database_url_env: Q15_DATABASE_URL
    embedding_provider: ollama
    embedding_base_url: http://q15-ollama:11434
    embedding_model: embeddinggemma-2:740m
    embedding_dimensions: 768
    embedding_batch_size: 8
    embedding_batch_tokens: 8192
    summary_provider: ollama-cloud
    summary_model: minimax-m3.1-flash
    working_budget_tokens: 64000
    context_window_fraction: 0.25
    recent_history_fraction: 0.3333333333333333
    max_request_bytes: 4194304
    recent_turns: 4
  tools:
    web_search:
      brave_api_key_env: BRAVE_API_KEY
      ollama_api_key_env: OLLAMA_API_KEY
    embeddings:
      qdrant_url_env: Q15_QDRANT_URL
      provider: openai
      base_url_env: Q15_EMBEDDINGS_BASE_URL
      model: qwen3-embedding:0.6b
      dimensions: 1024
      batch_size: 128
      # hosted alternative: provider: gemini + gemini_api_key_env,
      # model gemini-embedding-2, dimensions 768
    schedule:
      max_jobs: 64
      max_run_turns: 16
  telegram:
    token_env: Q15_TELEGRAM_TOKEN
    allowed_user_ids_env: Q15_TELEGRAM_ALLOWED_USER_IDS
```

Notes:

- provider keys, Brave API keys, Telegram tokens, and Telegram allowed-user lists can come from
  `NAME` or `NAME_FILE`

- `openai-codex` uses `/etc/q15/auth/auth.json` from `q15 auth login`; `openai-compatible` providers
  use `key_env`

- `openai-compatible` `base_url` must be the provider's documented HTTP(S) API root. Include the
  versioned path when required, such as `https://api.deepinfra.com/v1/openai`. Providers such as
  DeepSeek serve the API at a pathless root. Chat and discovery join relative paths onto this value
  (`chat/completions` and `models`); startup discovery reports an unroutable endpoint through
  `list_providers`.

- `ollama` defaults to local `http://localhost:11434` when `base_url` is omitted; set
  `base_url: https://ollama.com` plus `key_env: OLLAMA_API_KEY` for direct Ollama Cloud API access

- TEI embedding servers continue to serve `agent.tools.embeddings` with `provider: openai`, their
  service root in `base_url_env` (for example, `http://q15-tei:80`), and their exact served model ID
  in `model`. The embedding tool owns this fixed model and transport together; provider discovery
  and chat/decision switches do not change them. Existing TEI deployments need no new provider
  declaration or collection re-embedding for this change.

- For `openai-compatible` providers, omit `key_env` for a local unauthenticated endpoint (loopback,
  private or link-local address, or single-label service name). Public hosts require `key_env`. A
  named secret must resolve even for a local service.

- Interactive and decision model selections are runtime state in
  `/workspace/.q15/agent/selection.json`, with no YAML model assignments. At startup a persisted,
  roster-valid selection is restored; an empty or stale slot auto-selects an eligible live model and
  persists the choice. Interactive selection prefers text plus tool calling. `list_providers`
  exposes both slots under `role_selections`.

- Each operation checks its effective capability at dispatch: chat requires `chat` and decisions
  require `decision`. Text or image input does not imply chat generation. A decision-only model
  cannot become an interactive model or chat fallback. A provider model genuinely supporting both
  operations may be selected for both; selecting it for one operation does not disable the other.
  Chat falls back through chat-capable models in live roster order, current model first.

- `decision_check` uses the decision selection. Without one, it reports "role decision: no model
  selected; use switch_model with slot decision". Incoming `type: decision` metadata normalizes into
  the capability with provenance.

- `switch_model` accepts `slot: interactive` (default) or `slot: decision`, validates the required
  capability, and persists the selection before publishing the change. Embedding model changes
  require an explicit collection re-embedding migration; there is no general `embed` switching slot.
  Until the provider-registry migration in #376, embedding model and transport settings stay in
  `agent.tools.embeddings`. Do not change these settings against existing vectors without rebuilding
  the affected collections.

- A dedicated [System One compatible provider](https://docs.typesafe.ai/models) can use
  `type: systemone-compatible`, `base_url: https://api.typesafe.ai/v1`, and
  `key_env: TYPESAFE_API_KEY`. Its `models` array or OpenAI-style `data` array supplies the live
  decision roster. The key is optional for local unauthenticated servers. As with
  `openai-compatible`, the documented API root supplies the path: q15 appends `models` or
  `systemone` without inventing a version segment. `decision_path`, alongside `base_url` and
  `key_env`, overrides the decision endpoint per provider. Compatible roots default to the relative
  `systemone`; Ollama defaults to its native `/v1/systemone`. Relative overrides extend the API
  root; a leading slash replaces its path at the same origin. Full URLs, queries, and fragments are
  refused. For example, `base_url: https://api.solar.example/v2` with `decision_path: /systemone`
  calls `https://api.solar.example/systemone`. Discovery still uses the API root's `models`
  endpoint.

- `decision_check` takes `state` (text, object, or array), `question` (text), and `options` (2-26
  unique strings in preference order). It returns `choice`, `probabilities`, and `confidence`. Ties
  follow option order; confidence measures concentration, not correctness. See the
  [Ollama decision API](https://docs.ollama.com/capabilities/decision).

- With `discovery.models_dev: true`, Providers are matched against the catalog's `api` URL first,
  then a unique provider domain from `api` or `doc`. Ambiguous matches leave metadata unknown. Set
  `discovery.models_dev_provider` to a models.dev provider ID (for example `deepinfra`) when using a
  proxy or an ambiguous endpoint.

- Enrichment requests `https://models.dev/api.json?type=all`: the default models.dev response
  excludes decision models. The catalog only enriches models returned by the configured provider; it
  does not add unavailable models or other providers. Missing metadata remains unknown.

- Provider `capability_overrides` maps native model IDs to capability names and booleans, for
  example `capability_overrides: {model-id: {decision: true, chat: false}}`. Overrides win over
  provider-derived and models.dev-declared evidence, including explicit `false` values.
  `list_models` reports the effective capabilities and their `capability_provenance`.

- `list_models` reports `capabilities_known: false` when capability metadata is unavailable; an
  empty capability list then makes no claim about tool support. `structured_output: null` means that
  support is unknown. Capability filters include confirmed matches only.

- DeepInfra's roster tags identify chat models. Entries tagged only for embeddings, images, audio,
  or video are excluded from the chat roster, even when enrichment is disabled.

- This file is re-read on every roster tick, every 10 minutes. An edited provider list is applied
  without a restart once it loads, its keys resolve, and it discovers at least one model. An edit
  that fails any of those is refused whole: the previous providers keep running, and
  `list_providers` reports why under `config_reload`. A rotated secret file on its own is not a
  change to this file, so it still needs a restart.

- Only the `providers` section reloads; changes to `agent` settings require a restart. Model
  selections are runtime state, so they survive a provider reload and are re-checked against the
  live roster at dispatch.

- For Ollama Cloud, create an API key from the Ollama account settings, expose it as
  `OLLAMA_API_KEY` or `OLLAMA_API_KEY_FILE`, and use native Ollama model names such as
  `gpt-oss:120b`; browse the current cloud model catalog at `https://ollama.com/search`

- `agent.tools.web_search` is optional. Set `brave_api_key_env` and/or `ollama_api_key_env` to
  enable `web_search`; any single provider key is enough. Each name resolves through `NAME` or
  `NAME_FILE`. Requests rotate across the configured providers and are throttled per provider (Brave
  1.1s to respect the free tier's ~1 request/second, Ollama 250ms). A provider that fails three
  consecutive times is skipped for 60s so a dead backend does not cost every other call a full
  client timeout.

- `agent.tools.embeddings` is optional; omit it to disable `embed_sources`, `embed_sync`,
  `embed_search`, and `embed_status`

- `agent.tools.embeddings.provider` selects the embedding backend: `openai` for any
  OpenAI-compatible `/embeddings` endpoint such as TEI, OpenAI, Jina, or Ollama `/v1/embeddings`, or
  `gemini` for hosted Gemini. Omitting `provider` defaults to `gemini`; the checked-in Compose
  stacks set `openai` against the local TEI service

- `provider: openai` resolves `api_key_env` through `NAME` or `NAME_FILE`; setting only
  `base_url_env` targets that base URL without an API key (local unauthenticated endpoints such as
  TEI). `model` and `dimensions` are required for the openai provider

- `batch_size` bounds per-request embedding batches; omit it or set `0` to use the provider default
  (gemini 32, openai 128)

- changing the embeddings provider, model, or dimensions invalidates existing sync state and
  triggers a full dense re-embed; collections recreate automatically at the configured dimensions on
  the next sync. Run `embed_sync` with `full: true` after switching, and see the Gemini-to-TEI
  migration runbook in [deploy/compose/README.md](../deploy/compose/README.md)

- `agent.tools.schedule` controls agent-created scheduled jobs. When omitted it allows up to 64 jobs
  and caps each run at 16 model/tool turns

- each job declares its own `allowed_tools` when it is created or updated. The main agent selects
  only the capabilities required by that job; an empty list runs without tools

- create/update rejects tool names outside the current scheduled-run catalog. If a persisted tool
  later becomes unavailable, the job remains intact but its run fails closed until the tool is
  restored or the job policy is updated; q15 never substitutes another tool

- each job also chooses a context profile: `minimal` (the default) receives trusted run
  identity/timing plus the isolated scheduled task, while `agent` adds the main agent's system
  envelope, core memory, skill catalog, and bounded conversation context. Context selection does not
  add tool capabilities beyond the job's `allowed_tools`

- `schedule_runs` queries owner-scoped archived run records and exact status aggregates. It is the
  source of truth for execution counts; transcript messages and notification text are not

- each scheduled job pins an exact provider/model pair. If that target leaves the live roster, q15
  never falls back to another model: recurring jobs record the unavailable occurrence and continue
  their normal cadence, while one-shot jobs remain active and retry. Unavailable notifications are
  suppressed only after the transport acknowledges delivery; a failed delivery leaves the transition
  open for the next occurrence

- embedding search stores provider dense vectors (Gemini by default) and Qdrant-generated BM25
  sparse vectors, then uses hybrid dense+sparse search by default

- `embed_sources` action `delete_collection` deliberately drops one Qdrant collection and clears
  matching sync state; the next `embed_sync` recreates it from configured sources

- embedding sources are typed records under `/workspace/.q15/embed/sources.json`; use
  `source_type: chunked_markdown_tree` for pre-chunked Markdown libraries such as
  `/workspace/library/<author>/<work>/chunks`

- `chunked_markdown_tree` treats each Markdown file as one embedding unit and loads sibling
  `meta.yml`; EPUB/PDF ingestion is not part of this path

- provider discovery is mandatory; provider rosters are the source of truth for available models and
  capabilities

- interactive model switches are explicit provider/model selections persisted under
  `/workspace/.q15/agent/selection.json`. Use `list_providers`, `list_models`, `switch_model`
  (including `slot: decision`); switches are checked before persistence. The chat fallback chain is
  the live roster in discovery order with the current model first, filtered to chat-capable models.

- conversation context is configured under `agent.history`, independently of library search and
  interactive/decision selection. See
  [Postgres history configuration and cutover](postgres-history-cutover.md). The old cognition jobs,
  per-job model overrides and flat `memory_*` configuration keys are removed.

- current inference is intentionally text-first; image-input and tool-calling requirement inference
  land when canonical request signals for those modes are available

- conversation memory lives in Postgres; authored identity and notes live under `/memory`

- `BRAVE_API_KEY` / `BRAVE_API_KEY_FILE` is the default Brave web-search secret name used by the
  checked-in deployment examples

- `Q15_GEMINI_API_KEY` / `Q15_GEMINI_API_KEY_FILE` is the default Gemini embedding secret name used
  by the checked-in Compose examples

#### Asynchronous sync

`embed_sync` runs as a managed background job and takes `wait_seconds` (default 30, capped at 300).
The call blocks until the sync finishes or the window elapses, and returns the job plus the final
result when completed; otherwise it returns the still-running job snapshot with a note.
`wait_seconds: 0` returns immediately. Jobs run on a context detached from the conversation, so
interrupting a waiting turn (for example with Telegram Stop) never kills the in-flight sync: the
tool call reports the still-running job with a note and the sync continues in the background. Poll
`embed_job` to completion before reporting a sync as done.

`embed_job` inspects or cancels those jobs: `action: status` (optional `job_id`; omit it for the
active job) and `action: cancel` (required `job_id`). It registers alongside the other embedding
tools whenever `agent.tools.embeddings` is configured. Only one sync runs at a time; starting
another fails with an error naming the running job. Progress counters are cumulative across the
whole sync run.

Job tracking is in-memory: only the ten most recent finished jobs are kept, and everything is
forgotten on restart. Durable progress lives in `/workspace/.q15/embed/state.jsonl`: syncs
checkpoint in batches (embed, upsert, record state), so a cancelled or interrupted sync keeps all
completed batches and the next `embed_sync` re-embeds only the remainder.

## Proxy Policy

Keep the proxy file focused on policy and secret-backed request mutation.

The file's schema is defined once, in `libs/proxy-contract/policy`, and both sides of it use that
package: the proxy reads and resolves a policy, and the CLI renders one for a stack from the same
deployment profile it renders the units and the agent config from. The file declares its shape with
`schema_version`; an absent field reads as version 1, and a version this build does not understand
is refused rather than partly applied.

Example:

```yaml
schema_version: 1

proxy:
  no_proxy:
    - localhost
    - 127.0.0.1
    - ::1
    - q15-proxy
    - q15-exec
  set_lowercase_proxy_env: true
  secrets:
    - github_token
  rules:
    - name: github-api
      match_hosts:
        - api.github.com
  env:
    - name: GH_TOKEN
      secret: github_token
      rules:
        - github-api
      in:
        - header
```

Proxy secret aliases resolve from either the uppercased alias env var or its `_FILE` companion.
