# Bridge REST API

The bridge listens on `http://127.0.0.1:8080` by default. All responses are JSON with a `success` field; errors also carry `message` and a matching HTTP status (`400` bad input, `401` token, `403` blocked by security checks, `404` not found, `503` not connected to WhatsApp).

Rules for every request:

- `POST` bodies must be `Content-Type: application/json`.
- Requests from a browser on another origin are rejected (`Origin` check).
- When the bridge listens on loopback, the `Host` header must be `localhost`/`127.0.0.1`/`[::1]`.
- When `WHATSAPP_BRIDGE_TOKEN` is set: send `Authorization: Bearer <token>` (or `X-Bridge-Token`).

| Method & path | Body / query | Description |
|---|---|---|
| `GET /` | – | Dashboard (HTML) |
| `GET /api/health` | – | `{"ok":true,"state":"connected","connected":true,"version":"…"}` – no token needed |
| `GET /api/status` | – | Connection state, account, counters, token/log settings |
| `GET /api/qr.svg` | – | Current pairing QR code (404 when paired) |
| `POST /api/send` | `{"recipient","message","media_path?","reply_to?"}` | Send text or a file; returns `id`, `chat_jid`, `timestamp` |
| `POST /api/download` | `{"message_id","chat_jid"}` | Download media; returns `path`, `filename`, `media_type`, `cached` |
| `POST /api/react` | `{"chat_jid","message_id","emoji"}` | React; empty `emoji` removes the reaction |
| `POST /api/read` | `{"chat_jid","message_ids?"}` | Read receipts (latest 20 received if no IDs) |
| `POST /api/typing` | `{"chat_jid","typing":true}` | Show / hide "typing…" |
| `POST /api/check` | `{"phones":["41781234567"]}` | Which numbers are on WhatsApp (max 50) |
| `POST /api/history` | `{"chat_jid","count?"}` | Ask the phone for older messages of a chat |
| `GET /api/groups` | – | Joined groups |
| `GET /api/group?jid=…@g.us` | – | Group details with participants |
| `GET /api/schedule?status=` | `pending\|sent\|failed\|cancelled` | Scheduled messages |
| `POST /api/schedule` | `{"recipient","message","send_at","media_path?","reply_to?"}` | Schedule; `send_at` in RFC 3339 with offset, at most one year ahead |
| `DELETE /api/schedule/{id}` | – | Cancel a pending message |

`recipient` accepts phone numbers in any common format (`+41 78 123 45 67`, `0041781234567`) or JIDs (`…@s.whatsapp.net`, `…@g.us`).

## Database

`store/messages.db` (SQLite) is the contract between bridge and MCP server:

- `chats(jid, name, last_message_time)`
- `messages(id, chat_jid, sender, content, timestamp, is_from_me, media_type, filename, url, media_key, file_sha256, file_enc_sha256, file_length, quoted_id, sender_name)`
- `contacts(jid, name, push_name, updated_at)`
- `reactions(chat_jid, message_id, sender, emoji, timestamp)`
- `scheduled_messages(id, recipient, message, media_path, reply_to, send_at, status, error, created_at, sent_at)`

Timestamps are written by Go with a UTC offset; compare them with SQLite's `julianday()`.
