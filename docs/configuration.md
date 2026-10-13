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

| Runtime path or config/secret location    | Classification | Deployment expectations                                                                                                                                                                                                                                                                                                                                                                                                                                                                               | Owning service                    | Notes/purpose                                                                                                                                                                                                                                                                                |
| ----------------------------------------- | -------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `/workspace`                              | Persistent     | **Kubernetes:** required PVC (`q15-workspace`). **Compose:** required mount (bind or named volume). **Local development:** may start empty and be populated later.                                                                                                                                                                                                                                                                                                                                    | shared (`q15-agent`, `q15-exec`)  | Durable project tree and working files; long-lived stack state. Empty initial state is valid.                                                                                                                                                                                                |
| `/workspace/.q15/embed/sources.json`      | Persistent     | Lives inside `/workspace`; created by `embed_sources`.                                                                                                                                                                                                                                                                                                                                                                                                                                                | `q15-agent`                       | Typed embedding source registry. Sources declare `collection`, `source_type`, and `path`; scanner behavior is never inferred from collection names.                                                                                                                                          |
| `/workspace/.q15/embed/state.jsonl`       | Persistent     | Lives inside `/workspace`; created by `embed_sync`.                                                                                                                                                                                                                                                                                                                                                                                                                                                   | `q15-agent`                       | Dirty-tracking and pruning state for embedding syncs.                                                                                                                                                                                                                                        |
| `/memory`                                 | Persistent     | **Kubernetes:** required PVC (`q15-memory`). **Compose:** required named volume or equivalent mount. **Local development:** persistence is recommended for continuity, but disposable runs are possible.                                                                                                                                                                                                                                                                                              | shared (`q15-agent`, `q15-exec`)  | Git-backed agent memory root. Internal layers live under `core/`, `semantic/`, `working/`, `history/`, and `cognition/`; canonical semantic files are `semantic/facts.md`, `semantic/preferences.md`, and `semantic/projects.md`; auxiliary zettelkasten notebook files live under `notes/`. |
| `/skills`                                 | Persistent     | **Kubernetes:** required PVC (`q15-skills`). **Compose:** required named volume or equivalent mount. **Local development:** may be reset, but persistence avoids repeated skill bootstrap.                                                                                                                                                                                                                                                                                                            | shared (`q15-agent`, `q15-exec`)  | Installed skill artifacts and related runtime data.                                                                                                                                                                                                                                          |
| `/nix`                                    | Persistent     | **Kubernetes:** required PVC (`q15-exec-nix`). Fresh PVCs must preserve the image-provided bootstrap Nix runtime on first mount, because an empty `/nix` hides the image's own shell, cert bundle, profiles, and `nix` binary. **Compose:** required named volume or equivalent mount for long-running stacks. Docker named volumes copy up the image's `/nix` contents on first use. **Local development:** disposable runs may use ephemeral state, but persistent reuse is the normal expectation. | `q15-exec`                        | Intentionally persistent executor package/store state and fetched store paths; not scratch space.                                                                                                                                                                                            |
| `/var/lib/q15/agent`                      | Persistent     | **Kubernetes:** required PVC (`q15-agent-state`). **Compose:** required named volume (`q15_agent_state`) or equivalent mount. **Local development:** persistence is required when scheduled jobs must survive container replacement.                                                                                                                                                                                                                                                                  | `q15-agent`                       | Agent-owned operational state outside Git-backed memory. Scheduled definitions and run records live under `schedule/`; this volume is not mounted into `q15-exec`.                                                                                                                           |
| `/var/lib/q15/proxy`                      | Persistent     | **Kubernetes:** required PVC (`q15-proxy-state`). **Compose:** required named volume or equivalent mount for durable proxy state. **Local development:** may be ephemeral only when proxy state durability is intentionally not needed.                                                                                                                                                                                                                                                               | `q15-proxy`                       | Proxy-owned durable state directory.                                                                                                                                                                                                                                                         |
| `/var/lib/q15-web`                        | Persistent     | **Compose:** required `q15_web_state` volume, mounted only by `q15-web`; image-owned UID 65532/GID 1000, mode 0700. Preserve across updates. **Kubernetes:** not implemented.                                                                                                                                                                                                                                                                                                                         | `q15-web`                         | Owner WebAuthn public credentials, counters, hashed bounded sessions, session public keys and bounded proof-reuse records. Private admin Unix socket; never mount this state in the agent.                                                                                                   |
| Qdrant storage                            | Persistent     | **Compose:** required named volume (`q15_qdrant_storage`) for embedding collections.                                                                                                                                                                                                                                                                                                                                                                                                                  | `q15-qdrant`                      | Dense Gemini vectors plus Qdrant BM25 sparse vectors for opt-in typed sources.                                                                                                                                                                                                               |
| `/etc/q15/agent/config.yaml`              | Config         | **Kubernetes:** required via `ConfigMap/q15-agent-config`. **Compose:** required via compose `configs` or bind mount. **Local development:** required when running `q15-agent`.                                                                                                                                                                                                                                                                                                                       | `q15-agent`                       | Structured agent config (providers, models, Telegram policy).                                                                                                                                                                                                                                |
| `/etc/q15/proxy/policy.yaml`              | Config         | **Kubernetes:** required via `ConfigMap/q15-proxy-policy`. **Compose:** required via compose `configs` or bind mount. **Local development:** required when running `q15-proxy`.                                                                                                                                                                                                                                                                                                                       | `q15-proxy`                       | Structured proxy policy (rules, allowed secret aliases, request env mapping).                                                                                                                                                                                                                |
| `/etc/q15/auth/auth.json`                 | Secret         | **Kubernetes:** required via `Secret/q15-agent-auth` (`auth.json` key). **Compose:** required writable directory mount at `/etc/q15/auth` containing `auth.json`; do not bind-mount only the file. **Local development:** may be omitted only when auth-dependent flows are intentionally not used.                                                                                                                                                                                                   | `q15-agent`                       | Auth bootstrap output from `q15 auth login`; consumed at runtime by the agent and updated when OpenAI OAuth refreshes rotate credentials.                                                                                                                                                    |
| Provider/API key secrets (env or `_FILE`) | Secret         | **Kubernetes:** required secret keys in `q15-agent-env` and/or `q15-proxy-env` based on configured providers and policy aliases. **Compose:** required Docker secrets or env files for enabled integrations. **Local development:** optional only for integrations you are not using.                                                                                                                                                                                                                 | shared (`q15-agent`, `q15-proxy`) | Includes provider keys, Telegram token, Brave key (optional), Gemini key (optional), and proxy secret aliases resolved from env or `_FILE`.                                                                                                                                                  |

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
  memory_recent_turns: 6
  tools:
    web_search:
      brave_api_key_env: BRAVE_API_KEY
    embeddings:
      qdrant_url_env: Q15_QDRANT_URL
      provider: openai
      base_url_env: Q15_EMBEDDINGS_BASE_URL
      model: qwen3-embedding-0.6b
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
- `openai-compatible` `base_url` must include the versioned path the provider documents, such as
  `https://api.deepinfra.com/v1/openai`. The chat endpoint and the model roster are both relative
  paths joined onto it (the roster is `{base_url}/models`), so a scheme and host alone is refused at
  load.
- `ollama` defaults to local `http://localhost:11434` when `base_url` is omitted; set
  `base_url: https://ollama.com` plus `key_env: OLLAMA_API_KEY` for direct Ollama Cloud API access
- This file is re-read on every roster tick, every 10 minutes. An edited provider list is applied
  without a restart once it loads, its keys resolve, and it discovers at least one model. An edit
  that fails any of those is refused whole: the previous providers keep running, and
  `list_providers` reports why under `config_reload`. A rotated secret file on its own is not a
  change to this file, so it still needs a restart.
- For Ollama Cloud, create an API key from the Ollama account settings, expose it as
  `OLLAMA_API_KEY` or `OLLAMA_API_KEY_FILE`, and use native Ollama model names such as
  `gpt-oss:120b`; browse the current cloud model catalog at `https://ollama.com/search`
- `agent.tools.web_search.brave_api_key_env` is optional; omit it to disable `web_search`; when it
  is set, q15 resolves that env var through `NAME` or `NAME_FILE`
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
  envelope, core and working memory, skill catalog, and bounded recent conversation. Context
  selection does not add tool capabilities beyond the job's `allowed_tools`
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
- the interactive and cognition models are NOT in config; they are runtime state. On first run q15
  auto-selects a first-eligible roster model (preferring a tool-calling model), persists it under
  `/workspace/.q15/agent/selection.json`, and the agent/user changes it at runtime with
  `list_providers`, `list_models`, `switch_model`, and `switch_cognition_model`. Switches survive
  restart.
- background cognition jobs inherit the interactive model unless `switch_cognition_model` sets a
  per-job override
- current inference is intentionally text-first; image-input and tool-calling requirement inference
  land when canonical request signals for those modes are available
- agent memory lives under `/memory`
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

Example:

```yaml
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
