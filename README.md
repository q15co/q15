# q15

`q15` is a Telegram-based coding agent platform split across three long-lived runtime services:

- `q15-agent`: the agent runtime
- `q15-exec`: command execution and session lifecycle
- `q15-proxy`: proxy policy and auth-env mediation

Interactive auth bootstrap is handled separately by `q15-auth`.

## Architecture

- The agent owns prompt assembly, tool wiring, Telegram I/O, memory management, and rooted file
  operations.
- `q15-exec` owns command execution and reports the authoritative model-visible runtime directories:
  `/workspace`, `/memory`, and `/skills`.
- `q15-proxy` owns policy, egress, and auth-env injection for exec sessions.
- `q15-auth` is an operator tool that produces `auth.json` outside the runtime containers.

This maps directly onto the intended deployment model: one `q15-agent`, one `q15-exec`, and one
`q15-proxy` running together in Compose or Kubernetes.

In Kubernetes, the supported topology is one namespace per q15 stack. Each stack contains one
`q15-agent`, one `q15-exec`, one `q15-proxy`, stack-local ConfigMaps and Secrets, and stack-owned
persistent volumes for `/workspace`, `/memory`, `/skills`, `/nix`, and `/var/lib/q15/proxy`. The
namespace is the isolation boundary for that stack. Downstream multi-stack deployments should repeat
this stack in separate namespaces.

`/workspace` is not ephemeral scratch space. For each stack it is stack-owned, persistent, and
long-lived state that carries the durable project tree and working files over time. A newly created
stack may attach an empty persistent volume or empty host directory at `/workspace`; that empty
initial state is valid, and operators may populate it later through normal agent work or manual
setup.

## Build And Test

```bash
make build
make test
```

Artifacts are written to `./bin`:

- `q15-agent`
- `q15-auth`
- `q15-exec`
- `q15-proxy`

## Release Artifacts

Runtime services are published as OCI images to GHCR on every `push` to `main`:

- `ghcr.io/q15co/q15-agent`
- `ghcr.io/q15co/q15-exec`
- `ghcr.io/q15co/q15-proxy`

Published runtime tags:

- `main`
- `sha-<short-sha>`

Tag guidance for downstream consumers:

- Use one pinned `sha-<short-sha>` tag across `q15-agent`, `q15-exec`, and `q15-proxy` for
  long-running deployments.
- Treat `main` as a moving integration tag for fast-moving or development consumption, not the
  default for long-lived stacks.
- If release tags are added later, treat them the same way: pin one immutable tag across the whole
  stack.

GHCR runtime images are intended to be publicly pullable without registry auth for ordinary
self-hosted consumption. Maintain the GitHub package visibility for `q15-agent`, `q15-exec`, and
`q15-proxy` as public outside this repo.

`q15-auth` is published separately as GitHub Release archives on pushed tags that match `v*`. The
release assets are:

- `q15-auth_<version>_linux_amd64.tar.gz`
- `q15-auth_<version>_linux_arm64.tar.gz`
- `q15-auth_<version>_darwin_amd64.tar.gz`
- `q15-auth_<version>_darwin_arm64.tar.gz`
- `checksums.txt`

## Config Strategy

q15 uses a narrow container-first runtime contract:

- agent config is a mounted YAML file at `/etc/q15/agent/config.yaml`
- proxy policy is a mounted YAML file at `/etc/q15/proxy/policy.yaml`
- auth credentials are a mounted JSON file at `/etc/q15/auth/auth.json`
- provider, Telegram, and proxy secrets come from env vars or `_FILE`
- service topology, ports, and runtime directories are hard-coded

The fixed runtime contract is:

- `q15-agent`
  - reads `/etc/q15/agent/config.yaml`
  - reads `/etc/q15/auth/auth.json`
  - uses `/workspace`, `/memory`, and `/skills`
  - connects to `q15-exec:50051`
- `q15-exec`
  - listens on `:50051`
  - talks to `q15-proxy:50052`
  - uses `/workspace`, `/memory`, and `/skills`
- `q15-proxy`
  - reads `/etc/q15/proxy/policy.yaml`
  - listens on `:50052` and `:18080`
  - advertises `http://q15-proxy:18080`
  - uses `/var/lib/q15/proxy`

This repo no longer treats runtime wiring as a user-facing config surface.

`/workspace` is the durable working state for a q15 stack. It is expected to preserve project files
and in-progress work across restarts and redeployments, and a new deployment may start with an empty
`/workspace`. Pre-populating `/workspace` before first startup is optional, not required.

## Storage Contract (Canonical)

This table is the canonical runtime storage/config/secret contract across Kubernetes, Compose, and
local development. Use this as the source of truth when deciding what must persist, what can be
ephemeral, and what must be provided as config/secret input.

| Runtime path or config/secret location    | Classification | Deployment expectations                                                                                                                                                                                                                                                                                                                                                                                                                                                                               | Owning service                    | Notes/purpose                                                                                                        |
| ----------------------------------------- | -------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------- | -------------------------------------------------------------------------------------------------------------------- |
| `/workspace`                              | Persistent     | **Kubernetes:** required PVC (`q15-workspace`). **Compose:** required mount (bind or named volume). **Local development:** may start empty and be populated later.                                                                                                                                                                                                                                                                                                                                    | shared (`q15-agent`, `q15-exec`)  | Durable project tree and working files; long-lived stack state. Empty initial state is valid.                        |
| `/memory`                                 | Persistent     | **Kubernetes:** required PVC (`q15-memory`). **Compose:** required named volume or equivalent mount. **Local development:** persistence is recommended for continuity, but disposable runs are possible.                                                                                                                                                                                                                                                                                              | shared (`q15-agent`, `q15-exec`)  | Canonical agent/session memory path across services.                                                                 |
| `/skills`                                 | Persistent     | **Kubernetes:** required PVC (`q15-skills`). **Compose:** required named volume or equivalent mount. **Local development:** may be reset, but persistence avoids repeated skill bootstrap.                                                                                                                                                                                                                                                                                                            | shared (`q15-agent`, `q15-exec`)  | Installed skill artifacts and related runtime data.                                                                  |
| `/nix`                                    | Persistent     | **Kubernetes:** required PVC (`q15-exec-nix`). Fresh PVCs must preserve the image-provided bootstrap Nix runtime on first mount, because an empty `/nix` hides the image's own shell, cert bundle, profiles, and `nix` binary. **Compose:** required named volume or equivalent mount for long-running stacks. Docker named volumes copy up the image's `/nix` contents on first use. **Local development:** disposable runs may use ephemeral state, but persistent reuse is the normal expectation. | `q15-exec`                        | Intentionally persistent executor package/store state and fetched store paths; not scratch space.                    |
| `/var/lib/q15/proxy`                      | Persistent     | **Kubernetes:** required PVC (`q15-proxy-state`). **Compose:** required named volume or equivalent mount for durable proxy state. **Local development:** may be ephemeral only when proxy state durability is intentionally not needed.                                                                                                                                                                                                                                                               | `q15-proxy`                       | Proxy-owned durable state directory.                                                                                 |
| `/etc/q15/agent/config.yaml`              | Config         | **Kubernetes:** required via `ConfigMap/q15-agent-config`. **Compose:** required via compose `configs` or bind mount. **Local development:** required when running `q15-agent`.                                                                                                                                                                                                                                                                                                                       | `q15-agent`                       | Structured agent config (providers, models, Telegram policy).                                                        |
| `/etc/q15/proxy/policy.yaml`              | Config         | **Kubernetes:** required via `ConfigMap/q15-proxy-policy`. **Compose:** required via compose `configs` or bind mount. **Local development:** required when running `q15-proxy`.                                                                                                                                                                                                                                                                                                                       | `q15-proxy`                       | Structured proxy policy (rules, allowed secret aliases, request env mapping).                                        |
| `/etc/q15/auth/auth.json`                 | Secret         | **Kubernetes:** required via `Secret/q15-agent-auth` (`auth.json` key). **Compose:** required file mount or secret. **Local development:** may be omitted only when auth-dependent flows are intentionally not used.                                                                                                                                                                                                                                                                                  | `q15-agent`                       | Auth bootstrap output from `q15-auth`; consumed at runtime by the agent.                                             |
| Provider/API key secrets (env or `_FILE`) | Secret         | **Kubernetes:** required secret keys in `q15-agent-env` and/or `q15-proxy-env` based on configured providers and policy aliases. **Compose:** required Docker secrets or env files for enabled integrations. **Local development:** optional only for integrations you are not using.                                                                                                                                                                                                                 | shared (`q15-agent`, `q15-proxy`) | Includes provider keys, Telegram token, Brave key (optional), and proxy secret aliases resolved from env or `_FILE`. |

### Ephemeral/Scratch Paths

There are currently no contract-required explicit ephemeral mounts.

Transient data naturally lives on each container's writable filesystem layer by default. Do not
downgrade any contract-required persistent paths (`/workspace`, `/memory`, `/skills`, `/nix`,
`/var/lib/q15/proxy`) to ephemeral mounts (`emptyDir`, anonymous volumes, or temporary bind
locations) in long-running deployments.

### Agent Config

Keep the agent file focused on identity, models, providers, and Telegram policy.

Example:

```yaml
providers:
  - name: moonshot
    type: openai-compatible
    base_url: https://api.moonshot.ai/v1
    key_env: MOONSHOT_API_KEY

models:
  - name: kimi-k2.5
    provider: moonshot
    capabilities:
      - text
      - tool_calling
      - reasoning

agent:
  name: Q15
  models:
    - kimi-k2.5
  memory_recent_turns: 6
  telegram:
    token_env: Q15_TELEGRAM_TOKEN
    allowed_user_ids:
      - 123456789
```

Notes:

- provider keys and Telegram tokens can come from `NAME` or `NAME_FILE`
- agent memory lives under `/memory`
- `Q15_BRAVE_API_KEY` remains optional for Brave web search

### Proxy Policy

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

## q15-auth

`q15-auth` is the interactive bootstrap tool for generating and inspecting `auth.json`. It is
intended to run on an operator machine, not inside the runtime containers.

Examples:

```bash
q15-auth login --auth-path ./auth.json
q15-auth status --auth-path ./auth.json
q15-auth logout --auth-path ./auth.json
```

The resulting `auth.json` should be mounted into the `q15-agent` container or stored as a Kubernetes
Secret.

## First Startup Inputs

Before first startup, every long-running q15 stack must provide:

- agent config at `/etc/q15/agent/config.yaml`
- proxy policy at `/etc/q15/proxy/policy.yaml`
- `auth.json` at `/etc/q15/auth/auth.json`
- provider or API secrets required by the chosen agent config
- proxy secret aliases required by the chosen proxy policy
- persistent storage for `/workspace`, `/memory`, `/skills`, `/nix`, and `/var/lib/q15/proxy`

`/workspace` may begin empty, but it is expected to remain attached to the same stack over time.

## Local Docker Compose Stack (Development Only)

The checked-in local-development stack lives at [docker-compose.yml](/docker-compose.yml) and uses
the templates under [deploy/compose](/deploy/compose). It is not the canonical downstream
deployment-consumer example.

Its interface is organized as:

- [deploy/compose/agent-config.yaml](/deploy/compose/agent-config.yaml) for structured agent config
- [deploy/compose/proxy-policy.yaml](/deploy/compose/proxy-policy.yaml) for structured proxy policy
- ignored local Docker secret files under [deploy/compose/secrets](/deploy/compose/secrets), seeded
  from tracked `*.example` templates in that same directory

The local-development stack intentionally:

- keeps local source builds enabled with `build:`
- tags those local builds with the canonical GHCR image names on `:main`
- bind-mounts this repo into `/workspace`
- uses the root `make compose-*` targets for local iteration

Bring it up with:

```bash
make compose-secrets-init
make compose-up
```

The local-development stack uses:

- default container entrypoints with no runtime flags
- Compose `configs` for non-secret YAML files
- a read-only bind mount for `auth.json` at `/etc/q15/auth/auth.json`
- a bind mount of this repo as `/workspace`
- a named volume shared as `/memory`
- a named volume for `/skills`
- a named volume `q15_exec_nix_store` mounted at `/nix` for persistent executor package/store reuse
  across sessions
- Docker secret files under [deploy/compose/secrets](/deploy/compose/secrets)

That `/nix` named volume keeps fetched packages warm across sessions. Docker's named-volume copy-up
behavior also preserves the image's built-in bootstrap Nix runtime on first use, which is why the
local stack can safely keep `/nix` persistent without manually pre-seeding it.

Before using the local-development stack, replace the placeholder values in:

- [deploy/compose/secrets/moonshot_api_key.example](/deploy/compose/secrets/moonshot_api_key.example)
- [deploy/compose/secrets/q15_telegram_token.example](/deploy/compose/secrets/q15_telegram_token.example)
- [deploy/compose/secrets/github_token.example](/deploy/compose/secrets/github_token.example)
- [deploy/compose/secrets/q15_auth_json.example](/deploy/compose/secrets/q15_auth_json.example)

`make compose-secrets-init` copies those templates to ignored local files without the `.example`
suffix. Edit the local copies, not the templates.

Useful commands:

```bash
make compose-down
make compose-logs SERVICE=q15-agent
make compose-ps
```

## Image-First Compose Deployment Example

The canonical downstream image-first Compose example is
[deploy/compose/docker-compose.image-first.yml](/deploy/compose/docker-compose.image-first.yml).
Supporting notes live in [deploy/compose/README.md](/deploy/compose/README.md).

This deployment-oriented example:

- uses `image:` only, with no `build:`
- requires `Q15_IMAGE_TAG` and applies the same tag to `q15-agent`, `q15-exec`, and `q15-proxy`
- mounts persistent named volumes for `/workspace`, `/memory`, `/skills`, `/nix`, and
  `/var/lib/q15/proxy`
- mounts `agent-config.yaml`, `proxy-policy.yaml`, and `auth.json` at the exact runtime paths the
  binaries expect
- mounts provider and proxy secret files and resolves them via the `*_FILE` environment pattern

Bring it up with:

```bash
make compose-secrets-init
Q15_IMAGE_TAG=sha-<short-sha> docker compose -f deploy/compose/docker-compose.image-first.yml up -d
```

`/workspace` may start empty in this example, but it is expected to persist long-term for one stack.
For long-running deployments, keep that same storage attached across restarts and redeployments. The
`/nix` mount is intentionally persistent and should not be treated as scratch space. A Docker named
volume keeps both the bootstrap Nix runtime and the fetched store paths under `/nix`; an empty bind
mount at `/nix` would hide the image-provided shell and `nix` installation instead of just clearing
the cache.

## Kubernetes Base

A reusable Kustomize base lives under [deploy/kubernetes/base](/deploy/kubernetes/base).

It includes:

- Deployments for `q15-agent`, `q15-exec`, and `q15-proxy`
- Services for `q15-exec` and `q15-proxy`
- ConfigMap generators for agent config and proxy policy examples

The canonical runtime images are:

- `ghcr.io/q15co/q15-agent`
- `ghcr.io/q15co/q15-exec`
- `ghcr.io/q15co/q15-proxy`

The checked-in manifests keep the moving GHCR `:main` tags as placeholders:

- `ghcr.io/q15co/q15-agent:main`
- `ghcr.io/q15co/q15-exec:main`
- `ghcr.io/q15co/q15-proxy:main`

It expects a separate deployment repo or overlay to provide:

- image pinning and rollout policy
- environment-specific namespaces and labels
- Secrets
- PVCs

For long-running deployments, replace those placeholder tags with one pinned `sha-<short-sha>` tag
across all three services. Treat `main` as a moving integration tag only.

The supported Kubernetes model is one namespace per long-running q15 stack. One stack contains:

- one `q15-agent`
- one `q15-exec`
- one `q15-proxy`
- stack-local ConfigMaps and Secrets for agent config, proxy policy, `auth.json`, provider or API
  keys, and runtime tokens
- stack-owned persistent volumes for `/workspace`, `/memory`, `/skills`, `/nix`, and
  `/var/lib/q15/proxy`

`q15-workspace` is the durable project and working-state volume for that stack. A newly created
stack may bind an empty `q15-workspace` PVC on first deployment, and that empty initial state still
satisfies the runtime contract. Restarts and redeployments are expected to preserve that PVC so
`/workspace` survives over time.

The checked-in base already reflects that shape with `replicas: 1` for each deployment and
namespace-scoped Services named `q15-exec` and `q15-proxy`. This matches the current exec session
model: `q15-exec` supports multiple concurrent sessions within one pod, but session state is
currently held in memory inside that pod. Keeping one exec pod per stack avoids cross-pod session
routing complexity and keeps storage ownership local to the stack.

The checked-in exec deployment also seeds a fresh `/nix` PVC from the image in an init container
when the mounted volume is missing the bootstrap runtime markers. That one-time bootstrap keeps
Kubernetes PVC-backed `/nix` storage persistent without losing the image's built-in shell, profile,
cert bundle, or `nix` binary on first mount.

Validate the base with:

```bash
kubectl kustomize deploy/kubernetes/base
```

The intended workflow is:

1. This repo publishes `q15-agent`, `q15-exec`, and `q15-proxy` images to GHCR.
1. A separate deployment repo pins those images and owns environment-specific overlays.
1. The deployment repo rolls out the updated pod set.

## Self-Hosted Updates And Rollbacks

For Compose and Kubernetes alike:

- Update by changing the pinned image tag in the deployment repo or `Q15_IMAGE_TAG` and rolling the
  stack.
- Roll back by restoring the previous pinned `sha-<short-sha>` tag across all three services.
- Preserve the existing persistent storage for `/workspace`, `/memory`, `/skills`, `/nix`, and
  `/var/lib/q15/proxy` during normal upgrades and downgrades.
- If release tags are added later, treat them the same way as `sha-*`: pin one immutable tag across
  the full stack and keep rollback history in the deployment repo.
