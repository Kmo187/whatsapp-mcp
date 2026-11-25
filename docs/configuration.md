# Configuration

Everything is configured with environment variables. For the MCP server, put them in the `env` block of your client configuration; for the bridge, set them in the shell, the start script, `.env` (Docker) or your service manager.

## Bridge (`whatsapp-bridge`)

| Variable | Default | Description |
|---|---|---|
| `WHATSAPP_BRIDGE_ADDR` | `127.0.0.1:8080` | Address of the REST API and dashboard. Anything other than loopback requires `WHATSAPP_BRIDGE_TOKEN`. |
| `WHATSAPP_BRIDGE_PORT` | `8080` | Shortcut: port on `127.0.0.1` when `WHATSAPP_BRIDGE_ADDR` is not set. |
| `WHATSAPP_BRIDGE_TOKEN` | – | Shared secret. Clients send `Authorization: Bearer <token>`; the dashboard is unlocked once with `/?token=<token>` (stored in an HttpOnly cookie). |
| `WHATSAPP_BRIDGE_ALLOW_INSECURE` | `0` | Allow a non-loopback address without token. Only for isolated networks. |
| `WHATSAPP_STORE_DIR` | `store` | Folder for `whatsapp.db` (session keys – keep it secret), `messages.db` and `media/`. |
| `WHATSAPP_LOG_CONTENT` | `0` | Print message texts in the log. Off by default so `bridge.log` contains no private messages. |
| `WHATSAPP_LOG_LEVEL` | `INFO` | `DEBUG`, `INFO`, `WARN` or `ERROR`. |
| `WHATSAPP_HISTORY_REQUEST_COUNT` | `100` | Default number of messages for `request_chat_history`. |

Command-line flags: `-version` prints the version, `-healthcheck` exits 0 if a bridge answers on the configured address.

## MCP server (`whatsapp-mcp-server`)

| Variable | Default | Description |
|---|---|---|
| `WHATSAPP_BRIDGE_URL` | `http://127.0.0.1:8080` | Base URL of the bridge. |
| `WHATSAPP_BRIDGE_PORT` | `8080` | Shortcut when the URL is not set. |
| `WHATSAPP_BRIDGE_TOKEN` | – | Must match the bridge. |
| `WHATSAPP_STORE_DIR` | `../whatsapp-bridge/store` | Where `messages.db` lives. |
| `WHATSAPP_MCP_DB_PATH` | `<store>/messages.db` | Explicit path to the database. |
| `WHATSAPP_MCP_READ_ONLY` | `0` | Block every action that sends something (messages, files, reactions, read receipts, scheduling). |
| `WHATSAPP_MCP_ALLOWED_RECIPIENTS` | – | Comma-separated phone numbers (any format) or JIDs. When set, sending to anyone else is refused. |
| `WHATSAPP_MCP_ALLOWED_MEDIA_DIRS` | – | Comma-separated folders. When set, only files inside them can be sent. |
| `WHATSAPP_MCP_EXPORT_DIR` | `whatsapp-mcp-server/exports` | Where `export_chat` writes files. |
| `WHATSAPP_MCP_WRAP_UNTRUSTED` | `1` | Prefix message listings with a note that their content is untrusted. |
| `WHATSAPP_MCP_TIMEOUT` | `60` | Seconds to wait for the bridge. |

### Example: a careful setup

```json
{
  "mcpServers": {
    "whatsapp": {
      "command": "uv",
      "args": ["--directory", "/home/me/whatsapp-mcp/whatsapp-mcp-server", "run", "main.py"],
      "env": {
        "WHATSAPP_MCP_ALLOWED_RECIPIENTS": "41781234567",
        "WHATSAPP_MCP_ALLOWED_MEDIA_DIRS": "/home/me/WhatsApp-Share"
      }
    }
  }
}
```

Claude can now read everything, but can only message you yourself and only attach files you put into `WhatsApp-Share`.

## Docker

`docker compose up -d` builds and starts the bridge with the store mounted at `./whatsapp-bridge/store`, so the MCP server on your machine finds `messages.db` at its default location. Because the container listens on `0.0.0.0` inside Docker, a token is required; the port is published on `127.0.0.1` only.

Paths are seen from inside the container:

- `download_media` returns paths like `/data/store/media/...`, which on your machine are `./whatsapp-bridge/store/media/...`.
- To send files, mount a folder at the **same absolute path** in the container (e.g. `- /home/me/WhatsApp-Share:/home/me/WhatsApp-Share:ro`) and allow it with `WHATSAPP_MCP_ALLOWED_MEDIA_DIRS`.

## Running the bridge in the background

**Windows** – `scripts/install-windows.ps1` creates `bridge-unsichtbar.vbs` and a shortcut in the Startup folder. Output goes to `whatsapp-bridge/bridge.log`.

**macOS (launchd)** – `~/Library/LaunchAgents/com.github.whatsapp-mcp.bridge.plist`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>com.github.whatsapp-mcp.bridge</string>
  <key>ProgramArguments</key><array><string>/Users/me/whatsapp-mcp/whatsapp-bridge/whatsapp-bridge</string></array>
  <key>WorkingDirectory</key><string>/Users/me/whatsapp-mcp/whatsapp-bridge</string>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>/Users/me/whatsapp-mcp/whatsapp-bridge/bridge.log</string>
  <key>StandardErrorPath</key><string>/Users/me/whatsapp-mcp/whatsapp-bridge/bridge.log</string>
</dict></plist>
```

Load it with `launchctl load ~/Library/LaunchAgents/com.github.whatsapp-mcp.bridge.plist`.

**Linux (systemd user service)** – `~/.config/systemd/user/whatsapp-bridge.service`:

```ini
[Unit]
Description=WhatsApp bridge for MCP
After=network-online.target

[Service]
WorkingDirectory=%h/whatsapp-mcp/whatsapp-bridge
ExecStart=%h/whatsapp-mcp/whatsapp-bridge/whatsapp-bridge
Restart=on-failure

[Install]
WantedBy=default.target
```

`systemctl --user enable --now whatsapp-bridge`

## Upgrading

```bash
git pull
cd whatsapp-bridge && go build -o whatsapp-bridge .   # Windows: -o whatsapp-bridge.exe
```

Restart the bridge and your MCP client. The session and history are kept; new database columns are added automatically. Media downloaded by older versions in `store/<chat>/` is still found.
