# q15

Telegram-based shell agent with sandboxed command execution and OpenAI-compatible or OpenAI
Codex-subscription model providers.

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

Place your config at `~/.config/q15/config.toml` (or override `CONFIG=...`) and start the agent
runtime:

```bash
make run
```

To run with the repo-local config file instead:

```bash
make run CONFIG=config.toml
```

`q15` runs as a normal unprivileged user process. The `q15-sandbox-helper` process enters the
rootless user namespace for Buildah.

Or run the agent module directly:

```bash
go run ./systems/agent start
```

Default config path is `~/.config/q15/config.toml` and can be overridden with `--config` or
`Q15_CONFIG`.

Use `--config-dir` or `Q15_CONFIG_DIR` to change the default base directory used by both config and
auth paths.

If the config file is missing, `q15 start` now creates a minimal starter file and exits cleanly with
no running agents. Add your `[[provider]]` / `[[agent]]` blocks and start again.

## Nix Flake Package

This repo exposes a flake package for Linux (`x86_64-linux`) that installs both binaries:

- `q15`
- `q15-sandbox-helper`

Build locally:

```bash
nix build .#q15
```

Run directly from the flake output:

```bash
./result/bin/q15 start --help
```

Use from another flake (for example your `dots` repo):

```nix
inputs.q15.url = "github:q15co/q15";
```

Then consume:

```nix
inputs.q15.packages.${pkgs.system}.q15
```

### Updating Flake Vendor Hashes

When Go dependencies change, refresh flake `vendorHash` values automatically:

```bash
make nix-update-vendor-hashes
```

This updates both module hashes in `flake.nix` and validates with `nix build .#q15`.

## CI/CD and Releases

GitHub Actions now uses GitHub-hosted `ubuntu-latest` runners for CI and release jobs.

- CI runs on:
  - pull requests targeting `main`
  - pushes to `main`
- CI sets up Nix via `cachix/install-nix-action` and `cachix/cachix-action`.
- Release runs only after a successful CI run on `main` (not from PRs).

Releases are auto-tagged with a semver-compatible date+SHA format:

- `vYYYY.M.D-<sha7>`
- Example: `v2026.3.4-a1b2c3d`
- Date is derived from the commit date in UTC and paired with the commit short SHA

Because GoReleaser expects semver tags, this format keeps calendar-based versions while remaining
compatible with release automation.

### Main Branch Protection (GitHub Settings)

Configure branch protection (or a ruleset) for `main` with these minimum controls:

- Require a pull request before merging
- Require status checks to pass before merging
  - Required check: `CI` job from workflow `CI`
- Restrict direct pushes to `main` (except trusted admins if you prefer)
- Disallow force pushes and branch deletions

## Config

`config.toml` defines providers and agents. By default q15 reads it from
`~/.config/q15/config.toml`.

An empty starter config is valid and runs zero agents until you add entries.

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

[[provider]]
name = "openai-sub"
type = "openai-codex"

[[agent]]
# Authoritative agent identity used for prompt identity and core-memory rendering.
name = "Jared"
models = ["openai-sub/gpt-5-codex", "moonshot/kimi-k2.5", "zai/glm-5"]
memory_recent_turns = 6

[agent.sandbox]
container_name = "q15-jared"
workspace_host_dir = "/home/you/q15-workspaces/jared"
workspace_dir = "/workspace"

[agent.telegram]
token_env = "JARED_TELEGRAM_TOKEN"
allowed_user_ids = [123456789]
```

### Sandbox Runtime (Nix-Only)

Sandbox runtime is hardcoded to a rootless-Buildah-friendly nix-only mode:

- Base image is fixed: `docker.io/library/debian:bookworm-slim`
- Sandbox networking is always enabled
- Nix is auto-bootstrapped during `Prepare` if not already installed

### exec_shell Usage

`exec_shell` is nix-first and requires packages per call.

Required arguments:

- `command` (string)
- `packages` (array of nix installables, minimum 1)

Example tool payload:

```json
{
  "command": "git --version && jq --version",
  "packages": ["nixpkgs#git", "nixpkgs#jq"]
}
```

Runtime execution shape:

```bash
nix --extra-experimental-features "nix-command flakes" shell <packages...> --command /bin/bash -c '<command>'
```

First run may require network access to bootstrap nix and fetch packages.

### OpenAI Codex Subscription Login

For `provider.type = "openai-codex"`, q15 reads OAuth credentials from:

- `~/.config/q15/auth.json` (provider key: `openai`)

Path overrides:

- `Q15_CONFIG_DIR` sets the base directory used by both defaults (`config.toml` and `auth.json`).
- `Q15_AUTH_PATH` sets an explicit auth file path.
- CLI equivalents: `--config-dir` and `--auth-path`.

Login and inspect credentials:

```bash
q15 auth login --provider openai
q15 auth status
q15 auth logout --provider openai
```

## Notes

- `memory_recent_turns` controls how many persisted turns are replayed into the model context on
  each reply. `0` uses default `6`.
- Tool-call loop safety limits are internal runtime guards (hard-coded in the agent binary) and are
  not user-configurable in `config.toml`.
  - These guards are separate from `memory_recent_turns`.
  - If a run is interrupted by loop safety, the partial turn is still persisted so follow-up replies
    can continue with context.
- Sandbox runtime is nix-only with fixed Debian base image and always-on networking.
- `agent.name` is the authoritative runtime identity for the assistant.
  - The default system prompt is rendered from `agent.name`.
  - Seeded core memory templates use `{{agent_name}}` and are rendered at load time.
  - Keep identity lines in core memory templated with `{{agent_name}}` rather than hardcoding a
    name.
  - No legacy identity migration is performed for pre-template core files.
- Agent memory is persisted per configured agent runtime in:
  - host: `<agent.sandbox.workspace_host_dir>/.q15-memory`
  - sandbox: `/memory` The memory directory is git-backed and auto-committed after successful turns.
- Core memory is stored in:
  - `/memory/core/*.md` (seeded templates like `AGENT.md`, `USER.md`, `SOUL.md`) These files are
    injected into the system prompt on each reply.
- External memory stays out-of-context by default:
  - `/memory/history/turns/...` (canonical transcript turns)
  - `/memory/notes/...` (agent-managed notes)
- `telegram.allowed_user_ids` is required.
- Set `telegram.token` or `telegram.token_env`.
- `openai-codex` providers do not use `provider.base_url` or `provider.key_env`.
- `openai-compatible` providers still require both `provider.base_url` and `provider.key_env`.
- Optional Brave web search tool: set `Q15_BRAVE_API_KEY` to enable the `web_search` tool for the
  model.
- `web_search` runs in the host agent process (not inside the sandbox shell).
- On NixOS dev shells, do not add `shadow` to the shell packages: the Nix-store
  `newuidmap`/`newgidmap` binaries are not usable for rootless user-namespace setup.

## Troubleshooting

If sandbox prepare fails with an error mentioning a Nix store `shadow` path such as
`/nix/store/...-shadow-.../bin/newuidmap`, the helper resolved the wrong uidmap binary. Use the host
wrappers in `/run/wrappers/bin` and remove `shadow` from the devshell.

If an `openai-codex` model call fails with a credential error, run:

```bash
q15 auth login --provider openai
```
