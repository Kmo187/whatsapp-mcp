"""Runtime configuration for the WhatsApp MCP server.

Everything is read from environment variables so it can be set in the
``env`` block of ``claude_desktop_config.json`` without editing code.
"""

from __future__ import annotations

import os
from dataclasses import dataclass, field
from pathlib import Path

_HERE = Path(__file__).resolve().parent
_DEFAULT_STORE = _HERE.parent / "whatsapp-bridge" / "store"


def _bool(name: str, default: bool) -> bool:
    raw = os.environ.get(name)
    if raw is None or raw.strip() == "":
        return default
    return raw.strip().lower() in {"1", "true", "yes", "on"}


def _list(name: str) -> list[str]:
    raw = os.environ.get(name, "")
    return [item.strip() for item in raw.replace(";", ",").split(",") if item.strip()]


@dataclass(frozen=True)
class Settings:
    """All settings of the MCP server."""

    db_path: Path
    bridge_url: str
    bridge_token: str | None
    read_only: bool
    allowed_recipients: list[str] = field(default_factory=list)
    allowed_media_dirs: list[Path] = field(default_factory=list)
    export_dir: Path = _HERE / "exports"
    wrap_untrusted: bool = True
    request_timeout: float = 60.0

    @property
    def sending_restricted(self) -> bool:
        return bool(self.allowed_recipients)


def load_settings() -> Settings:
    store_dir = Path(os.environ.get("WHATSAPP_STORE_DIR", str(_DEFAULT_STORE))).expanduser()
    db_path = Path(os.environ.get("WHATSAPP_MCP_DB_PATH", str(store_dir / "messages.db"))).expanduser()

    bridge_url = os.environ.get("WHATSAPP_BRIDGE_URL", "").strip()
    if not bridge_url:
        port = os.environ.get("WHATSAPP_BRIDGE_PORT", "8080").strip() or "8080"
        bridge_url = f"http://127.0.0.1:{port}"
    bridge_url = bridge_url.rstrip("/")
    bridge_url = bridge_url.removesuffix("/api")

    return Settings(
        db_path=db_path,
        bridge_url=bridge_url,
        bridge_token=os.environ.get("WHATSAPP_BRIDGE_TOKEN") or None,
        read_only=_bool("WHATSAPP_MCP_READ_ONLY", False),
        allowed_recipients=_list("WHATSAPP_MCP_ALLOWED_RECIPIENTS"),
        allowed_media_dirs=[Path(p).expanduser().resolve() for p in _list("WHATSAPP_MCP_ALLOWED_MEDIA_DIRS")],
        export_dir=Path(os.environ.get("WHATSAPP_MCP_EXPORT_DIR", str(_HERE / "exports"))).expanduser(),
        wrap_untrusted=_bool("WHATSAPP_MCP_WRAP_UNTRUSTED", True),
        request_timeout=float(os.environ.get("WHATSAPP_MCP_TIMEOUT", "60") or 60),
    )
