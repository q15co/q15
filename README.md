# q15

`q15` is a Telegram-based coding agent split across three long-lived services:

- `q15`: the agent runtime
- `q15-exec-service`: command execution and session lifecycle
- `q15-proxy-service`: proxy policy and auth-env mediation

Interactive auth bootstrap is handled separately by `q15-auth`.

## Architecture

- The agent owns prompt assembly, tool wiring, Telegram I/O, memory management, and rooted file
  operations.
- `exec-service` owns command execution and reports the authoritative model-visible runtime
  directories: `/workspace`, `/memory`, and `/skills`.
- `proxy-service` owns policy, egress, and auth-env injection for exec sessions.
- `q15-auth` is an operator tool that produces `auth.json` outside the runtime containers.

This maps directly onto the intended deployment model: one agent runtime, one exec-service runtime,
and one proxy-service runtime running together in Compose or Kubernetes.

## Build And Test

```bash
make build
make test
```

Artifacts are written to `./bin`:

- `q15`
- `q15-auth`
- `q15-exec-service`
- `q15-proxy-service`

## Config Strategy

q15 now uses a narrow container-first contract:

- agent config is a mounted YAML file at `/etc/q15/agent/config.yaml`
- proxy policy is a mounted YAML file at `/etc/q15/proxy/policy.yaml`
- auth credentials are a mounted JSON file at `/etc/q15/auth/auth.json`
- provider, Telegram, and proxy secrets come from env vars or `_FILE`
- service topology, ports, and runtime directories are hard-coded

The fixed runtime contract is:

- `q15`
  - reads `/etc/q15/agent/config.yaml`
  - reads `/etc/q15/auth/auth.json`
  - uses `/workspace` and `/skills`
  - connects to `q15-exec-service:50051`
- `q15-exec-service`
  - listens on `:50051`
  - talks to `q15-proxy-service:50052`
  - uses `/workspace`, `/memory`, and `/skills`
- `q15-proxy-service`
  - reads `/etc/q15/proxy/policy.yaml`
  - listens on `:50052` and `:18080`
  - advertises `http://q15-proxy-service:18080`
  - uses `/var/lib/q15/proxy-service`

This repo no longer treats runtime wiring as a user-facing config surface.

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
- agent memory lives under `/workspace/.q15-memory`
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
    - q15-proxy-service
    - q15-exec-service
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

The resulting `auth.json` should be mounted into the `q15` container or stored as a Kubernetes
Secret.

## Local Docker Compose Stack

The checked-in Compose stack lives at
[docker-compose.yml](/home/avanderbergh/repos/github.com/q15co/q15/docker-compose.yml) and uses the
examples under [deploy/compose](/home/avanderbergh/repos/github.com/q15co/q15/deploy/compose).

Its interface is organized as:

- [deploy/compose/agent-config.yaml](/home/avanderbergh/repos/github.com/q15co/q15/deploy/compose/agent-config.yaml)
  for structured agent config
- [deploy/compose/proxy-policy.yaml](/home/avanderbergh/repos/github.com/q15co/q15/deploy/compose/proxy-policy.yaml)
  for structured proxy policy
- ignored local Docker secret files under
  [deploy/compose/secrets](/home/avanderbergh/repos/github.com/q15co/q15/deploy/compose/secrets),
  seeded from tracked `*.example` templates in that same directory

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
- a named volume shared as agent memory and exec `/memory`
- a named volume for `/skills`
- Docker secret files under
  [deploy/compose/secrets](/home/avanderbergh/repos/github.com/q15co/q15/deploy/compose/secrets)

Before using the stack for real, replace the placeholder values in:

- [deploy/compose/secrets/moonshot_api_key.example](/home/avanderbergh/repos/github.com/q15co/q15/deploy/compose/secrets/moonshot_api_key.example)
- [deploy/compose/secrets/jared_telegram_token.example](/home/avanderbergh/repos/github.com/q15co/q15/deploy/compose/secrets/jared_telegram_token.example)
- [deploy/compose/secrets/jared_gh_token.example](/home/avanderbergh/repos/github.com/q15co/q15/deploy/compose/secrets/jared_gh_token.example)
- [deploy/compose/secrets/q15_auth_json.example](/home/avanderbergh/repos/github.com/q15co/q15/deploy/compose/secrets/q15_auth_json.example)

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
[deploy/kubernetes/base](/home/avanderbergh/repos/github.com/q15co/q15/deploy/kubernetes/base).

It includes:

- Deployments for `q15-agent`, `q15-exec-service`, and `q15-proxy-service`
- Services for `q15-exec-service` and `q15-proxy-service`
- ConfigMap generators for agent config and proxy policy examples

It expects a separate deployment repo or overlay to provide:

- image names and tags
- environment-specific namespaces and labels
- Secrets
- PVCs

Validate the base with:

```bash
kubectl kustomize deploy/kubernetes/base
```

The intended workflow is:

1. This repo builds and publishes images.
1. A separate deployment repo pins those images and owns environment-specific overlays.
1. The deployment repo rolls out the updated pod set.

## Nix Flake Outputs

The flake builds:

- `q15-agent`
- `q15-exec-service`
- `q15-proxy-service`
- `q15` as the combined package output
