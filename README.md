# Shell AI Agent Bot

An AI-powered Telegram agent bot written in Go. The bot runs an agent loop that can execute shell commands and reply back through Telegram.

## Features

- 🤖 **AI-Powered Conversations**: Uses Moonshot AI's Kimi K2.5 model for intelligent responses
- 🐚 **Shell Command Execution**: Can execute shell commands in fish, bash, or sh via function calling
- 🧵 **Conversation Memory**: Maintains context across turns in memory
- 🔄 **Reset Support**: Clear chat history with `/reset`
- ✉️ **Telegram Adapter**: Telegram channel driver with inbound/outbound message routing
- ⏱️ **Command Timeouts**: 30-second timeout for safety on shell commands
- 🏗️ **Clean Architecture**: Uses ports and adapters pattern for maintainability

## Architecture

This project follows **Hexagonal Architecture** (Ports and Adapters pattern):

\`\`\`
main.go (Entry Point)
       |
       v
cmd/root.go + cmd/start.go (CLI Commands)
       |
       v
internal/app/start.go (Startup + Config Runtime)
       |
       v
internal/app/bot.go + workers.go (Bot Runtime + Workers)
       |
       v
internal/agent/loop.go (Agent Loop)
       |
       v
internal/provider/moonshot/client.go (Model Adapter)
internal/tools/shell.go (Tool Adapter)
\`\`\`

## Prerequisites

- Go 1.25.5 or later
- A Moonshot AI API key (https://platform.moonshot.ai/)

## Installation

1. Clone the repository:
\`\`\`bash
git clone https://github.com/yourusername/sandbox.git
cd sandbox
\`\`\`

2. Install dependencies:
\`\`\`bash
go mod download
\`\`\`

3. Set your Moonshot AI API key:
\`\`\`bash
export MOONSHOT_API_KEY=your-api-key-here
\`\`\`

## Usage

Show CLI help:

\`\`\`bash
go run .
\`\`\`

Start all agents from `q15.toml`:

\`\`\`bash
go run . start --config q15.toml
\`\`\`

## Configuration

### Environment Variables

| Variable | Required | Description |
|----------|----------|-------------|
| MOONSHOT_API_KEY | Provider dependent | API key for model provider in config |
| TELEGRAM_BOT_TOKEN | Agent dependent | Telegram token when referenced by `telegram.token_env` |

Runtime options can also be set with `Q15_`-prefixed env vars (for example `Q15_CONFIG`).
Config precedence is: flags > env vars > config file > defaults.

### Config File

Use `q15.toml` to define providers and agents:

\`\`\`toml
[[provider]]
name = "moonshot"
type = "openai-compatible"
base_url = "https://api.moonshot.ai/v1"
key_env = "MOONSHOT_API_KEY"

[[agent]]
name = "Jared"
model = "moonshot/kimi-k2.5"

[agent.telegram]
token_env = "JARED_TELEGRAM_TOKEN"
allowed_user_ids = [123456789]
\`\`\`

The `agent.model` field uses `provider/model` format.
You can set either `telegram.token` or `telegram.token_env` per agent.
`telegram.allowed_user_ids` is required and only those Telegram users may talk to that agent.

### Supported Shells

The application looks for shells in this order:
1. fish (preferred)
2. bash
3. sh

## Dependencies

- github.com/openai/openai-go/v3 - OpenAI-compatible Go client
- github.com/mymmrac/telego - Telegram bot adapter
- github.com/spf13/viper - Config/flags/env merging

## Project Structure

\`\`\`
.
├── cmd/
│   ├── root.go              # Root CLI command
│   └── start.go             # Starts all configured agents
├── internal/
│   ├── app/                 # App runtime
│   │   ├── start.go
│   │   ├── bot.go
│   │   └── workers.go
│   ├── agent/               # Agent contracts
│   │   └── agent.go
│   ├── conversation/        # Conversation domain events
│   │   └── event.go
│   ├── provider/            # Model provider adapters
│   │   └── moonshot/
│   │       └── client.go
│   ├── channel/             # Channel adapters
│   │   └── telegram/
│   │       └── channel.go
│   └── memory/              # Memory domain
│       └── event.go
├── go.mod                   # Go module definition
├── go.sum                   # Dependency checksums
├── main.go                  # Process entry point
└── README.md                # This file
\`\`\`

## System Prompt

The AI assistant is configured with:
> You are a helpful assistant with excellent skills in using nixos and the fish shell

## Safety Features

- Command Timeouts: 30-second timeout for all shell commands
- Combined Output: Both stdout and stderr are captured
- Error Handling: Command failures are reported to the AI

## License

[Your License Here]

## Acknowledgments

- Built with Moonshot AI's Kimi K2.5 model
- Uses the OpenAI-compatible API from Moonshot
