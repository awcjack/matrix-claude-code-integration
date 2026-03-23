# Matrix-Claude Code Integration

A bridge that connects [Matrix](https://matrix.org) chat to [Claude Code](https://claude.ai/claude-code), enabling real-time AI-powered coding assistance directly from Matrix rooms and threads.

## Features

- **Two-Way Communication**: Send messages from Matrix to Claude Code and receive responses back
- **Thread-Based Sessions**: Each Matrix thread maintains its own Claude Code session context
- **Streaming Responses**: Real-time message streaming with MSC4357 live message support
- **Multiple Operating Modes**:
  - **Application Service Mode** (recommended): Push-based, instant event delivery
  - **Bot Mode**: Polling-based, works with any Matrix server
  - **MCP Stdio Mode**: Run as a Claude Code channel plugin
- **User Whitelist**: Control who can interact with the bot
- **Model Selection**: Switch between Claude models per-session

## Architecture

```
Matrix Homeserver <──> Matrix-Claude Code Bridge <──> Claude Code
                         │
                         ├── Application Service API (push events)
                         │   or Client-Server API (polling)
                         │
                         └── MCP Channel Protocol (stdio)
                             └── Claude Code --channels
```

The bridge implements the [MCP (Model Context Protocol)](https://modelcontextprotocol.io) to communicate with Claude Code's Channels feature. Messages from Matrix are pushed as channel notifications, and Claude's replies come back through the MCP reply tool.

## Quick Start

### Prerequisites

- Go 1.22 or later
- A Matrix account or homeserver admin access
- Claude Code v2.1.80 or later with Channels enabled
- claude.ai login (API key auth not supported for Channels)

### Installation

```bash
# Clone the repository
git clone https://github.com/your-org/matrix-claude-code-integration.git
cd matrix-claude-code-integration

# Build
go build -o matrix-claude-code ./cmd/matrix-claude-code

# Or use Docker
docker build -t matrix-claude-code .
```

### Bot Mode (Quick Start)

Bot mode works with any Matrix server, including matrix.org:

1. Create a Matrix account for your bot
2. Get an access token (see [SETUP.md](./SETUP.md) for details)
3. Configure and run:

```bash
export MATRIX_MODE=bot
export MATRIX_HOMESERVER=https://matrix.org
export MATRIX_USER_ID=@your-bot:matrix.org
export MATRIX_ACCESS_TOKEN=your_access_token
export MATRIX_WHITELIST='["@your-user:matrix.org"]'
export CLAUDE_CODE_WORKING_DIR=/path/to/your/projects

./matrix-claude-code
```

### Application Service Mode (Recommended)

For self-hosted Matrix servers, AS mode provides better performance:

1. Generate registration file:
```bash
export AS_HOMESERVER_DOMAIN=matrix.example.com
export AS_PUBLIC_URL=http://your-bridge-server:8080

./matrix-claude-code --generate-registration -registration-output registration.yaml
```

2. Configure your homeserver to use the registration
3. Run the bridge:
```bash
./matrix-claude-code --config config.json
```

See [SETUP.md](./SETUP.md) for detailed instructions.

### Claude Code Channel Mode

Run the bridge as an MCP server for Claude Code's `--channels` flag:

```bash
# Add to your .mcp.json
{
  "mcpServers": {
    "matrix": {
      "command": "/path/to/matrix-claude-code",
      "args": ["--stdio", "--config", "/path/to/config.json"]
    }
  }
}

# Start Claude Code with the channel
claude --channels server:matrix
```

## Configuration

### Environment Variables

| Variable | Description | Required |
|----------|-------------|----------|
| `MATRIX_MODE` | `appservice` or `bot` | Yes |
| `MATRIX_HOMESERVER` | Matrix homeserver URL | Yes |
| `MATRIX_USER_ID` | Bot user ID (bot mode) | Bot mode |
| `MATRIX_ACCESS_TOKEN` | Access token (bot mode) | Bot mode |
| `MATRIX_WHITELIST` | Allowed user IDs (JSON array) | Yes |
| `CLAUDE_CODE_WORKING_DIR` | Working directory for Claude Code | No |
| `CLAUDE_CODE_MODEL` | Default model to use | No |

See [.env.example](./.env.example) for all options.

### JSON Configuration

```json
{
  "mode": "appservice",
  "matrix": {
    "homeserver": "https://matrix.example.com"
  },
  "appservice": {
    "registration_path": "/path/to/registration.yaml",
    "listen_address": ":8080"
  },
  "claude_code": {
    "working_directory": "/home/user/projects",
    "model": "claude-sonnet-4-6-20250514"
  },
  "whitelist": ["@your-user:matrix.example.com"]
}
```

## Usage

### Basic Interaction

1. Invite the bot to a Matrix room
2. Send a message to start a conversation
3. Claude Code processes your request and replies

### Commands

| Command | Description |
|---------|-------------|
| `!help` | Show available commands |
| `!new` | Start a new session |
| `!session` | Show current session info |
| `!model <name>` | Switch model (sonnet, opus, haiku) |
| `!workdir <path>` | Set working directory |

### Thread-Based Sessions

Each Matrix thread maintains its own session context:
- Start a thread to create an isolated conversation
- Context is preserved within the thread
- Use `!new` to reset session state

## Deployment

### Docker

```bash
docker run -d \
  --name matrix-claude-code \
  -e MATRIX_MODE=bot \
  -e MATRIX_HOMESERVER=https://matrix.org \
  -e MATRIX_USER_ID=@your-bot:matrix.org \
  -e MATRIX_ACCESS_TOKEN=your_token \
  -e MATRIX_WHITELIST='["@your-user:matrix.org"]' \
  -v /path/to/projects:/workspace \
  matrix-claude-code
```

### Docker Compose

```bash
cp .env.example .env
# Edit .env with your configuration
docker-compose up -d
```

### Systemd

See [SETUP.md](./SETUP.md) for systemd service configuration.

## Security

- **Whitelist Required**: Only whitelisted Matrix users can interact with the bot
- **Token Authentication**: AS mode uses secure token authentication with the homeserver
- **No Wildcard by Default**: The `*` whitelist pattern must be explicitly configured
- **Local Only**: MCP communication is over stdio, no network exposure

## Streaming & Real-Time Features

The bridge supports MSC4357 Live Messages for real-time streaming:
- Messages are marked as "live" while Claude is responding
- A cursor indicator (▌) shows response progress
- Throttled updates (500ms) prevent rate limiting
- Clients supporting MSC4357 show a streaming indicator

## Troubleshooting

### Common Issues

**Bot doesn't respond:**
- Check whitelist configuration
- Verify Matrix credentials
- Ensure Claude Code session is running with `--channels`

**Connection errors:**
- Verify homeserver URL
- Check firewall settings for AS mode
- Ensure registration file is properly configured

**Permission issues:**
- Channels require claude.ai login (not API key)
- Team/Enterprise orgs must enable Channels in settings

See [SETUP.md](./SETUP.md) for detailed troubleshooting.

## Development

```bash
# Run tests
go test ./...

# Build for development
go build -o matrix-claude-code ./cmd/matrix-claude-code

# Run with debug logging
LOG_LEVEL=debug ./matrix-claude-code --config config.json
```

## License

MIT License - see [LICENSE](./LICENSE) for details.

## Related Projects

- [Claude Code](https://claude.ai/claude-code) - AI-powered coding assistant
- [Matrix](https://matrix.org) - Decentralized communication protocol
- [mautrix-go](https://github.com/mautrix/go) - Matrix client library for Go
- [MCP](https://modelcontextprotocol.io) - Model Context Protocol

## Contributing

Contributions are welcome! Please read our contributing guidelines and submit pull requests to the main repository.
