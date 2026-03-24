# Bot Package

Single-session Matrix-Claude Code integration using bot mode.

## Features

- **Simple setup**: Just needs Matrix account credentials
- **Single session**: All rooms share the same Claude Code context
- **MCP Channel**: Runs as Claude Code's MCP channel server via stdio

## Architecture

```
Matrix Homeserver
       │
       │ (polling via Client-Server API)
       ▼
┌─────────────────────┐
│   Bot Process       │
│                     │
│  ┌───────────────┐  │
│  │ Matrix Client │  │
│  └───────┬───────┘  │
│          │          │
│  ┌───────┴───────┐  │
│  │  MCP Server   │◄─┼──── Claude Code (stdio)
│  └───────────────┘  │
└─────────────────────┘
```

## Usage

```bash
# Build
make build-bot

# Run with Claude Code
claude --channels server:./bin/matrix-claude-bot

# Or standalone (without Claude Code integration)
./bin/matrix-claude-bot --config config.json
```

## Configuration

```json
{
  "matrix": {
    "homeserver": "https://matrix.example.com",
    "user_id": "@bot:matrix.example.com",
    "access_token": "YOUR_ACCESS_TOKEN"
  },
  "whitelist": ["@user:matrix.example.com"]
}
```

## Limitations

- **No session isolation**: All Matrix rooms share the same Claude context
- **No conversation separation**: Messages from different rooms are mixed
- **Single user focus**: Best for personal use with one active conversation
