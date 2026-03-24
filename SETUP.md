# Setup Guide

## Bot Package Setup

See [`packages/bot/README.md`](./packages/bot/README.md)

### Quick Steps

1. Create Matrix bot account
2. Get access token from Element (Settings → Help & About → Advanced)
3. Configure:
```bash
export MATRIX_HOMESERVER=https://matrix.org
export MATRIX_USER_ID=@bot:matrix.org
export MATRIX_ACCESS_TOKEN=your_token
export MATRIX_WHITELIST='["@you:matrix.org"]'
```
4. Run: `claude --channels server:./bin/matrix-claude-bot`

## AppService Package Setup

See [`packages/appservice/README.md`](./packages/appservice/README.md)

### Quick Steps

1. Generate registration:
```bash
# Create registration.yaml with tokens
```

2. Add to homeserver (Synapse example):
```yaml
# homeserver.yaml
app_service_config_files:
  - /etc/synapse/appservices/registration.yaml
```

3. Configure `config.json`:
```json
{
  "matrix": {
    "homeserver": "https://matrix.example.com",
    "appservice": {
      "listen_address": ":8080",
      "hs_token": "from_registration",
      "as_token": "from_registration",
      "bot_user_id": "@claude-bot:matrix.example.com"
    }
  },
  "sessions": {
    "working_directory": "/workspace",
    "model": "claude-sonnet-4-6-20250514",
    "idle_timeout": "30m"
  },
  "ipc": {
    "socket_path": "/tmp/matrix-claude.sock"
  },
  "whitelist": ["@you:matrix.example.com"]
}
```

4. Run: `./bin/matrix-coordinator -config config.json`

## Troubleshooting

### Bot doesn't respond
- Check whitelist includes your Matrix ID
- Verify access token is valid
- Ensure Claude Code is running with `--channels`

### AppService not receiving events
- Verify registration file loaded by homeserver
- Check coordinator can be reached from homeserver
- Ensure hs_token matches between registration and config

### Permission requests
- Reply with `!allow` or `!deny` within 60 seconds
- Use `!allow <id>` if multiple pending requests
