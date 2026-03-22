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
  name: Jared
  models:
    - kimi-k2.5
  memory_recent_turns: 6
  telegram:
    token_env: JARED_TELEGRAM_TOKEN
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
    - jared_gh_token
  rules:
    - name: github-api
      match_hosts:
        - api.github.com
  env:
    - name: GH_TOKEN
      secret: jared_gh_token
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

## Local Docker Compose Stack

The checked-in Compose stack lives at
[docker-compose.yml](/docker-compose.yml) and uses the
examples under [deploy/compose](/deploy/compose).

Its interface is organized as:

- [deploy/compose/agent-config.yaml](/deploy/compose/agent-config.yaml)
  for structured agent config
- [deploy/compose/proxy-policy.yaml](/deploy/compose/proxy-policy.yaml)
  for structured proxy policy
- ignored local Docker secret files under
  [deploy/compose/secrets](/deploy/compose/secrets),
  seeded from tracked `*.example` templates in that same directory

The Compose file keeps local source builds enabled while tagging them with the canonical GHCR image
names:

- `ghcr.io/q15co/q15-agent:main`
- `ghcr.io/q15co/q15-exec:main`
- `ghcr.io/q15co/q15-proxy:main`

Bring it up with:

```bash
make compose-secrets-init
make compose-up
```

The Compose example uses:

- default container entrypoints with no runtime flags
- Compose `configs` for non-secret YAML files
- a read-only bind mount for `auth.json` at `/etc/q15/auth/auth.json`
- a bind mount of this repo as `/workspace`
- a named volume shared as `/memory`
- a named volume for `/skills`
- Docker secret files under
  [deploy/compose/secrets](/deploy/compose/secrets)

`[docker-compose.yml](/docker-compose.yml)` is a local
development example, so it bind-mounts this repo into `/workspace`. For long-running Compose
deployments, mount stack-owned persistent storage at `/workspace` instead and keep that same storage
attached across restarts. That storage may start as an empty persistent volume or empty host
directory; q15 does not require `/workspace` to be pre-populated before first startup.

Before using the stack for real, replace the placeholder values in:

- [deploy/compose/secrets/moonshot_api_key.example](/deploy/compose/secrets/moonshot_api_key.example)
- [deploy/compose/secrets/jared_telegram_token.example](/deploy/compose/secrets/jared_telegram_token.example)
- [deploy/compose/secrets/jared_gh_token.example](/deploy/compose/secrets/jared_gh_token.example)
- [deploy/compose/secrets/q15_auth_json.example](/deploy/compose/secrets/q15_auth_json.example)

`make compose-secrets-init` copies those templates to ignored local files without the `.example`
suffix. Edit the local copies, not the templates.

Useful commands:

```bash
make compose-down
make compose-logs SERVICE=q15-agent
make compose-ps
```

## Kubernetes Base

A reusable Kustomize base lives under
[deploy/kubernetes/base](/deploy/kubernetes/base).

It includes:

- Deployments for `q15-agent`, `q15-exec`, and `q15-proxy`
- Services for `q15-exec` and `q15-proxy`
- ConfigMap generators for agent config and proxy policy examples

The checked-in manifests default to the moving GHCR `:main` tags:

- `ghcr.io/q15co/q15-agent:main`
- `ghcr.io/q15co/q15-exec:main`
- `ghcr.io/q15co/q15-proxy:main`

It expects a separate deployment repo or overlay to provide:

- image pinning and rollout policy
- environment-specific namespaces and labels
- Secrets
- PVCs

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

Validate the base with:

```bash
kubectl kustomize deploy/kubernetes/base
```

The intended workflow is:

1. This repo publishes `q15-agent`, `q15-exec`, and `q15-proxy` images to GHCR.
1. A separate deployment repo pins those images and owns environment-specific overlays.
1. The deployment repo rolls out the updated pod set.
