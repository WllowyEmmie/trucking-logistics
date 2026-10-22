"""The Copilot's tools. Each one calls an endpoint the console already uses,
as the signed-in dispatcher (dispatcher_api.py).

Three kinds, and the harness treats them differently (agent.py):
- READ_TOOLS run at once.
- WRITE_TOOLS and DESTRUCTIVE_TOOLS pause for the dispatcher's approval
  (HumanInTheLoopMiddleware) before they run. Destructive ones need a reason.
- UI_TOOLS move the console (map, panels) and never touch data.

Refusals come back as text the model can explain ("Refused (truck_busy): ..."),
never as exceptions that end the turn. Text from outside (addresses, names,
notes) comes back fenced in <data> blocks."""

import math
from datetime import datetime, timezone
from typing import Literal

from langchain.tools import ToolRuntime, tool
from pydantic import BaseModel, Field

from .context import CopilotContext
from .dispatcher_api import AsDispatcher, CopilotAPIError, seg
from .util import compact, find_by_plate, find_by_reference, idempotency_key, is_uuid, wrap_data

Runtime = ToolRuntime[CopilotContext]

DELIVERY_STATUSES = ("pending", "assigned", "in_transit", "completed", "cancelled", "failed")
MAX_BULK = 10


def _api(rt: Runtime) -> AsDispatcher:
    return AsDispatcher(rt.context.token, rt.context.thread_id)


def _key(rt: Runtime) -> str:
    return idempotency_key(rt.context.thread_id, getattr(rt, "tool_call_id", None))


def _refused(e: CopilotAPIError) -> str:
    return f"Refused ({e.code}): {e.message}"


class _NotFound(Exception):
    pass


async def _delivery_id(api: AsDispatcher, ref: str) -> tuple[str, str]:
    """A delivery's id and reference, from an id or a reference such as
    DEL-2026-10042 or just 10042."""
    ref = ref.strip()
    if is_uuid(ref):
        return ref, ref
    items = await api.get("/deliveries")
    hits = find_by_reference(items or [], ref)
    if len(hits) == 1:
        return hits[0]["id"], hits[0]["reference"]
    if not hits:
        raise _NotFound(f"No delivery with reference {ref!r}.")
    raise _NotFound(f"{len(hits)} deliveries match {ref!r}: " + ", ".join(h["reference"] for h in hits[:5]))


async def _truck(api: AsDispatcher, plate_or_id: str) -> dict:
    trucks = await api.get("/trucks") or []
    v = plate_or_id.strip()
    hits = [t for t in trucks if t.get("id") == v] if is_uuid(v) else find_by_plate(trucks, v)
    if len(hits) != 1:
        raise _NotFound(f"No truck {v!r}." if not hits else f"Several trucks match {v!r}.")
    return hits[0]


async def _driver(api: AsDispatcher, name_or_id: str) -> dict:
    drivers = await api.get("/drivers") or []
    v = name_or_id.strip()
    if is_uuid(v):
        hits = [d for d in drivers if d.get("id") == v]
    else:
        low = v.lower()
        hits = [d for d in drivers if str(d.get("name", "")).lower() == low] or [
            d for d in drivers if low in str(d.get("name", "")).lower() or v in str(d.get("phone", ""))
        ]
    if len(hits) != 1:
        raise _NotFound(
            f"No driver {v!r}." if not hits else f"Several drivers match {v!r}: " + ", ".join(d["name"] for d in hits[:5])
        )
    return hits[0]


def _minutes_since(ts: str | None) -> int | None:
    if not ts:
        return None
    try:
        t = datetime.fromisoformat(ts.replace("Z", "+00:00"))
    except ValueError:
        return None
    return int((datetime.now(timezone.utc) - t).total_seconds() // 60)


def _km(lat1: float, lng1: float, lat2: float, lng2: float) -> float:
    r = 6371.0
    p1, p2 = math.radians(lat1), math.radians(lat2)
    dp, dl = p2 - p1, math.radians(lng2 - lng1)
    a = math.sin(dp / 2) ** 2 + math.cos(p1) * math.cos(p2) * math.sin(dl / 2) ** 2
    return 2 * r * math.asin(math.sqrt(a))


# ---------------------------------------------------------------- reads


@tool
async def list_deliveries(runtime: Runtime, status: str = "", waiting_more_than_min: int = 0, limit: int = 30) -> str:
    """List deliveries on the board (active ones and recently finished).

    status: pending, assigned, in_transit, completed, cancelled or failed; empty for all.
    waiting_more_than_min: only those created more than this many minutes ago.
    Returns id, reference, status, customer, truck_id and minutes since created.
    """
    status = status.strip().lower()
    if status and status not in DELIVERY_STATUSES:
        return f"Unknown status {status!r}. Use one of: {', '.join(DELIVERY_STATUSES)}."
    params = {"board": "1"}
    if status:
        params["status"] = status
    try:
        items = await _api(runtime).get("/deliveries", params) or []
    except CopilotAPIError as e:
        return _refused(e)
    rows = []
    for it in items:
        age = _minutes_since(it.get("created_at"))
        if waiting_more_than_min and (age is None or age <= waiting_more_than_min):
            continue
        rows.append({k: it.get(k) for k in ("id", "reference", "status", "customer_name", "truck_id")} | {"age_min": age})
    rows = rows[: max(1, min(limit, 100))]
    return wrap_data("deliveries", {"count": len(rows), "deliveries": rows})


@tool
async def get_delivery(runtime: Runtime, delivery: str) -> str:
    """One delivery in full: stops, events (with who and how), truck and ETA.

    delivery: its reference (DEL-2026-10042, or just 10042) or id.
    """
    api = _api(runtime)
    try:
        did, _ = await _delivery_id(api, delivery)
        return wrap_data("delivery", await api.get(f"/deliveries/{seg(did)}"))
    except _NotFound as e:
        return str(e)
    except CopilotAPIError as e:
        return _refused(e)


@tool
async def list_trucks(runtime: Runtime, status: str = "") -> str:
    """Trucks with status (available, on_delivery, maintenance, offline),
    driver, whether the driver is on shift, and minutes since the last
    position. status: filter, or empty for all."""
    try:
        trucks = await _api(runtime).get("/trucks") or []
    except CopilotAPIError as e:
        return _refused(e)
    out = []
    for t in trucks:
        if status and t.get("status") != status.strip().lower():
            continue
        out.append(
            {k: t.get(k) for k in ("id", "plate_number", "status", "driver_id", "driver_on_shift", "lat", "lng")}
            | {"last_seen_min": _minutes_since(t.get("last_seen_at"))}
        )
    return wrap_data("trucks", out)


@tool
async def list_drivers(runtime: Runtime) -> str:
    """Drivers with id, name, phone and whether they're on shift."""
    try:
        return wrap_data("drivers", await _api(runtime).get("/drivers"))
    except CopilotAPIError as e:
        return _refused(e)


@tool
async def nearest_trucks(runtime: Runtime, delivery: str, limit: int = 5) -> str:
    """Free trucks ranked by straight-line distance to a delivery's pickup:
    available, with a driver on shift and a known position (what auto mode
    would consider). delivery: reference or id."""
    api = _api(runtime)
    try:
        did, ref = await _delivery_id(api, delivery)
        detail = await api.get(f"/deliveries/{seg(did)}")
        trucks = await api.get("/trucks") or []
    except _NotFound as e:
        return str(e)
    except CopilotAPIError as e:
        return _refused(e)
    pickup = next((s for s in detail.get("stops", []) if s.get("stop_type") == "pickup"), None)
    if not pickup or pickup.get("lat") is None:
        return f"{ref} has no pickup position."
    ranked = []
    for t in trucks:
        if t.get("status") != "available" or not t.get("driver_id") or not t.get("driver_on_shift"):
            continue
        if t.get("lat") is None:
            continue
        km = _km(pickup["lat"], pickup["lng"], t["lat"], t["lng"])
        ranked.append({"plate_number": t["plate_number"], "id": t["id"], "km": round(km, 1),
                       "last_seen_min": _minutes_since(t.get("last_seen_at"))})
    ranked.sort(key=lambda r: r["km"])
    if not ranked:
        return f"No free truck with a driver on shift for {ref}."
    return wrap_data("nearest_trucks", {"delivery": ref, "trucks": ranked[: max(1, min(limit, 20))]})


@tool
async def list_proposals(runtime: Runtime, status: str = "pending") -> str:
    """The agent's suggestions (assign a truck, flag a problem). status:
    pending, approved, rejected or expired."""
    try:
        return wrap_data("proposals", await _api(runtime).get("/proposals", {"status": status}))
    except CopilotAPIError as e:
        return _refused(e)


@tool
async def supervisor_log(runtime: Runtime) -> str:
    """What auto mode did recently (pairings and assignments) and why."""
    try:
        return wrap_data("supervisor_actions", await _api(runtime).get("/supervisor/actions"))
    except CopilotAPIError as e:
        return _refused(e)


@tool
async def find_address(runtime: Runtime, query: str) -> str:
    """Look up a place in the service area; returns candidates with lat/lng
    to use in create_delivery."""
    if not query.strip():
        return "Give a place to look up."
    try:
        return wrap_data("places", await _api(runtime).get("/geocode", {"q": query.strip()[:200]}))
    except CopilotAPIError as e:
        return _refused(e)


@tool
async def get_settings(runtime: Runtime) -> str:
    """Operator settings that matter for dispatch: auto mode and its radius,
    the agent switch, time zone, service area and depot."""
    try:
        cfg = await _api(runtime).get("/config")
    except CopilotAPIError as e:
        return _refused(e)
    keep = ("auto_assign", "auto_radius_km", "agent_enabled", "timezone", "units",
            "min_lat", "max_lat", "min_lng", "max_lng", "depot", "operator_name")
    return compact({k: cfg.get(k) for k in keep if k in cfg})


# ---------------------------------------------------------------- writes (need approval)


class StopArg(BaseModel):
    stop_type: Literal["pickup", "dropoff"]
    address: str = Field(max_length=300)
    lat: float
    lng: float
    contact_name: str | None = Field(default=None, max_length=120)
    contact_phone: str | None = Field(default=None, max_length=40)


@tool
async def create_delivery(
    runtime: Runtime,
    customer_name: str,
    stops: list[StopArg],
    due_by: str | None = None,
    customer_phone: str | None = None,
    goods: str | None = None,
) -> str:
    """Create a delivery. stops in order: one pickup first, then drop-offs,
    each with lat/lng (use find_address). due_by: ISO 8601 time for the last
    drop-off, optional. Needs the dispatcher's approval."""
    body = {
        "customer_name": customer_name,
        "customer_phone": customer_phone,
        "goods_desc": goods,
        "stops": [s.model_dump() if isinstance(s, BaseModel) else s for s in stops],
        "due_by": due_by,
    }
    try:
        res = await _api(runtime).post("/deliveries", body, _key(runtime))
    except CopilotAPIError as e:
        return _refused(e)
    d = (res or {}).get("delivery", {})
    return f"Created {d.get('reference')} (id {d.get('id')}), status {d.get('status')}."


@tool
async def assign_truck(runtime: Runtime, delivery: str, truck: str) -> str:
    """Assign a truck to a pending delivery. delivery: reference or id.
    truck: plate number or id. Needs the dispatcher's approval."""
    api = _api(runtime)
    try:
        did, ref = await _delivery_id(api, delivery)
        t = await _truck(api, truck)
        await api.post(f"/deliveries/{seg(did)}/assign", {"truck_id": t["id"]}, _key(runtime))
    except _NotFound as e:
        return str(e)
    except CopilotAPIError as e:
        return _refused(e)
    return f"Assigned {t['plate_number']} to {ref}."


@tool
async def set_due_time(runtime: Runtime, delivery: str, due_by: str | None) -> str:
    """Change (ISO 8601) or clear (null) when a delivery is promised by.
    Needs the dispatcher's approval."""
    api = _api(runtime)
    try:
        did, ref = await _delivery_id(api, delivery)
        await api.post(f"/deliveries/{seg(did)}/due", {"due_by": due_by}, _key(runtime))
    except _NotFound as e:
        return str(e)
    except CopilotAPIError as e:
        return _refused(e)
    return f"{ref} is now due {due_by}." if due_by else f"{ref} has no due time now."


@tool
async def approve_proposals(runtime: Runtime, proposal_ids: list[str]) -> str:
    """Approve up to 10 of the agent's pending proposals at once (ids from
    list_proposals). Needs the dispatcher's approval."""
    if not proposal_ids:
        return "No proposals given."
    if len(proposal_ids) > MAX_BULK:
        return f"At most {MAX_BULK} at a time; split the list."
    api, base = _api(runtime), _key(runtime)
    done, refused = [], []
    for i, pid in enumerate(proposal_ids):
        if not is_uuid(pid):
            refused.append(f"{pid}: not a proposal id")
            continue
        try:
            await api.post(f"/proposals/{seg(pid)}/approve", {}, f"{base}:{i}")
            done.append(pid)
        except CopilotAPIError as e:
            refused.append(f"{pid}: {e.code}: {e.message}")
    return compact({"approved": done, "refused": refused})


@tool
async def reject_proposals(runtime: Runtime, proposal_ids: list[str]) -> str:
    """Reject up to 10 of the agent's pending proposals at once. Needs the
    dispatcher's approval."""
    if not proposal_ids:
        return "No proposals given."
    if len(proposal_ids) > MAX_BULK:
        return f"At most {MAX_BULK} at a time; split the list."
    api, base = _api(runtime), _key(runtime)
    done, refused = [], []
    for i, pid in enumerate(proposal_ids):
        if not is_uuid(pid):
            refused.append(f"{pid}: not a proposal id")
            continue
        try:
            await api.post(f"/proposals/{seg(pid)}/reject", {}, f"{base}:{i}")
            done.append(pid)
        except CopilotAPIError as e:
            refused.append(f"{pid}: {e.code}: {e.message}")
    return compact({"rejected": done, "refused": refused})


@tool
async def start_shift(runtime: Runtime, driver: str) -> str:
    """Start a driver's shift (name, phone or id). Only works where the
    deployment allows dispatchers to start shifts. Needs approval."""
    api = _api(runtime)
    try:
        d = await _driver(api, driver)
        await api.post(f"/drivers/{seg(d['id'])}/shift/start", {}, _key(runtime))
    except _NotFound as e:
        return str(e)
    except CopilotAPIError as e:
        return _refused(e)
    return f"{d['name']} is on shift."


@tool
async def end_shift(runtime: Runtime, driver: str) -> str:
    """End a driver's shift (name, phone or id). Refused while they have a
    delivery on the road. Needs approval."""
    api = _api(runtime)
    try:
        d = await _driver(api, driver)
        await api.post(f"/drivers/{seg(d['id'])}/shift/end", {}, _key(runtime))
    except _NotFound as e:
        return str(e)
    except CopilotAPIError as e:
        return _refused(e)
    return f"{d['name']} is off shift."


@tool
async def set_truck_status(runtime: Runtime, truck: str, status: Literal["available", "maintenance", "offline"]) -> str:
    """Put a truck in maintenance, take it offline, or make it available
    again. (on_delivery only comes from assigning.) Needs approval."""
    api = _api(runtime)
    try:
        t = await _truck(api, truck)
        await api.patch(f"/trucks/{seg(t['id'])}", {"status": status}, _key(runtime))
    except _NotFound as e:
        return str(e)
    except CopilotAPIError as e:
        return _refused(e)
    return f"{t['plate_number']} is now {status}."


@tool
async def set_auto_mode(runtime: Runtime, on: bool, radius_km: float | None = None) -> str:
    """Turn auto mode on or off, optionally with a new radius in km
    (0.1-100). Other settings stay as they are. Needs approval."""
    api = _api(runtime)
    try:
        cfg = await api.get("/config")
        # PUT /config merges into the current settings, except the depot:
        # a body without one clears it, so send it back.
        body: dict = {"auto_assign": on, "depot": cfg.get("depot")}
        if radius_km is not None:
            body["auto_radius_km"] = radius_km
        await api.put("/config", body, _key(runtime))
    except CopilotAPIError as e:
        return _refused(e)
    return f"Auto mode {'on' if on else 'off'}" + (f", radius {radius_km} km." if radius_km is not None else ".")


# ---------------------------------------------------------------- destructive (approval + reason)


@tool
async def cancel_delivery(runtime: Runtime, delivery: str, reason: str) -> str:
    """Cancel a delivery (pending, assigned, in transit or failed). reason is
    required and is kept on the delivery's history. Needs approval."""
    reason = reason.strip()
    if not reason:
        return "A reason is required to cancel. Ask the dispatcher why."
    api = _api(runtime)
    try:
        did, ref = await _delivery_id(api, delivery)
        await api.post(f"/deliveries/{seg(did)}/cancel", {"reason": reason[:300]}, _key(runtime))
    except _NotFound as e:
        return str(e)
    except CopilotAPIError as e:
        return _refused(e)
    return f"Cancelled {ref}. Reason noted: {reason[:300]}"


@tool
async def reopen_delivery(
    runtime: Runtime,
    delivery: str,
    mode: Literal["resume", "handover", "restart"],
    reason: str,
    truck: str | None = None,
) -> str:
    """Send a failed delivery out again. mode: resume (same truck carries on),
    handover (goods move to another truck: give truck), restart (start over,
    optionally on truck). Ask for a reason first: it goes in your reply to
    the dispatcher. Needs approval."""
    if not reason.strip():
        return "A reason is required. Ask the dispatcher why."
    api = _api(runtime)
    try:
        did, ref = await _delivery_id(api, delivery)
        body: dict = {"mode": mode}
        if truck:
            body["truck_id"] = (await _truck(api, truck))["id"]
        await api.post(f"/deliveries/{seg(did)}/reopen", body, _key(runtime))
    except _NotFound as e:
        return str(e)
    except CopilotAPIError as e:
        return _refused(e)
    return f"{ref} is out again ({mode})."


# ---------------------------------------------------------------- UI (moves the console only)


@tool
async def focus_map(runtime: Runtime, kind: Literal["truck", "delivery"], target: str) -> str:
    """Pan and zoom the console's map to a truck (plate) or a delivery
    (reference). Changes nothing."""
    runtime.stream_writer({"ui": {"action": "focus_map", "kind": kind, "target": target}})
    return f"Map focused on {kind} {target}."


@tool
async def open_panel(runtime: Runtime, kind: Literal["delivery", "truck", "driver"], target: str) -> str:
    """Open the console's side panel for a delivery, truck or driver.
    Changes nothing."""
    runtime.stream_writer({"ui": {"action": "open_panel", "kind": kind, "target": target}})
    return f"Opened the {kind} panel for {target}."


READ_TOOLS = [list_deliveries, get_delivery, list_trucks, list_drivers, nearest_trucks,
              list_proposals, supervisor_log, find_address, get_settings]
WRITE_TOOLS = [create_delivery, assign_truck, set_due_time, approve_proposals, reject_proposals,
               start_shift, end_shift, set_truck_status, set_auto_mode]
DESTRUCTIVE_TOOLS = [cancel_delivery, reopen_delivery]
UI_TOOLS = [focus_map, open_panel]
ALL_TOOLS = READ_TOOLS + WRITE_TOOLS + DESTRUCTIVE_TOOLS + UI_TOOLS

TIER = {t.name: "read" for t in READ_TOOLS} | {t.name: "write" for t in WRITE_TOOLS} | \
    {t.name: "destructive" for t in DESTRUCTIVE_TOOLS} | {t.name: "ui" for t in UI_TOOLS}
