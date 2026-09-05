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
- [agent-config.yaml](/deploy/compose/agent-config.yaml),
  [agent-config.discovery.example.yaml](/deploy/compose/agent-config.discovery.example.yaml),
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
