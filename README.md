# q15

Telegram-based shell agent with sandboxed command execution and OpenAI-compatible model providers.

## Requirements

- Go (for local builds/runs)
- Buildah (sandbox runtime)
- A Telegram bot token
- API key(s) for your configured model provider(s)

## Run

Use the repo `q15.toml` (or create your own) and start the agent runtime:

```bash
make run
```

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
