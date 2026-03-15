# q15

`q15` is a Telegram-based coding agent split across three long-lived services:

- `q15`: the agent process
- `q15-exec-service`: command execution and session lifecycle
- `q15-proxy-service`: proxy policy and auth-env mediation

The Buildah sandbox path has been removed. File operations now run directly in the agent against its
mounted workspace and skills volumes, while command execution goes through `q15-exec-service`.

## Architecture

- The agent owns prompt assembly, tool wiring, Telegram I/O, memory management, and rooted file
  operations.
- `exec-service` owns command execution and reports the authoritative model-visible runtime
  directories: `/workspace`, `/memory`, and `/skills`.
- `proxy-service` owns policy, egress, and auth-env injection for exec sessions.

This layout is intended to map directly onto the target deployment model: one agent per Kubernetes
namespace, with separate `agent`, `exec-service`, and `proxy-service` pods.

## Prerequisites

- Go 1.25.x for local builds
- Docker Compose for the local three-container stack
- `git` available in the agent runtime for memory commits

## Build And Test

```bash
make build
make test
```

Artifacts are written to `./bin`:

- `q15`
- `q15-exec-service`
- `q15-proxy-service`

## Agent Config

The agent config uses service-owned runtime boundaries:

- `[agent.workspace]` defines the agent-local workspace mount
- `[skills].local_dir` defines the optional shared skills mount
- `[agent.execution]` is required
- model-visible runtime roots are fixed at `/workspace`, `/memory`, and `/skills`

Example:

```toml
[skills]
local_dir = "/skills"

[[provider]]
name = "openai-sub"
type = "openai-codex"

[[model]]
name = "codex-primary"
provider = "openai-sub"
provider_model = "gpt-5-codex"
capabilities = ["text", "tool_calling", "reasoning"]

[[agent]]
name = "Jared"
models = ["codex-primary"]
memory_recent_turns = 6

[agent.workspace]
local_dir = "/workspace"

[agent.execution]
service_address = "exec-service:50051"

[agent.telegram]
token_env = "JARED_TELEGRAM_TOKEN"
allowed_user_ids = [123456789]
```

Notes:

- Agent memory lives under `<agent.workspace.local_dir>/.q15-memory`.
- `skills.local_dir` is optional, but when present it should point at the shared skills volume.

## Local Docker Compose Stack

The local stack starts three containers:

- `proxy-service`
- `exec-service`
- `agent`

`exec-service` mounts:

- `/workspace`
- `/memory`
- `/skills`

The agent mounts:

- `/workspace`
- `/skills`

Bring the stack up with:

```bash
make compose-up
```

Before running it, provide:

- `~/.config/q15/config.compose.toml`
- `~/.config/q15/proxy-service.compose.toml`
- `~/.config/q15/auth.json`
- any required secret env vars such as `JARED_GH_TOKEN`, `MOONSHOT_API_KEY`, or `ZAI_API_KEY`

Useful commands:

```bash
make compose-down
make compose-logs SERVICE=agent
make compose-ps
```

## Tool Surface

The active tool set is now:

- `read_file`
- `write_file`
- `edit_file`
- `apply_patch`
- `validate_skill`
- `exec`
- `web_fetch`
- `web_search` when configured

Browser-specific shell wrappers were removed. Use `exec` directly with explicit packages when
browser automation is needed.

## Nix Flake Outputs

The flake builds:

- `q15-agent`
- `q15-exec-service`
- `q15-proxy-service`
- `q15` as the combined package output
