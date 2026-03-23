# Matrix-Claude Code Integration Setup Guide

This guide provides detailed instructions for setting up the Matrix-Claude Code integration in various configurations.

## Table of Contents

1. [Prerequisites](#prerequisites)
2. [Bot Mode Setup](#bot-mode-setup)
3. [Application Service Mode Setup](#application-service-mode-setup)
4. [Claude Code Channel Setup](#claude-code-channel-setup)
5. [NixOS Configuration](#nixos-configuration)
6. [Troubleshooting](#troubleshooting)

---

## Prerequisites

### Required Software

- **Go 1.22+**: For building from source
- **Matrix Account**: Either on a public server or self-hosted
- **Claude Code v2.1.80+**: With Channels feature enabled
- **claude.ai Account**: API key authentication is not supported for Channels

### Claude Code Channels Requirements

Claude Code Channels are in research preview and require:
- Claude Code version 2.1.80 or later
- claude.ai login (Console/API key auth not supported)
- Pro, Max, Team, or Enterprise plan
- Team/Enterprise orgs must have Channels explicitly enabled by an admin

Verify your Claude Code version:
```bash
claude --version
```

---

## Bot Mode Setup

Bot mode uses the Matrix Client-Server API with polling. It works with any Matrix server, including matrix.org.

### Step 1: Create a Bot Account

1. Register a new Matrix account for your bot
2. You can use any Matrix client or the command line:

```bash
# Using curl to register (if registration is open)
curl -X POST "https://matrix.org/_matrix/client/v3/register" \
  -H "Content-Type: application/json" \
  -d '{"username": "my-claude-bot", "password": "secure-password", "auth": {"type": "m.login.dummy"}}'
```

### Step 2: Get an Access Token

Option A - Using Element Web:
1. Log in to Element with your bot account
2. Go to Settings > Help & About > Advanced
3. Copy the "Access token" value

Option B - Using curl:
```bash
curl -X POST "https://matrix.org/_matrix/client/v3/login" \
  -H "Content-Type: application/json" \
  -d '{
    "type": "m.login.password",
    "user": "my-claude-bot",
    "password": "your-password"
  }'
```

Save the `access_token` from the response.

### Step 3: Configure the Bridge

Create a config file or use environment variables:

```bash
# config.bot-mode.json
{
  "mode": "bot",
  "matrix": {
    "homeserver": "https://matrix.org",
    "user_id": "@my-claude-bot:matrix.org",
    "access_token": "syt_abc123..."
  },
  "claude_code": {
    "working_directory": "/home/user/projects",
    "model": "claude-sonnet-4-6-20250514"
  },
  "whitelist": ["@your-user:matrix.org"]
}
```

### Step 4: Run the Bridge

```bash
./matrix-claude-code --config config.bot-mode.json
```

Or with environment variables:
```bash
export MATRIX_MODE=bot
export MATRIX_HOMESERVER=https://matrix.org
export MATRIX_USER_ID=@my-claude-bot:matrix.org
export MATRIX_ACCESS_TOKEN=syt_abc123...
export MATRIX_WHITELIST='["@your-user:matrix.org"]'
export CLAUDE_CODE_WORKING_DIR=/home/user/projects

./matrix-claude-code
```

### Step 5: Invite the Bot

From your Matrix client, invite the bot to a room:
1. Create or open a room
2. Invite `@my-claude-bot:matrix.org`
3. Send a message to start chatting

---

## Application Service Mode Setup

Application Service mode provides better performance for self-hosted Matrix servers.

### Step 1: Generate Registration File

```bash
export AS_HOMESERVER_DOMAIN=matrix.example.com
export AS_SENDER_LOCALPART=claude-bot
export AS_PUBLIC_URL=http://bridge-server:8080

./matrix-claude-code --generate-registration -registration-output registration.yaml
```

This creates `registration.yaml` with generated tokens.

### Step 2: Configure Your Homeserver

#### Synapse

1. Copy the registration file:
```bash
cp registration.yaml /etc/synapse/appservices/
```

2. Add to `homeserver.yaml`:
```yaml
app_service_config_files:
  - /etc/synapse/appservices/registration.yaml
```

3. Restart Synapse:
```bash
systemctl restart synapse
```

#### Dendrite

1. Copy the registration file:
```bash
cp registration.yaml /etc/dendrite/appservices/
```

2. Add to `dendrite.yaml`:
```yaml
app_service_api:
  config_files:
    - /etc/dendrite/appservices/registration.yaml
```

3. Restart Dendrite:
```bash
systemctl restart dendrite
```

#### Conduit

Add to your Conduit configuration:
```toml
[global.appservices]
claude-code = "/etc/conduit/appservices/registration.yaml"
```

### Step 3: Configure the Bridge

Create your configuration:

```json
{
  "mode": "appservice",
  "matrix": {
    "homeserver": "https://matrix.example.com"
  },
  "appservice": {
    "registration_path": "/etc/bridge/registration.yaml",
    "listen_address": ":8080",
    "public_url": "http://bridge-server:8080",
    "sender_localpart": "claude-bot",
    "homeserver_domain": "matrix.example.com"
  },
  "claude_code": {
    "working_directory": "/home/user/projects"
  },
  "whitelist": ["@admin:matrix.example.com"]
}
```

### Step 4: Network Configuration

Ensure your homeserver can reach the bridge:
- The bridge listens on the configured `listen_address` (default `:8080`)
- The `public_url` must be reachable from the homeserver
- Configure firewall rules as needed

### Step 5: Run the Bridge

```bash
./matrix-claude-code --config config.json
```

---

## Claude Code Channel Setup

This mode integrates directly with Claude Code's `--channels` feature.

### Step 1: Add to MCP Configuration

Add the bridge to your `.mcp.json`:

```json
{
  "mcpServers": {
    "matrix": {
      "command": "/path/to/matrix-claude-code",
      "args": ["--stdio", "--config", "/path/to/config.json"]
    }
  }
}
```

For project-level config, place `.mcp.json` in your project root.
For user-level config, use `~/.claude.json`.

### Step 2: Configure the Bridge

Create a config file with Matrix credentials:

```json
{
  "mode": "bot",
  "matrix": {
    "homeserver": "https://matrix.org",
    "user_id": "@my-claude-bot:matrix.org",
    "access_token": "your_access_token"
  },
  "channel": {
    "name": "matrix",
    "stdio_mode": true
  },
  "whitelist": ["@your-user:matrix.org"]
}
```

### Step 3: Start Claude Code with Channels

```bash
# During research preview, use the development flag
claude --dangerously-load-development-channels server:matrix
```

Once the channel is on the approved allowlist:
```bash
claude --channels server:matrix
```

### Step 4: Usage

Messages from Matrix will appear in your Claude Code session as:
```
<channel source="matrix" room_id="!abc:matrix.org" sender="@user:matrix.org">
Your message here
</channel>
```

Claude will respond using the `reply` tool, sending messages back to Matrix.

---

## NixOS Configuration

Example NixOS module for the integration:

```nix
{ config, lib, pkgs, ... }:

with lib;

let
  cfg = config.services.matrix-claude-code;
in {
  options.services.matrix-claude-code = {
    enable = mkEnableOption "Matrix-Claude Code Integration";

    package = mkOption {
      type = types.package;
      description = "The matrix-claude-code package to use";
    };

    mode = mkOption {
      type = types.enum [ "appservice" "bot" ];
      default = "appservice";
      description = "Operating mode";
    };

    homeserver = mkOption {
      type = types.str;
      description = "Matrix homeserver URL";
    };

    homeserverDomain = mkOption {
      type = types.str;
      description = "Matrix homeserver domain";
    };

    botLocalpart = mkOption {
      type = types.str;
      default = "claude-bot";
      description = "Bot user localpart";
    };

    listenAddress = mkOption {
      type = types.str;
      default = "127.0.0.1:8080";
      description = "Address to listen on";
    };

    workingDirectory = mkOption {
      type = types.str;
      default = "/var/lib/matrix-claude-code/workspace";
      description = "Working directory for Claude Code";
    };

    whitelist = mkOption {
      type = types.listOf types.str;
      default = [];
      description = "Whitelisted Matrix user IDs";
    };

    environmentFile = mkOption {
      type = types.nullOr types.path;
      default = null;
      description = "Environment file with secrets";
    };
  };

  config = mkIf cfg.enable {
    systemd.services.matrix-claude-code = {
      description = "Matrix-Claude Code Integration";
      wantedBy = [ "multi-user.target" ];
      after = [ "network-online.target" ];
      wants = [ "network-online.target" ];

      serviceConfig = {
        Type = "simple";
        User = "matrix-claude-code";
        Group = "matrix-claude-code";
        DynamicUser = true;
        StateDirectory = "matrix-claude-code";
        WorkingDirectory = "/var/lib/matrix-claude-code";
        EnvironmentFile = mkIf (cfg.environmentFile != null) cfg.environmentFile;

        ExecStart = "${cfg.package}/bin/matrix-claude-code";

        # Security hardening
        NoNewPrivileges = true;
        ProtectSystem = "strict";
        ProtectHome = true;
        PrivateTmp = true;
        PrivateDevices = true;
        ProtectKernelTunables = true;
        ProtectKernelModules = true;
        ProtectControlGroups = true;
        RestrictNamespaces = true;
        RestrictRealtime = true;
        RestrictSUIDSGID = true;
        MemoryDenyWriteExecute = true;
        LockPersonality = true;
      };

      environment = {
        MATRIX_MODE = cfg.mode;
        MATRIX_HOMESERVER = cfg.homeserver;
        AS_HOMESERVER_DOMAIN = cfg.homeserverDomain;
        AS_SENDER_LOCALPART = cfg.botLocalpart;
        AS_LISTEN_ADDRESS = cfg.listenAddress;
        CLAUDE_CODE_WORKING_DIR = cfg.workingDirectory;
        MATRIX_WHITELIST = builtins.toJSON cfg.whitelist;
      };
    };

    users.users.matrix-claude-code = {
      isSystemUser = true;
      group = "matrix-claude-code";
      home = "/var/lib/matrix-claude-code";
    };

    users.groups.matrix-claude-code = {};
  };
}
```

---

## Troubleshooting

### Bot Doesn't Respond

1. **Check whitelist**: Ensure your Matrix user ID is in the whitelist
   ```bash
   echo $MATRIX_WHITELIST
   ```

2. **Verify tokens**: Make sure access token is valid
   ```bash
   curl -H "Authorization: Bearer $MATRIX_ACCESS_TOKEN" \
     "https://matrix.org/_matrix/client/v3/account/whoami"
   ```

3. **Check logs**: Run with debug logging
   ```bash
   LOG_LEVEL=debug ./matrix-claude-code --config config.json
   ```

### Application Service Not Receiving Events

1. **Verify registration**: Check that the homeserver loaded the registration
   - Synapse: Check logs for "Loading application service"
   - Dendrite: Look for appservice initialization messages

2. **Check connectivity**: Ensure homeserver can reach the bridge
   ```bash
   curl http://bridge-server:8080/health
   ```

3. **Verify tokens**: Ensure hs_token and as_token match between registration and config

### Claude Code Channel Issues

1. **Channels not available**:
   - Ensure you're on Claude Code v2.1.80+
   - Verify you're logged in via claude.ai (not API key)
   - Team/Enterprise: Ask admin to enable Channels

2. **Channel not registering**:
   - During research preview, use `--dangerously-load-development-channels`
   - Check MCP server configuration in `.mcp.json`

3. **Messages not arriving**:
   - Verify the MCP server is running
   - Check that Matrix credentials are correct
   - Ensure whitelist includes your Matrix user

### Common Error Messages

**"Invalid hs_token"**: The homeserver token doesn't match. Regenerate registration and update homeserver config.

**"User not whitelisted"**: Add your Matrix user ID to the whitelist configuration.

**"Failed to connect to Matrix"**: Check homeserver URL and network connectivity.

**"Channels blocked by org policy"**: Team/Enterprise admin must enable Channels in organization settings.

### Getting Help

- Check the [README](./README.md) for quick reference
- Open an issue on GitHub for bugs
- Join the Matrix room for community support: `#matrix-claude-code:matrix.org`
