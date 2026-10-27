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
from .util import compact, find_by_plate, find_by_reference, idempotency_key, is_uuid, resolve_period, wrap_data

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


@tool
async def find_problems(runtime: Runtime) -> str:
    """What needs attention right now, worst first: late deliveries, ones at
    risk of being late, assigned ones that haven't started, pending ones
    waiting for a truck, failed ones not yet dealt with, trucks that have
    gone silent, and drivers on shift without a truck. Use it for "what
    should I look at?", "any problems?" or a start-of-shift check."""
    try:
        out = await _api(runtime).get("/problems")
    except CopilotAPIError as e:
        return _refused(e)
    return wrap_data("problems", out)


@tool
async def plan_assignments(runtime: Runtime, max_km: float | None = None) -> str:
    """Work out a truck for every pending delivery, without changing
    anything: oldest delivery first, each takes the nearest free truck (with
    a driver on shift and a known position) that no earlier delivery took.
    max_km: leave out pairs further apart than this. Show the plan, then
    call assign_trucks with it to apply it on one approval."""
    api = _api(runtime)
    try:
        pending = await api.get("/deliveries", {"status": "pending"}) or []
        trucks = await api.get("/trucks") or []
    except CopilotAPIError as e:
        return _refused(e)
    free = [t for t in trucks if t.get("status") == "available" and t.get("driver_id")
            and t.get("driver_on_shift") and t.get("lat") is not None]
    if not pending:
        return "Nothing is pending."
    if not free:
        return f"{len(pending)} pending, but no free truck with a driver on shift and a position."
    pending.sort(key=lambda d: d.get("created_at") or "")
    plan, unplanned, taken = [], [], set()
    for d in pending[:30]:
        try:
            detail = await api.get(f"/deliveries/{seg(d['id'])}")
        except CopilotAPIError:
            unplanned.append({"delivery": d["reference"], "why": "couldn't read it"})
            continue
        pickup = next((s for s in detail.get("stops", []) if s.get("stop_type") == "pickup"), None)
        if not pickup or pickup.get("lat") is None:
            unplanned.append({"delivery": d["reference"], "why": "no pickup position"})
            continue
        options = sorted(
            ((_km(pickup["lat"], pickup["lng"], t["lat"], t["lng"]), t) for t in free if t["id"] not in taken),
            key=lambda x: x[0],
        )
        if not options:
            unplanned.append({"delivery": d["reference"], "why": "no free truck left"})
            continue
        km, t = options[0]
        if max_km is not None and km > max_km:
            unplanned.append({"delivery": d["reference"], "why": f"nearest free truck is {km:.1f} km away"})
            continue
        taken.add(t["id"])
        plan.append({"delivery": d["reference"], "truck": t["plate_number"], "km": round(km, 1),
                     "waiting_min": _minutes_since(d.get("created_at"))})
    return wrap_data("plan", {"plan": plan, "not_planned": unplanned,
                              "next": "show this, then assign_trucks with the delivery/truck pairs (10 at most per call)"})


# ---------------------------------------------------------------- reports (read only: no approval)


async def _report(rt: Runtime, period: str, from_date: str | None, to_date: str | None) -> tuple[dict, str]:
    from datetime import datetime
    from zoneinfo import ZoneInfo

    try:
        tz = ZoneInfo(rt.context.timezone)
    except Exception:
        tz = ZoneInfo("UTC")
    a, b, label = resolve_period(period, from_date, to_date, datetime.now(tz).date())
    report = await _api(rt).get("/reports/deliveries", {"from": a, "to": b})
    return report, label


Period = Literal["today", "yesterday", "last_7_days", "last_30_days", "this_month", "last_month", "custom"]


@tool
async def delivery_stats(runtime: Runtime, period: Period = "today", from_date: str | None = None,
                         to_date: str | None = None, list_status: str | None = None) -> str:
    """Numbers for a period, from the database: deliveries created, how many
    completed / cancelled / failed / still open, on time vs late, average
    time to complete, busiest trucks and the daily counts. For "how did today
    go?" or "how many did we cancel this week?". period "custom" takes
    from_date and to_date (YYYY-MM-DD, inclusive).

    list_status: also list the deliveries themselves (reference, customer,
    truck, finished time), e.g. "completed" for "which deliveries were
    completed this week?", or "all". At most 100 are listed. The board
    (list_deliveries) only has recent ones; this covers the whole period."""
    try:
        report, label = await _report(runtime, period, from_date, to_date)
    except ValueError as e:
        return str(e)
    except CopilotAPIError as e:
        return _refused(e)
    rows = report.pop("deliveries", None) or []
    if list_status:
        want = list_status.strip().lower()
        picked = [r for r in rows if want == "all" or r.get("status") == want]
        report["listed"] = [
            {k: r.get(k) for k in ("reference", "customer_name", "status", "plate", "created_at", "finished_at")}
            for r in picked[:100]
        ]
        if len(picked) > 100:
            report["listed_note"] = f"first 100 of {len(picked)}; chart_pdf with include_table lists them all"
    report["by_truck"] = report.get("by_truck", [])[:10]
    return compact({"period": label} | report)


Chart = Literal["all", "status", "daily", "by_truck", "on_time"]


@tool
async def chart_pdf(runtime: Runtime, charts: list[Chart] | None = None, period: Period = "today",
                    from_date: str | None = None, to_date: str | None = None, include_table: bool = True,
                    title: str = "Delivery report") -> str:
    """Make a PDF report with charts for a period and give the dispatcher a
    download. ONLY when they ask for a chart, graph, PDF, report or download;
    for a list or numbers, answer in the chat with delivery_stats instead. charts: any of status (completed/cancelled/failed/open donut),
    daily (created vs completed per day), by_truck (outcomes per truck),
    on_time (on time vs late), or all (the default). include_table adds a
    table of every delivery. period "custom" takes from_date and to_date
    (YYYY-MM-DD). title: a few words without the dates, e.g. "Completed
    deliveries" (the period is printed under it and added to the file name).
    Changes nothing, so no approval is needed."""
    import asyncio
    from datetime import datetime
    from zoneinfo import ZoneInfo

    from ..reports.pdf import ReportMeta, build_pdf
    from . import files

    try:
        report, label = await _report(runtime, period, from_date, to_date)
        cfg = await _api(runtime).get("/config")
    except ValueError as e:
        return str(e)
    except CopilotAPIError as e:
        return _refused(e)
    try:
        tz = ZoneInfo(report.get("timezone") or runtime.context.timezone)
    except Exception:
        tz = ZoneInfo("UTC")
    meta = ReportMeta(title=title.strip()[:80] or "Delivery report", company=cfg.get("operator_name") or "Dispatch",
                      period_label=label, timezone=str(tz), made_at=datetime.now(tz))
    # reportlab is CPU work: off the event loop.
    data, pages = await asyncio.to_thread(build_pdf, report, meta, list(charts or ["all"]), include_table, tz)
    # A tidy file name: the title's words, then the dates.
    words = "".join(c if c.isalnum() else " " for c in meta.title).split()[:6] or ["Delivery", "report"]
    name = "_".join(words) + f"_{report['from']}" + (f"_to_{report['to']}" if report["to"] != report["from"] else "") + ".pdf"
    f = files.store.put(runtime.context.dispatcher_id or runtime.context.thread_id, name, "application/pdf", data)
    info = {"id": f.id, "name": name, "mime": "application/pdf", "size": len(data), "pages": pages,
            "url": f"/copilot/files/{f.id}"}
    runtime.stream_writer({"file": info})
    return compact({"made": name, "pages": pages, "deliveries": report["total"], "period": label,
                    "note": "The dispatcher has a download button for it; files last an hour."})


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
async def plan_reopen(runtime: Runtime, delivery: str) -> str:
    """How a failed delivery should go out again, worked out from why it
    failed and where the goods are, with the same rules as auto mode and the
    console: nothing collected → reassign to the nearest free truck; broke
    down or crashed with the goods on board → hand over to the nearest free
    truck; customer not available (truck fine) → try again with the same
    truck; goods damaged → start over from the pickup. Changes nothing.
    Then call reopen_delivery with the mode and truck it gives; don't ask
    the dispatcher for a reason, the failure is already recorded."""
    from ..proposer import PLANS, choose_plan, goods_on_truck

    api = _api(runtime)
    try:
        did, ref = await _delivery_id(api, delivery)
        detail = await api.get(f"/deliveries/{seg(did)}")
        trucks = await api.get("/trucks") or []
    except _NotFound as e:
        return str(e)
    except CopilotAPIError as e:
        return _refused(e)
    d = detail.get("delivery", {})
    if d.get("status") != "failed":
        return f"{ref} is {d.get('status')}, not failed: nothing to reopen."
    failed = next((e for e in reversed(detail.get("events", [])) if e.get("event_type") == "failed"), {})
    reason = failed.get("note")
    stops = detail.get("stops", [])
    collected = goods_on_truck(stops)
    by_id = {t["id"]: t for t in trucks}
    old = by_id.get(d.get("truck_id") or "")
    free = [t for t in trucks if t.get("status") == "available" and t.get("driver_id") and t.get("driver_on_shift")]
    plan = choose_plan(reason, collected, old, {t["id"] for t in free})
    mode, title, what = PLANS[plan]

    if plan == "try_again":
        truck, km, near_what = old, None, ""
    else:
        if plan == "handover":
            point, near_what = old, f"{old['plate_number']}'s last position"
            pool = [t for t in free if t["id"] != old["id"]]
        else:
            todo = stops if plan == "restart" else [s for s in stops if s.get("status") in ("pending", "arrived")]
            point = todo[0] if todo else None
            near_what = f"the pickup at {point['address']}" if point else ""
            pool = free
        ranked = sorted(
            ((_km(point["lat"], point["lng"], t["lat"], t["lng"]), t) for t in pool
             if point and point.get("lat") is not None and t.get("lat") is not None),
            key=lambda x: x[0],
        )
        if not ranked:
            return compact({"delivery": ref, "failed_because": reason, "goods_on_truck": collected, "plan": title,
                            "mode": mode, "truck": None,
                            "problem": "no free truck with a driver on shift and a known position for this plan"})
        km, truck = ranked[0]
    return compact({
        "delivery": ref, "plan": title, "mode": mode, "truck": truck["plate_number"],
        "km": None if km is None else round(km, 1), "failed_because": reason or "no reason given",
        "goods_on_truck": collected, "failed_truck": old["plate_number"] if old else None,
        "from": near_what or None, "what_happens": what,
        "next": "reopen_delivery with this mode and truck",
    })


class Pair(BaseModel):
    delivery: str = Field(description="Reference or id")
    truck: str = Field(description="Plate or id")


@tool
async def assign_trucks(runtime: Runtime, assignments: list[Pair]) -> str:
    """Assign several pending deliveries at once, e.g. the plan from
    plan_assignments: up to 10 delivery/truck pairs, all on one approval.
    Each goes through on its own: one refused doesn't stop the others."""
    if not assignments:
        return "No assignments given."
    if len(assignments) > MAX_BULK:
        return f"At most {MAX_BULK} at a time; split the list."
    api, base = _api(runtime), _key(runtime)
    done, refused = [], []
    for i, a in enumerate(assignments):
        pair = a if isinstance(a, Pair) else Pair(**a)
        try:
            did, ref = await _delivery_id(api, pair.delivery)
            t = await _truck(api, pair.truck)
            await api.post(f"/deliveries/{seg(did)}/assign", {"truck_id": t["id"]}, f"{base}:{i}")
            done.append(f"{ref} → {t['plate_number']}")
        except _NotFound as e:
            refused.append(f"{pair.delivery}: {e}")
        except CopilotAPIError as e:
            refused.append(f"{pair.delivery} → {pair.truck}: {e.code}: {e.message}")
    return compact({"assigned": done, "refused": refused})


@tool
async def reassign(runtime: Runtime, delivery: str, truck: str) -> str:
    """Move an assigned delivery that hasn't started to another truck, in
    one step: the old truck is freed. (Pending: use assign_truck. Started:
    it can't be moved; it would need a failure report and a handover.)
    Needs approval."""
    api = _api(runtime)
    try:
        did, ref = await _delivery_id(api, delivery)
        t = await _truck(api, truck)
        await api.post(f"/deliveries/{seg(did)}/reassign", {"truck_id": t["id"]}, _key(runtime))
    except _NotFound as e:
        return str(e)
    except CopilotAPIError as e:
        return _refused(e)
    return f"Moved {ref} to {t['plate_number']}."


class StopEditArg(BaseModel):
    stop: int = Field(ge=1, description="The stop's number on the delivery: 1 is the pickup.")
    address: str | None = Field(default=None, max_length=300, description="New address; needs lat and lng from find_address.")
    lat: float | None = None
    lng: float | None = None
    contact_name: str | None = Field(default=None, max_length=120, description='Site contact; "" removes it.')
    contact_phone: str | None = Field(default=None, max_length=40, description='Site contact phone; "" removes it.')


@tool
async def update_delivery(
    runtime: Runtime,
    delivery: str,
    customer_name: str | None = None,
    customer_phone: str | None = None,
    goods: str | None = None,
    stops: list[StopEditArg] | None = None,
) -> str:
    """Fix a delivery's details: the customer's name or phone, the goods, or a
    stop's address (with lat/lng from find_address) and site contact. Only
    while it's pending or assigned; once in transit the API refuses, and the
    way out is to fail it and create a new one (ask the dispatcher first).
    Leave out what isn't changing; "" clears a phone, the goods or a contact.
    The assigned driver is told what changed. Needs the dispatcher's approval."""
    api = _api(runtime)
    try:
        did, ref = await _delivery_id(api, delivery)
        body: dict = {}
        if customer_name is not None:
            body["customer_name"] = customer_name
        if customer_phone is not None:
            body["customer_phone"] = customer_phone
        if goods is not None:
            body["goods_desc"] = goods
        if stops:
            detail = await api.get(f"/deliveries/{seg(did)}")
            by_seq = {s.get("sequence"): s for s in (detail or {}).get("stops", [])}
            out = []
            for st in stops:
                s = st if isinstance(st, StopEditArg) else StopEditArg.model_validate(st)
                row = by_seq.get(s.stop)
                if not row:
                    return f"{ref} has no stop {s.stop}; it has {len(by_seq)}."
                out.append({"id": row["id"]} | s.model_dump(exclude={"stop"}, exclude_none=True))
            body["stops"] = out
        if not body:
            return "Nothing to change."
        res = await api.patch(f"/deliveries/{seg(did)}", body, _key(runtime))
    except _NotFound as e:
        return str(e)
    except CopilotAPIError as e:
        return _refused(e)
    changes = (res or {}).get("changes") or []
    if not changes:
        return f"{ref} already had those details; nothing changed."
    return f"Updated {ref}:\n" + wrap_data("changes", "; ".join(changes))


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
    """Start a driver's shift (name, phone or id). Only when the dispatcher
    asks outright: a shift is the driver's to start in the app (it turns on
    their phone's location sharing), and most deployments refuse this.
    Needs approval."""
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
async def give_truck_to_driver(runtime: Runtime, driver: str, truck: str) -> str:
    """Put a driver on a truck, so the truck can take deliveries. driver:
    name, phone or id. truck: plate or id; pick one with no driver from
    list_trucks (available, not on a delivery). Refused if the driver
    already drives another truck or the truck has a driver: say so and ask
    before moving anyone. Needs approval."""
    api = _api(runtime)
    try:
        d = await _driver(api, driver)
        t = await _truck(api, truck)
        trucks = await api.get("/trucks") or []
    except _NotFound as e:
        return str(e)
    except CopilotAPIError as e:
        return _refused(e)
    current = next((x for x in trucks if x.get("driver_id") == d["id"] and x["id"] != t["id"]), None)
    if current:
        return f"Not changed: {d['name']} already drives {current['plate_number']}. Ask before moving them."
    if t.get("driver_id") == d["id"]:
        return f"{d['name']} already drives {t['plate_number']}."
    if t.get("driver_id"):
        return f"Not changed: {t['plate_number']} already has a driver. Pick a truck without one."
    if t.get("status") == "on_delivery":
        return f"Not changed: {t['plate_number']} is on a delivery."
    try:
        await api.patch(f"/trucks/{seg(t['id'])}", {"driver_id": d["id"]}, _key(runtime))
    except CopilotAPIError as e:
        return _refused(e)
    shift = "" if d.get("on_shift", True) else " They're off shift, so it can't take deliveries until they start one."
    return f"{d['name']} now drives {t['plate_number']}.{shift}"


@tool
async def add_truck(runtime: Runtime, plate: str, model: str | None = None,
                    status: Literal["available", "maintenance"] = "available", driver: str | None = None) -> str:
    """Add a truck to the fleet. plate: its plate number, as written on it.
    model: e.g. "MAN TGS" (optional). status: available (default) or
    maintenance (in the workshop). driver: name, phone or id of a driver to
    put on it straight away (optional; they mustn't drive another truck).
    Check list_trucks first so you don't add a plate twice. Needs approval."""
    api = _api(runtime)
    plate = plate.strip().upper()
    if not plate:
        return "Not added: a plate number is needed."
    d = None
    if driver:
        try:
            d = await _driver(api, driver)
            trucks = await api.get("/trucks") or []
        except _NotFound as e:
            return f"Not added: {e}"
        except CopilotAPIError as e:
            return _refused(e)
        current = next((x for x in trucks if x.get("driver_id") == d["id"]), None)
        if current:
            return f"Not added: {d['name']} already drives {current['plate_number']}. Ask before moving them, or add the truck without a driver."
    try:
        t = await api.post("/trucks", {"plate_number": plate, "model": (model or "").strip() or None}, _key(runtime))
    except CopilotAPIError as e:
        return _refused(e)
    notes = []
    # The API creates every truck available; the rest are follow-up changes.
    patch: dict = {}
    if status == "maintenance":
        patch["status"] = "maintenance"
    if d:
        patch["driver_id"] = d["id"]
    if patch:
        try:
            await api.patch(f"/trucks/{seg(t['id'])}", patch, _key(runtime) + ":patch")
            if d:
                notes.append(f"{d['name']} drives it")
            if status == "maintenance":
                notes.append("it's in the workshop")
        except CopilotAPIError as e:
            notes.append(f"but setting {'the driver' if d else 'its status'} was refused ({_refused(e)})")
    extra = f"; {', '.join(notes)}" if notes else ""
    what = f" ({t['model']})" if t.get("model") else ""
    return f"Added {t['plate_number']}{what}{extra}."


@tool
async def add_driver(runtime: Runtime, name: str, phone: str, truck: str | None = None) -> str:
    """Add a driver. name: their full name. phone: their mobile number
    (local or international; the API normalises it, and it's how they sign
    in). truck: plate or id of a truck with no driver to put them on
    (optional). They get the starting PIN 1234 and the app makes them choose
    their own at first sign-in. Check list_drivers first so you don't add
    someone twice. Needs approval."""
    api = _api(runtime)
    name = " ".join(name.split())
    if not name or not phone.strip():
        return "Not added: a name and a phone number are both needed."
    t = None
    if truck:
        try:
            t = await _truck(api, truck)
        except _NotFound as e:
            return f"Not added: {e}"
        except CopilotAPIError as e:
            return _refused(e)
        if t.get("driver_id"):
            return f"Not added: {t['plate_number']} already has a driver. Pick a truck without one, or add the driver without a truck."
    try:
        d = await api.post("/drivers", {"name": name, "phone": phone}, _key(runtime))
    except CopilotAPIError as e:
        return _refused(e)
    out = f"Added {d['name']} ({d['phone']})."
    if t:
        try:
            await api.patch(f"/trucks/{seg(t['id'])}", {"driver_id": d["id"]}, _key(runtime) + ":patch")
            out += f" They drive {t['plate_number']}."
        except CopilotAPIError as e:
            out += f" Putting them on {t['plate_number']} was refused: {_refused(e)}"
    return out + (" They sign in to the driver app with their phone and the starting PIN 1234; the app then"
                  " makes them choose their own, and their shift starts once they have.")


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
    truck: str,
) -> str:
    """Send a failed delivery out again, with the plan from plan_reopen:
    mode resume (the truck carries on: the same truck when the goods are
    on it), handover (another truck collects the goods from where the
    failed one stopped) or restart (fresh goods from the pickup). truck:
    the plate that does it. No reason needed: the failure is already on
    record. Needs approval."""
    api = _api(runtime)
    try:
        did, ref = await _delivery_id(api, delivery)
        t = await _truck(api, truck)
        await api.post(f"/deliveries/{seg(did)}/reopen", {"mode": mode, "truck_id": t["id"]}, _key(runtime))
    except _NotFound as e:
        return str(e)
    except CopilotAPIError as e:
        return _refused(e)
    return f"{ref} is out again ({mode}, on {t['plate_number']})."


# ---------------------------------------------------------------- UI (moves the console only)


@tool
async def remove_truck(runtime: Runtime, truck: str) -> str:
    """Remove a truck from the fleet for good (sold, written off, returned).
    truck: plate or id. Refused while it has an assigned, in-transit or
    failed delivery: reassign or cancel that first. Finished deliveries keep
    their history. For a truck that's only off the road for a while, use
    set_truck_status (maintenance or offline) instead. Needs approval."""
    api = _api(runtime)
    try:
        t = await _truck(api, truck)
        await api.delete(f"/trucks/{seg(t['id'])}", _key(runtime))
    except _NotFound as e:
        return str(e)
    except CopilotAPIError as e:
        return _refused(e)
    return f"Removed {t['plate_number']} from the fleet."


@tool
async def remove_driver(runtime: Runtime, driver: str) -> str:
    """Remove a driver for good (they've left). driver: name, phone or id.
    Refused while they're on shift: end_shift first (with the dispatcher's
    OK). Their truck is left without a driver and they're signed out of the
    app. Their messages and shift records go too. Needs approval."""
    api = _api(runtime)
    try:
        d = await _driver(api, driver)
        trucks = await api.get("/trucks") or []
        await api.delete(f"/drivers/{seg(d['id'])}", _key(runtime))
    except _NotFound as e:
        return str(e)
    except CopilotAPIError as e:
        return _refused(e)
    had = next((x["plate_number"] for x in trucks if x.get("driver_id") == d["id"]), None)
    return f"Removed {d['name']}." + (f" {had} has no driver now: give_truck_to_driver to put someone on it." if had else "")


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
              list_proposals, supervisor_log, find_address, get_settings, delivery_stats,
              find_problems, plan_assignments, plan_reopen]
# Make a file, change nothing: no approval.
REPORT_TOOLS = [chart_pdf]
WRITE_TOOLS = [create_delivery, update_delivery, assign_truck, assign_trucks, reassign, set_due_time, approve_proposals, reject_proposals,
               start_shift, end_shift, set_truck_status, give_truck_to_driver, set_auto_mode, reopen_delivery,
               add_truck, add_driver]
DESTRUCTIVE_TOOLS = [cancel_delivery, remove_truck, remove_driver]
UI_TOOLS = [focus_map, open_panel]
ALL_TOOLS = READ_TOOLS + REPORT_TOOLS + WRITE_TOOLS + DESTRUCTIVE_TOOLS + UI_TOOLS

TIER = {t.name: "read" for t in READ_TOOLS} | {t.name: "report" for t in REPORT_TOOLS} | \
    {t.name: "write" for t in WRITE_TOOLS} | \
    {t.name: "destructive" for t in DESTRUCTIVE_TOOLS} | {t.name: "ui" for t in UI_TOOLS}
