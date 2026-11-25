# Security policy

This project gives software access to a personal WhatsApp account, so security reports are taken seriously.

## Reporting a vulnerability

Please **do not open a public issue**. Use GitHub's private reporting:
<https://github.com/Kmo187/whatsapp-mcp/security/advisories/new>

Include what an attacker could do, how to reproduce it and which version you tested. You will get an answer as soon as possible.

## Scope

In scope: the bridge's REST API and dashboard, the MCP server, the safety guards, the installers and the Docker setup.

Out of scope: vulnerabilities in WhatsApp itself or in whatsmeow (report those upstream), and prompt injection that only works when the user disabled the default protections.

## Hardening checklist for users

- Keep the bridge on `127.0.0.1` (default). Use `WHATSAPP_BRIDGE_TOKEN` if you change that.
- Treat `whatsapp-bridge/store/whatsapp.db` like a password: it contains your session keys.
- Use `WHATSAPP_MCP_READ_ONLY`, `WHATSAPP_MCP_ALLOWED_RECIPIENTS` and `WHATSAPP_MCP_ALLOWED_MEDIA_DIRS` to limit what an assistant can do.
- Review tool calls that send messages before approving them in your MCP client.
- Update regularly: WhatsApp changes its protocol and old bridge versions stop working.
