"""The Go API, called as the signed-in dispatcher.

Every Copilot call carries the dispatcher's own session token (never the
agent's service key), so the API applies their permissions and records the
change as theirs. Writes are labelled X-Via: copilot with the conversation
id, and carry an Idempotency-Key per tool call so a repeated call can't act
twice."""

from dataclasses import dataclass
from typing import Any
from urllib.parse import quote

import httpx

from ..config import settings

_http = httpx.AsyncClient(base_url=settings.api_url, timeout=settings.api_timeout_s)


class CopilotAPIError(Exception):
    """The API refused. `code` is its stable reason (truck_busy, wrong_status,
    not_found...), `message` its sentence for people."""

    def __init__(self, status: int, code: str, message: str):
        self.status = status
        self.code = code
        self.message = message
        super().__init__(f"{status} {code}: {message}")


@dataclass(frozen=True)
class AsDispatcher:
    token: str
    thread_id: str

    def _headers(self, idempotency_key: str | None = None) -> dict[str, str]:
        h = {
            "Authorization": f"Bearer {self.token}",
            "Content-Type": "application/json",
            "X-Via": "copilot",
            "X-Copilot-Thread": self.thread_id,
        }
        if idempotency_key:
            h["Idempotency-Key"] = idempotency_key
        return h

    async def request(
        self,
        method: str,
        path: str,
        *,
        json: Any = None,
        params: dict | None = None,
        idempotency_key: str | None = None,
    ) -> Any:
        try:
            res = await _http.request(
                method, path, json=json, params=params, headers=self._headers(idempotency_key)
            )
        except httpx.TransportError as e:
            raise CopilotAPIError(0, "unreachable", f"the dispatch API is unreachable ({type(e).__name__})") from e
        if res.status_code >= 400:
            try:
                body = res.json()
            except ValueError:
                body = {}
            raise CopilotAPIError(
                res.status_code,
                str(body.get("code") or "error"),
                str(body.get("error") or res.reason_phrase),
            )
        if res.status_code == 204 or not res.content:
            return None
        return res.json()

    async def get(self, path: str, params: dict | None = None) -> Any:
        return await self.request("GET", path, params=params)

    async def post(self, path: str, body: Any, key: str) -> Any:
        return await self.request("POST", path, json=body, idempotency_key=key)

    async def patch(self, path: str, body: Any, key: str) -> Any:
        return await self.request("PATCH", path, json=body, idempotency_key=key)

    async def put(self, path: str, body: Any, key: str) -> Any:
        return await self.request("PUT", path, json=body, idempotency_key=key)


def seg(value: str) -> str:
    """One path segment: a model's argument can't reach another endpoint."""
    return quote(str(value), safe="")


async def aclose() -> None:
    await _http.aclose()
