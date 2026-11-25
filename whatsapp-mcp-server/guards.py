"""Safety policy for actions that leave the computer.

The WhatsApp MCP server combines private data, untrusted content (messages
from anyone) and the ability to send messages – the "lethal trifecta".
These guards let users decide exactly how much the AI may do:

* ``WHATSAPP_MCP_READ_ONLY=1`` – no sending at all.
* ``WHATSAPP_MCP_ALLOWED_RECIPIENTS`` – send only to these numbers / JIDs.
* ``WHATSAPP_MCP_ALLOWED_MEDIA_DIRS`` – attach files only from these folders,
  so a prompt injection cannot make the AI send e.g. ``~/.ssh/id_rsa``.
"""

from __future__ import annotations

import re
from pathlib import Path

from config import Settings


class PolicyError(Exception):
    """Raised when an action is blocked by the user's safety settings."""


_PHONE_FORMAT = re.compile(r"[\s+\-().]")


def normalize_recipient(recipient: str) -> str:
    """Return a canonical form: digits for phone numbers, the JID otherwise."""
    r = (recipient or "").strip()
    if "@" in r:
        user, _, server = r.partition("@")
        user = user.split(":")[0]  # drop device suffix
        if server == "s.whatsapp.net":
            return user
        return f"{user}@{server}"
    digits = _PHONE_FORMAT.sub("", r)
    digits = digits.removeprefix("00")
    return digits


def check_can_send(settings: Settings, recipient: str) -> None:
    if settings.read_only:
        raise PolicyError("Sending is disabled: the WhatsApp MCP server runs in read-only mode (WHATSAPP_MCP_READ_ONLY=1).")
    if not recipient or not recipient.strip():
        raise PolicyError("Recipient must be provided.")
    if settings.allowed_recipients:
        allowed = {normalize_recipient(a) for a in settings.allowed_recipients}
        if normalize_recipient(recipient) not in allowed:
            raise PolicyError(
                f"Recipient {recipient} is not in WHATSAPP_MCP_ALLOWED_RECIPIENTS. "
                "Ask the user to add it to the allow-list if this is intended."
            )


def check_media_path(settings: Settings, media_path: str) -> Path:
    """Validate a file that should be sent and return its resolved path."""
    if not media_path:
        raise PolicyError("Media path must be provided.")
    path = Path(media_path).expanduser()
    if not path.is_absolute():
        raise PolicyError(f"Media path must be absolute: {media_path}")
    resolved = path.resolve()
    if not resolved.is_file():
        raise PolicyError(f"Media file not found: {media_path}")
    if settings.allowed_media_dirs and not any(resolved.is_relative_to(d) for d in settings.allowed_media_dirs):
        raise PolicyError(
            f"{resolved} is outside WHATSAPP_MCP_ALLOWED_MEDIA_DIRS; only files from "
            + ", ".join(str(d) for d in settings.allowed_media_dirs)
            + " may be sent."
        )
    return resolved
