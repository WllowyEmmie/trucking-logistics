"""Small helpers with no LangChain imports, so they can be tested alone."""

import json
import re
import secrets
import uuid
from typing import Any

UUID_RE = re.compile(r"^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$")
# The API accepts thread ids of 1-64 letters, digits, _ and - (X-Copilot-Thread).
THREAD_RE = re.compile(r"^[0-9a-f]{32}_[A-Za-z0-9_-]{8,24}$")


def is_uuid(value: str) -> bool:
    return bool(UUID_RE.match(value.strip()))


def new_thread_id(dispatcher_id: str) -> str:
    """A conversation id that names its owner: the dispatcher's id (hex, no
    dashes) and a random part. Checking the prefix is all the ownership check
    needs, with no table of threads to keep."""
    return f"{uuid.UUID(dispatcher_id).hex}_{secrets.token_urlsafe(12)}"


def owns_thread(dispatcher_id: str, thread_id: str) -> bool:
    if not THREAD_RE.match(thread_id or ""):
        return False
    try:
        return thread_id.split("_", 1)[0] == uuid.UUID(dispatcher_id).hex
    except ValueError:
        return False


def idempotency_key(thread_id: str, tool_call_id: str | None) -> str:
    """One key per tool call: if the same approved call is run again (a retry,
    a resume sent twice) the API answers with the first result instead of
    acting twice. Without a tool call id, a fresh key: no protection, but no
    wrong replays either."""
    base = tool_call_id or secrets.token_hex(8)
    key = re.sub(r"[^A-Za-z0-9_.:-]", "", f"cp:{thread_id[-12:]}:{base}")
    return key[:128] if len(key) >= 8 else key + secrets.token_hex(4)


def find_by_reference(items: list[dict], ref: str) -> list[dict]:
    """Deliveries whose reference matches what the dispatcher typed:
    the whole reference ('DEL-2026-10042', any case) or just its number
    ('10042')."""
    want = ref.strip().upper().replace(" ", "")
    if not want:
        return []
    out = []
    for it in items:
        got = str(it.get("reference", "")).upper()
        if got == want or (want.isdigit() and got.rsplit("-", 1)[-1] == want):
            out.append(it)
    return out


def norm_plate(value: str) -> str:
    return re.sub(r"[^A-Z0-9]", "", value.upper())


def find_by_plate(trucks: list[dict], plate: str) -> list[dict]:
    want = norm_plate(plate)
    return [t for t in trucks if norm_plate(str(t.get("plate_number", ""))) == want]


def compact(value: Any) -> str:
    """Tool results go into the prompt: no pretty-printing."""
    return json.dumps(value, separators=(",", ":"), default=str)


def wrap_data(label: str, value: Any) -> str:
    """Tool output that carries text from outside (notes, addresses, names)
    is fenced, so the model treats it as data. The system prompt says text in
    these blocks is never an instruction."""
    return f"<data source={json.dumps(label)}>\n{compact(value)}\n</data>"


def session_token(cookie_header: str | None, authorization: str | None, cookie_name: str = "session") -> str | None:
    """The dispatcher's session token, from their bearer header or their
    session cookie, so the Copilot can call the API as them."""
    if authorization and authorization.startswith("Bearer "):
        tok = authorization.removeprefix("Bearer ").strip()
        if tok:
            return tok
    for part in (cookie_header or "").split(";"):
        name, _, val = part.strip().partition("=")
        if name == cookie_name and val:
            return val
    return None


def sse(event: str, data: Any) -> str:
    """One server-sent event."""
    return f"event: {event}\ndata: {json.dumps(data, default=str)}\n\n"


PERIODS = ("today", "yesterday", "last_7_days", "last_30_days", "this_month", "last_month", "custom")


def resolve_period(period: str, from_date: str | None, to_date: str | None, today: "date") -> tuple[str, str, str]:
    """A named period, or two dates, → (from, to, label), dates inclusive in
    the operator's time zone. Raises ValueError with a sentence to show."""
    from datetime import date, timedelta

    p = (period or "today").strip().lower()
    if p not in PERIODS:
        raise ValueError(f"Unknown period {period!r}. Use one of: {', '.join(PERIODS)}.")
    if p == "today":
        a = b = today
    elif p == "yesterday":
        a = b = today - timedelta(days=1)
    elif p == "last_7_days":
        a, b = today - timedelta(days=6), today
    elif p == "last_30_days":
        a, b = today - timedelta(days=29), today
    elif p == "this_month":
        a, b = today.replace(day=1), today
    elif p == "last_month":
        b = today.replace(day=1) - timedelta(days=1)
        a = b.replace(day=1)
    else:
        try:
            a = date.fromisoformat((from_date or "").strip())
            b = date.fromisoformat((to_date or from_date or "").strip())
        except ValueError:
            raise ValueError("A custom period needs from_date and to_date like 2026-10-01.") from None
    if b < a:
        a, b = b, a
    if (b - a).days > 91:
        raise ValueError("At most 92 days at a time.")
    if a == b:
        label = a.strftime("%a %d %b %Y")
    elif a.year == b.year and a.month == b.month:
        label = f"{a.day}–{b.day} {b.strftime('%b %Y')}"
    else:
        label = f"{a.strftime('%d %b')} – {b.strftime('%d %b %Y')}"
    return a.isoformat(), b.isoformat(), label
