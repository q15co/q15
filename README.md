# q15

Telegram-based shell agent with sandboxed command execution and OpenAI-compatible model providers.

## Requirements

- Linux (sandbox runtime is rootless Buildah)
- Go (for local builds/runs)
- Buildah (sandbox runtime)
- A Telegram bot token
- API key(s) for your configured model provider(s)
- A working C toolchain for building `q15-sandbox-helper` locally (`make run` builds it with cgo)

On NixOS, rootless sandbox startup requires the host wrapper helpers:

- `/run/wrappers/bin/newuidmap`
- `/run/wrappers/bin/newgidmap`

## Run

Use the repo `q15.toml` (or create your own) and start the agent runtime:

```bash
make run
```

`q15` runs as a normal unprivileged user process. The `q15-sandbox-helper` process enters the
rootless user namespace for Buildah.

Or run the agent module directly:

```bash
go run ./systems/agent start --config q15.toml
```

## Config

`q15.toml` defines providers and agents.

`agent.models` is an ordered fallback list of `provider/model` references.

- The agent tries models in order.
- It falls back only when a model call fails.
- Fallbacks can span providers (for example `moonshot/...` -> `zai/...`).
- If you want one model, use a list of one item.

Example:

```toml
[[provider]]
name = "moonshot"
type = "openai-compatible"
base_url = "https://api.moonshot.ai/v1"
key_env = "MOONSHOT_API_KEY"

[[provider]]
name = "zai"
type = "openai-compatible"
base_url = "https://api.z.ai/api/coding/paas/v4"
key_env = "ZAI_API_KEY"

[[agent]]
name = "Jared"
models = ["moonshot/kimi-k2.5", "zai/glm-5"]

[agent.sandbox]
container_name = "q15-jared"
from_image = "docker.io/library/debian:bookworm-slim"
workspace_host_dir = "/home/you/q15-workspaces/jared"
workspace_dir = "/workspace"
network = "enabled"

[agent.telegram]
token_env = "JARED_TELEGRAM_TOKEN"
allowed_user_ids = [123456789]
```

## Notes

- Use `/reset` in Telegram to clear an agent's conversation history for that chat.
- `telegram.allowed_user_ids` is required.
- Set `telegram.token` or `telegram.token_env`.
- Optional Brave web search tool: set `Q15_BRAVE_API_KEY` to enable the `web_search` tool for the
  model.
- `web_search` runs in the host agent process (not inside the sandbox shell), so it is independent
  of `agent.sandbox.network`.
- On NixOS dev shells, do not add `shadow` to the shell packages: the Nix-store
  `newuidmap`/`newgidmap` binaries are not usable for rootless user-namespace setup.

## Troubleshooting

If sandbox prepare fails with an error mentioning a Nix store `shadow` path such as
`/nix/store/...-shadow-.../bin/newuidmap`, the helper resolved the wrong uidmap binary. Use the host
wrappers in `/run/wrappers/bin` and remove `shadow` from the devshell.
