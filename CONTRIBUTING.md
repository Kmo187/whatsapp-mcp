# Contributing

Thanks for helping! Issues and pull requests are welcome – from typo fixes to new tools.

## Setup

```bash
git clone https://github.com/<you>/whatsapp-mcp.git
cd whatsapp-mcp
make build
make test
```

You need Go 1.26+ with a C compiler, Python 3.11+ and [uv](https://docs.astral.sh/uv/).

## Project layout

```
whatsapp-bridge/        Go: WhatsApp connection, SQLite store, REST API, dashboard, scheduler
  api.go                HTTP handlers and security middleware
  messages.go           incoming messages, history sync, names
  send.go               sending, replies, reactions, read receipts
  store.go              messages.db schema and migrations
whatsapp-mcp-server/    Python: MCP tools
  main.py               tool and prompt definitions
  whatsapp.py           database queries and bridge calls
  guards.py             safety policy (read-only, allow-lists)
  tests/                pytest suite with a fake bridge
docs/                   configuration and API reference
scripts/                installers
```

## Guidelines

- **Never commit real data.** No message contents, phone numbers, tokens or files from `store/` – also not in tests, issues or screenshots. Use the fictional numbers from `tests/conftest.py`.
- **Keep existing tools compatible.** People have the server configured in their clients; renaming a tool or parameter breaks them.
- **Safety first.** New actions that send something must go through `guards.check_can_send` and be annotated as such in `main.py`.
- **Tests:** add a Go test for bridge logic and a pytest for new tools. `make lint` must pass (gofmt, go vet, ruff, mypy).
- **Commits:** small and focused, written in the imperative ("Add read receipts").

## Adding a tool

1. Implement the data access or bridge call in `whatsapp.py` (return a dict with `success` and `message` on errors).
2. Register it in `main.py` with a clear docstring – the model reads it – and the right `ToolAnnotations`.
3. If the bridge needs a new endpoint, add it in `api.go` behind `a.secure(...)` and document it in `docs/api.md`.
4. Add tests and a row in the README tool table.
