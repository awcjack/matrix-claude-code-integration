# Matrix-Claude Code Integration

A bridge connecting [Matrix](https://matrix.org) to [Claude Code](https://claude.ai/claude-code), enabling AI-powered coding assistance from Matrix rooms.

## Packages

| Package | Description | Session Isolation |
|---------|-------------|-------------------|
| [`packages/bot`](./packages/bot) | Single-session bot mode | ❌ Shared context |
| [`packages/appservice`](./packages/appservice) | Multi-session appservice mode | ✅ Isolated per room |

**Bot Package**: Simple setup, personal use, all rooms share one Claude session.

**AppService Package**: Production use, each room gets its own isolated Claude Code instance.

## Architecture

### Bot Package (Single Session)

```
Matrix Homeserver
       │
       │ (polling)
       ▼
┌─────────────────────┐
│   Bot Process       │
│                     │
│  Matrix Client      │
│       │             │
│  MCP Server ◄───────┼──── Claude Code (stdio)
└─────────────────────┘
```

All Matrix rooms share the same Claude Code context.

### AppService Package (Multi Session)

```
Matrix Homeserver
       │
       │ (push via AppService API)
       ▼
┌─────────────────────────────────────────────┐
│              COORDINATOR                     │
│                                             │
│  AppService HTTP ──► Router ──► IPC Server │
└──────────────────────────┬──────────────────┘
                           │ Unix Socket
        ┌──────────────────┼──────────────────┐
        ▼                  ▼                  ▼
┌───────────────┐  ┌───────────────┐  ┌───────────────┐
│ Claude Code   │  │ Claude Code   │  │ Claude Code   │
│      │        │  │      │        │  │      │        │
│ MCP (stdio)   │  │ MCP (stdio)   │  │ MCP (stdio)   │
│      │        │  │      │        │  │      │        │
│ Bridge (IPC)  │  │ Bridge (IPC)  │  │ Bridge (IPC)  │
└───────────────┘  └───────────────┘  └───────────────┘
    Room A             Room B             Room C
```

Each room gets isolated Claude Code context via IPC.

**Components:**
- **Coordinator**: Receives Matrix events, manages sessions, routes messages
- **Bridge**: Per-session MCP server connecting IPC ↔ Claude Code
- **IPC**: Unix socket communication between coordinator and bridges

## Quick Start

### Prerequisites

- Go 1.22+
- Claude Code v2.1.80+ with Channels enabled
- Matrix account or homeserver admin access

### Build

```bash
make build           # Build all
make build-bot       # Bot only
make build-appservice # AppService only
```

### Bot Mode

```bash
export MATRIX_HOMESERVER=https://matrix.org
export MATRIX_USER_ID=@bot:matrix.org
export MATRIX_ACCESS_TOKEN=your_token
export MATRIX_WHITELIST='["@you:matrix.org"]'

claude --channels server:./bin/matrix-claude-bot
```

### AppService Mode

```bash
./bin/matrix-coordinator -config config.json
# Bridges spawn automatically per room
```

## Commands

| Command | Description |
|---------|-------------|
| `!help` | Show help |
| `!new` | New session |
| `!status` | Session info |
| `!sessions` | List all sessions (appservice) |
| `!allow` / `!deny` | Permission responses |

## License

MIT
