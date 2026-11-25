#!/usr/bin/env bash
# Installer for macOS and Linux: builds the bridge, installs the MCP server
# and prints the Claude Desktop configuration.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
need() { command -v "$1" >/dev/null 2>&1 || { echo "✗ $1 is required: $2" >&2; exit 1; }; }
need go "https://go.dev/dl/"
need uv "curl -LsSf https://astral.sh/uv/install.sh | sh"
need gcc "Xcode command line tools (xcode-select --install) or your distro's build-essential"

echo "==> Building the WhatsApp bridge"
cd "$ROOT/whatsapp-bridge"
CGO_ENABLED=1 go build -trimpath -ldflags "-s -w -X main.Version=$(git -C "$ROOT" describe --tags --always 2>/dev/null || echo dev)" -o whatsapp-bridge .

echo "==> Installing the MCP server"
cd "$ROOT/whatsapp-mcp-server"
uv sync --quiet

UV="$(command -v uv)"
case "$(uname -s)" in
  Darwin) CFG="$HOME/Library/Application Support/Claude/claude_desktop_config.json" ;;
  *)      CFG="$HOME/.config/Claude/claude_desktop_config.json" ;;
esac

cat <<MSG

✓ Done.

1. Start the bridge and link your phone:
     cd "$ROOT/whatsapp-bridge" && ./whatsapp-bridge
   then open http://127.0.0.1:8080/ and scan the QR code
   (WhatsApp → Settings → Linked devices → Link a device).

2. Add this to $CFG:

{
  "mcpServers": {
    "whatsapp": {
      "command": "$UV",
      "args": ["--directory", "$ROOT/whatsapp-mcp-server", "run", "main.py"]
    }
  }
}

   Claude Code users can run instead:
     claude mcp add whatsapp -- "$UV" --directory "$ROOT/whatsapp-mcp-server" run main.py

3. Restart Claude Desktop.
MSG
