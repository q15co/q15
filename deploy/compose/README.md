# q15 Compose Examples

This directory contains the checked-in Compose-facing config, policy, and secret templates for q15.

- [docker-compose.image-first.yml](/deploy/compose/docker-compose.image-first.yml) is the canonical
  downstream deployment example. It uses published `ghcr.io/q15co/q15-*` images only, applies one
  synchronized release tag to all services, and mounts persistent storage for `/workspace`,
  `/memory`, `/skills`, `/nix`, `/var/lib/q15/agent`, and `/var/lib/q15/proxy`, plus persistent
  Qdrant storage for embedding collections.
- [release.env.example](/deploy/compose/release.env.example) selects the moving `stable` release or
  one immutable DateVer for updates and rollbacks.
- [docker-compose.yml](/docker-compose.yml) in the repo root is the local-development stack. It
  keeps `build:` enabled and uses a named `q15_workspace` volume for `/workspace`; it is not the
  image-first deployment example for downstream consumers.
- [agent-config.yaml](/deploy/compose/agent-config.yaml) selects the local TEI embeddings backend
  (the `q15-tei` service present in both Compose stacks); see the migration runbook below.
- [agent-config.discovery.example.yaml](/deploy/compose/agent-config.discovery.example.yaml),
  [proxy-policy.yaml](/deploy/compose/proxy-policy.yaml), and
  [secrets/\*.example](/deploy/compose/secrets) are generic templates that downstream repos can copy
  or adapt.
- [auth/auth.json.example](/deploy/compose/auth/auth.json.example) is the OpenAI OAuth credential
  template. Mount the auth directory, not the `auth.json` file, so atomic credential refreshes and
  re-authentication updates remain visible to a running container.

For a long-running image-first deployment:

```bash
make compose-secrets-init
cp deploy/compose/release.env.example deploy/compose/release.env
docker compose --env-file deploy/compose/release.env \
  -f deploy/compose/docker-compose.image-first.yml up -d --wait
```

Notes:

- `stable` is updated on `q15-agent`, `q15-exec`, and `q15-proxy` only after the same immutable
  DateVer has been published and verified on all three packages.
- Pull `stable` only after the publish workflow succeeds, so all three moving tags have advanced.
- Set `Q15_IMAGE_TAG` to an immutable `YYYY.MM.DD.<run-number>` DateVer when pinning or rolling back
  a deployment. The same tag always selects one compatible three-image release.
- `/workspace` is expected to persist long-term for one stack. It may be empty on first startup.
- `/memory` should also persist across updates. `q15-agent` eagerly upgrades stored turn history to
  the latest transcript schema on startup.
- `/var/lib/q15/agent` is agent-owned durable runtime state. Scheduled-job definitions and run
  provenance live under `/var/lib/q15/agent/schedule/` and must persist across updates.
- Scheduled-job tool access is stored per job: the main agent selects the job's `allowed_tools` at
  creation or update time. It is not configured as a deployment-wide allow-list.
- Compose health checks gate the executor on the proxy and the agent on the executor and Qdrant.
  Keep `--wait` in deployment commands so a successful update means the whole stack is ready.
- `/etc/q15/auth` must be a writable directory mount containing `auth.json`. A single-file
  `auth.json` bind mount can keep pointing at an old inode after `q15-auth login` atomically
  replaces the host file.
- In `agent-config.yaml`, provider discovery is mandatory: provider rosters are the source of truth
  for available models and capabilities. `agent.provider` + `agent.model` seed the current
  interactive provider/model pair; q15 can list and switch live models at runtime, and otherwise
  tries the current model first each turn before falling through to other eligible roster models.
  `agent.cognition_model` is an optional current model ref for background cognition jobs; when
  omitted, cognition inherits `agent.model`.
- The checked-in Compose agent config enables Brave Search with
  `agent.tools.web_search.brave_api_key_env: BRAVE_API_KEY`, and the Compose file mounts that
  optional secret as `BRAVE_API_KEY_FILE=/run/q15-secrets/brave_api_key`.
- The checked-in Compose agent config enables typed embedding tools with Qdrant and Gemini. Source
  registry and JSONL sync state live under `/workspace/.q15/embed/`; library books remain opt-in via
  `embed_sources add` with `source_type: chunked_markdown_tree`.
- The checked-in Compose config reads the Telegram allow-list from `Q15_TELEGRAM_ALLOWED_USER_IDS`
  or `Q15_TELEGRAM_ALLOWED_USER_IDS_FILE`, so local user IDs stay out of tracked YAML.
- Update by pulling `stable`, or roll back by selecting an earlier DateVer, while preserving the
  persistent volumes.
- GHCR runtime images are intended to be publicly pullable without registry auth for normal
  self-hosted consumption. Maintain the package visibility for these GHCR packages as public outside
  this repo.

## Self-managed model discovery

Provider discovery is **mandatory**: the provider roster IS the model config. There is no hand-typed
`models:` list. The agent queries each provider's roster endpoint at startup (Ollama `/api/tags` +
`/api/show`, OpenAI-compatible `/v1/models`), enriches models from [models.dev](https://models.dev),
and refreshes periodically so roster changes are picked up without restart.

`agent.name` and `agent.memory_recent_turns` are the only agent fields. The current model is runtime
state, not config: on first run q15 auto-selects a first-eligible roster model (preferring a
tool-calling model) and persists it; the agent/user then changes it with `list_providers`,
`list_models`, `switch_model`, and `switch_cognition_model`. Background cognition jobs inherit the
interactive model unless `switch_cognition_model` sets a per-job override. Each path tries its model
first, then falls through to other eligible roster models if it's unavailable.

```yaml
providers:
  - name: ollama-cloud
    type: ollama
    base_url: https://ollama.com
    key_env: OLLAMA_API_KEY
    discovery:
      models_dev: true
      exclude: ["*-embed"]
agent:
  name: Q15
  memory_recent_turns: 6
  ...
```

A provider that is unreachable at startup contributes nothing to the roster (the agent starts with
the remaining providers). If the total roster is empty, startup fails. A model that leaves the
roster (deprecated) stops being selected.

See [agent-config.discovery.example.yaml](/deploy/compose/agent-config.discovery.example.yaml) for a
working example with include/exclude glob filters.

## Embeddings backend (TEI)

Both Compose stacks run a local TEI (Hugging Face Text Embeddings Inference) container, `q15-tei`,
and [agent-config.yaml](/deploy/compose/agent-config.yaml) selects the `openai` embedding provider
with `base_url_env: Q15_EMBEDDINGS_BASE_URL`. TEI serves no request authentication by default, so
the agent runs keyless against it. Default model: `Qwen/Qwen3-Embedding-0.6B` served as
`qwen3-embedding-0.6b` at 1024 dimensions.

```bash
make compose-up   # local development stack (builds q15 images, pulls the TEI image)
make compose-logs SERVICE=q15-tei
```

Notes:

- `q15-tei` needs the NVIDIA container toolkit. The default `turing-1.9` image targets Turing GPUs
  (T4, RTX 2000 series, such as an RTX 2060) and is experimental with Flash Attention off. Ampere or
  newer: set `TEI_IMAGE=ghcr.io/huggingface/text-embeddings-inference:1.9`. CPU-only hosts:
  `cpu-1.9` and remove the `deploy.resources` GPU reservation.
- `TEI_MODEL`, `TEI_SERVED_MODEL_NAME`, and `TEI_MAX_CLIENT_BATCH_SIZE` override the served model;
  the served name must match `model` in agent-config.yaml, and `TEI_MAX_CLIENT_BATCH_SIZE` must stay
  \>= the config's `batch_size`.
- Model weights (~1.2 GB for the default model) download into the `q15_tei_hf_cache` volume on first
  start. Gated or private models need `HF_TOKEN` on the `q15-tei` container.
- TEI is reachable only on the Compose network. If you expose it, run TEI with `--api-key` and set
  `api_key_env: Q15_EMBEDDINGS_API_KEY` in agent-config.yaml (plus the matching environment entry)
  so the agent sends the Bearer token.
- The hosted alternative stays two lines away: set `provider: gemini` with `gemini_api_key_env`,
  model `gemini-embedding-2`, dimensions 768, and wire the `gemini_api_key` secret back into the
  Compose file (see [secrets/gemini_api_key.example](/deploy/compose/secrets)).

## Migrating from Gemini to TEI Qwen

Any provider, model, or dimensions change invalidates the embedding sync state: the vector-version
stamp changes from the pre-provider `dense:gemini-embedding-2:768` to
`dense:openai:qwen3-embedding-0.6b:1024`, marking every stored record dirty. Collections also
recreate automatically: on the next sync the service detects the incompatible 768-dimension
collections and recreates them at 1024 dimensions.

1. Deploy the updated stack: `agent-config.yaml` and the Compose files change together (the
   `q15-tei` service and the `Q15_EMBEDDINGS_BASE_URL` entry are required by the config).
1. First start: TEI downloads the model weights into `q15_tei_hf_cache` (~1.2 GB for the default
   model), then reports ready.
1. Run `embed_sync` with `full: true`. Collections recreate at 1024 dimensions automatically and
   every chunk re-embeds locally at no API cost.
1. Spot-check `embed_search` results before relying on them.

Optional: prune duplicate ingestion sources first (for example overlapping `library-chunks`
sources); the re-embed cost is roughly proportional to the unique chunk count.

Gemini stays available as a provider: switching back is the same two-line config change, and the
same stamp invalidation plus automatic collection recreation applies in reverse.
