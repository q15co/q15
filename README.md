# Shell AI Assistant

An interactive AI-powered shell assistant CLI written in Go. This tool provides an intelligent conversational interface that can execute shell commands on your behalf, with a focus on NixOS and fish shell expertise.

## Features

- 🤖 **AI-Powered Conversations**: Uses Moonshot AI's Kimi K2.5 model for intelligent responses
- 🐚 **Shell Command Execution**: Can execute shell commands in fish, bash, or sh via function calling
- 💬 **Interactive CLI**: Simple REPL-style interface for continuous conversation
- 🧵 **Conversation Memory**: Maintains context across turns in memory
- 🔄 **Reset Support**: Clear conversation history with `/reset`
- ✉️ **Telegram Adapter**: Optional Telegram channel driver
- ⏱️ **Command Timeouts**: 30-second timeout for safety on shell commands
- 🏗️ **Clean Architecture**: Uses ports and adapters pattern for maintainability

## Architecture

This project follows **Hexagonal Architecture** (Ports and Adapters pattern):

\`\`\`
cmd/main.go (Entry Point)
       |
       v
internal/app/cli.go (CLI Driver)
       |
       | depends on
       v
internal/agent/agent.go (Agent Interface)
       |
       | implements
       v
internal/provider/moonshot/client.go (Provider Adapter)
       - Moonshot AI integration
       - Tool/function calling
       - Shell execution
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

Run the application:

\`\`\`bash
go run cmd/main.go
\`\`\`

Or build and run:

\`\`\`bash
go build -o shell-ai cmd/main.go
./shell-ai
\`\`\`

### Interactive Commands

Once running, you'll see the prompt:

\`\`\`
Type your question and press enter.
Use '/reset' to clear chat history.
Type 'exit' to quit.
#>
\`\`\`

- Type any question or command request
- The AI can execute shell commands on your behalf (you'll see CMD> and OUT> prefixes)
- Use /reset to clear conversation history
- Type exit or quit to exit

### Example Session

\`\`\`
#> list all files in the current directory
CMD> ls -la
total 12
drwxr-xr-x 3 user users   60 Feb 20 23:49 .
...
OUT> (output shown above)
AI> Here are the files in your current directory...

#> /reset
history reset

#> exit
\`\`\`

## Configuration

### Environment Variables

| Variable | Required | Description |
|----------|----------|-------------|
| MOONSHOT_API_KEY | Yes | Your Moonshot AI API key |
| TELEGRAM_BOT_TOKEN | Bot mode only | Telegram bot token |

### Supported Shells

The application looks for shells in this order:
1. fish (preferred)
2. bash
3. sh

## Dependencies

- github.com/openai/openai-go/v3 - OpenAI-compatible Go client
- github.com/tidwall/gjson - JSON parsing utilities
- github.com/tidwall/sjson - JSON modification utilities

## Project Structure

\`\`\`
.
├── cmd/
│   └── main.go              # Application entry point
├── internal/
│   ├── app/                 # App runners (CLI/Bot)
│   │   ├── cli.go
│   │   └── bot.go
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
