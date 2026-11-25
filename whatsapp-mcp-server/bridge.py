"""Small HTTP client for the Go bridge's REST API."""

from __future__ import annotations

from typing import Any

import httpx

from config import Settings


class BridgeError(Exception):
    """The bridge is unreachable or returned an error."""


class BridgeClient:
    def __init__(self, settings: Settings, transport: httpx.BaseTransport | None = None) -> None:
        headers = {"User-Agent": "whatsapp-mcp-server"}
        if settings.bridge_token:
            headers["Authorization"] = f"Bearer {settings.bridge_token}"
        self._client = httpx.Client(
            base_url=settings.bridge_url,
            headers=headers,
            timeout=httpx.Timeout(settings.request_timeout, connect=5.0),
            transport=transport,
            trust_env=False,  # the bridge is local; never route it through a proxy
        )
        self.base_url = settings.bridge_url

    def request(
        self, method: str, path: str, json: dict[str, Any] | None = None, params: dict[str, Any] | None = None
    ) -> dict[str, Any]:
        try:
            resp = self._client.request(method, path, json=json, params=params)
        except httpx.ConnectError as exc:
            raise BridgeError(
                f"Cannot reach the WhatsApp bridge at {self.base_url}. Is it running? "
                "Start it with `go run .` in whatsapp-bridge (or the bridge-starten.bat / Docker container)."
            ) from exc
        except httpx.TimeoutException as exc:
            raise BridgeError(f"The WhatsApp bridge did not answer in time ({exc.__class__.__name__}).") from exc
        except httpx.HTTPError as exc:
            raise BridgeError(f"Request to the WhatsApp bridge failed: {exc}") from exc

        try:
            data = resp.json()
        except ValueError:
            data = {"success": False, "message": resp.text.strip() or f"HTTP {resp.status_code}"}
        if not isinstance(data, dict):
            data = {"success": resp.is_success, "data": data}
        if resp.status_code == 401:
            raise BridgeError(
                "The bridge rejected the token. Set WHATSAPP_BRIDGE_TOKEN to the same value for bridge and MCP server."
            )
        if resp.status_code == 404 and resp.request.url.path in ("/api/status", "/api/health"):
            raise BridgeError("The bridge is too old for this feature. Update and rebuild whatsapp-bridge.")
        if not resp.is_success or data.get("success") is False:
            raise BridgeError(str(data.get("message") or f"HTTP {resp.status_code}"))
        return data

    def get(self, path: str, **params: Any) -> dict[str, Any]:
        return self.request("GET", path, params={k: v for k, v in params.items() if v is not None} or None)

    def post(self, path: str, body: dict[str, Any]) -> dict[str, Any]:
        return self.request("POST", path, json=body)

    def delete(self, path: str) -> dict[str, Any]:
        return self.request("DELETE", path)
