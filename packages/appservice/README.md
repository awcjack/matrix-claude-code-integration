# AppService Package

Multi-session Matrix-Claude Code integration using Application Service mode.

## Features

- **Session isolation**: Each room/thread gets its own Claude Code instance
- **Separate contexts**: Conversations don't mix between rooms
- **Scalable**: Configurable concurrent session limits
- **Efficient**: Events pushed from homeserver (no polling)

## Architecture

```
Matrix Homeserver
       │
       │ (push via Application Service API)
       ▼
┌─────────────────────────────────────────────┐
│              COORDINATOR                     │
│                                             │
│  ┌──────────┐  ┌────────┐  ┌─────────────┐ │
│  │ AS HTTP  │──│ Router │──│ IPC Server  │ │
│  │ Server   │  │        │  │ (Unix Sock) │ │
│  └──────────┘  └────────┘  └──────┬──────┘ │
└───────────────────────────────────┼─────────┘
                                    │
         ┌──────────────────────────┼──────────────────────┐
         │                          │                      │
         ▼                          ▼                      ▼
┌─────────────────┐      ┌─────────────────┐      ┌─────────────────┐
│ Claude Code #1  │      │ Claude Code #2  │      │ Claude Code #3  │
│       │         │      │       │         │      │       │         │
│  MCP (stdio)    │      │  MCP (stdio)    │      │  MCP (stdio)    │
│       │         │      │       │         │      │       │         │
│  Bridge #1      │      │  Bridge #2      │      │  Bridge #3      │
│  (IPC Client)   │      │  (IPC Client)   │      │  (IPC Client)   │
└─────────────────┘      └─────────────────┘      └─────────────────┘
    Room A                   Room B                   Room C
```

## Components

| Binary | Purpose |
|--------|---------|
| `matrix-coordinator` | Parent server, receives Matrix events, manages sessions |
| `matrix-bridge` | Child MCP server, bridges IPC ↔ Claude Code |

## Usage

```bash
# Build
make build-appservice

# Run coordinator
./bin/matrix-coordinator -config config.json

# Bridge is spawned automatically per-session by coordinator
```

## Configuration

```json
{
  "matrix": {
    "homeserver": "https://matrix.example.com",
    "appservice": {
      "listen_address": ":8080",
      "hs_token": "...",
      "as_token": "...",
      "bot_user_id": "@claude-bot:matrix.example.com"
    }
  },
  "sessions": {
    "working_directory": "/workspace",
    "model": "sonnet",
    "idle_timeout": "30m"
  },
  "ipc": {
    "socket_path": "/tmp/matrix-claude.sock"
  },
  "whitelist": ["@user:matrix.example.com"]
}
```

## Session Lifecycle

1. Message arrives for Room X
2. Coordinator checks if session exists for Room X
3. If not, spawns: `claude --channels server:matrix-bridge`
4. Bridge connects to coordinator via IPC
5. Message forwarded: Coordinator → IPC → Bridge → Claude
6. Reply flows back: Claude → Bridge → IPC → Coordinator → Matrix
7. Idle sessions cleaned up after timeout
