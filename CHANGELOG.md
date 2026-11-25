# Changelog

## v1.0.0 – 2026-11-25

First release of this fork of [lharries/whatsapp-mcp](https://github.com/lharries/whatsapp-mcp).

### Added
- Web dashboard with browser QR pairing, live status and scheduled messages (dark/light mode).
- Message scheduler in the bridge (`schedule_message`, `list_scheduled_messages`, `cancel_scheduled_message`).
- New tools: `catch_up`, `get_chat_stats`, `export_chat`, `get_bridge_status`, `send_reaction`, `mark_as_read`, `list_groups`, `get_group_info`, `check_whatsapp_numbers`, `request_chat_history`.
- Quote-replies (`reply_to`) and captions for files.
- MCP prompts: `whatsapp_daily_digest`, `whatsapp_draft_reply`, `whatsapp_find`; tool annotations.
- Safety guards: read-only mode, recipient and media-folder allow-lists, untrusted-content labelling.
- Optional API token; Dockerfile and docker-compose; Windows and macOS/Linux installers.
- Storage of captions, polls, locations, contacts, reactions, edits, deletions, replies, contact names.
- CI on Windows, macOS and Linux; release workflow with prebuilt bridge binaries; Go and Python test suites.

### Changed
- Updated whatsmeow so the bridge connects to current WhatsApp servers.
- Bridge listens on `127.0.0.1` instead of all interfaces.
- MCP server uses `httpx`, reads the database read-only and logs to stderr.
- Downloaded media is stored under `store/media/<chat>/` with collision-free names.
- Pairing no longer gives up after 3 minutes; logged-out devices re-pair automatically.

### Fixed
- Cross-site request forgery: any website could previously send messages through the local API.
- Path traversal through crafted document names.
- Messages sorted incorrectly when timestamps had different UTC offsets.
- Chat "last active" time moving backwards during history sync.
- Duplicate chats in `list_chats` and duplicate messages when context windows overlapped.
- `print()` output corrupting the MCP stdio protocol.
- Voice-note duration calculated with the wrong sample rate.
