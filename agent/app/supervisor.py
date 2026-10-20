"""Auto mode: the agent supervises instead of proposing.

Each pass, while auto mode is on in Settings:

1. Pair. A driver who has started a shift but has no truck gets a free
   truck (available, no driver). Their phone has no position until they have
   a truck, so the truck parked nearest the depot is the one they're most
   likely standing next to.
2. Assign. Each pending delivery, oldest first, gets a ready truck (available,
   driver on shift): the nearest within the auto mode distance of the pickup,
   otherwise the nearest anywhere.

The arithmetic (distances, who is free) is done here. The model is asked
only when there's a real choice to make: two deliveries whose nearest truck
is the same one. It picks from the options in a table and gives its reason;
anything it gets wrong, or leaves out, the nearest-truck rule fills in. If
the model is down, the rule does everything, so a delivery never waits on it.

The API checks every action again (auto mode on, driver on shift, truck
free) and records it, so the console can list it and a dispatcher undo it.
"""

import asyncio
import logging
import math
from dataclasses import dataclass, field
from datetime import datetime, timezone

from .client import APIError
from .timefmt import duration
from .tools import api

log = logging.getLogger("app.supervisor")

# Deliveries per pass, and trucks shown to the model per delivery.
MAX_DELIVERIES = 20
OPTIONS_SHOWN = 3


@dataclass
class Option:
    truck: dict
    km: float | None  # None: the truck has no position yet

    @property
    def plate(self) -> str:
        return self.truck["plate_number"]


@dataclass
class Row:
    """A pending delivery and every ready truck for it, best first."""

    d: dict
    pickup: dict
    # Nearest first, so the ones within the auto mode distance come first;
    # the rest are there for when those are taken.
    options: list[Option]
    # At least one option is within the auto mode distance.
    near: bool = False


@dataclass
class Action:
    kind: str  # "pair" or "assign"
    body: dict
    label: str
    decided_by: str = "rule"
    extra: dict = field(default_factory=dict)


# ---- 1. pairing ----


def plan_pairs(trucks: list[dict], drivers: list[dict], depot: dict | None) -> list[Action]:
    """Drivers on shift with no truck, longest on shift first, each with the
    free truck nearest the depot (trucks with no position last)."""
    driving = {t["driver_id"] for t in trucks if t.get("driver_id")}
    waiting = [d for d in drivers if d.get("on_shift") and d["id"] not in driving]
    waiting.sort(key=lambda d: d.get("shift_since") or "")
    free = [t for t in trucks if t["status"] == "available" and not t.get("driver_id")]

    def depot_km(t: dict) -> float:
        if not depot or t.get("lat") is None or t.get("lng") is None:
            return math.inf
        return km(t, depot)

    free.sort(key=lambda t: (depot_km(t), t["plate_number"]))
    out = []
    for driver, truck in zip(waiting, free):
        where = depot_km(truck)
        at = (
            "parked at the depot" if where <= 0.2
            else f"parked {where:.1f} km from the depot" if where != math.inf
            else "free"
        )
        out.append(Action(
            "pair",
            {"driver_id": driver["id"], "truck_id": truck["id"],
             "reason": f"{driver['name']} is on shift with no truck; {truck['plate_number']} is {at}.",
             "decided_by": "rule"},
            f"{truck['plate_number']} to {driver['name']}",
            extra={"truck": truck, "driver": driver},
        ))
    return out


# ---- 2. assigning ----


def ready_trucks(trucks: list[dict]) -> list[dict]:
    return [t for t in trucks if t["status"] == "available" and t.get("driver_id") and t.get("driver_on_shift")]


def build_rows(deliveries: list[dict], pickups: list[dict | None], trucks: list[dict], radius_km: float) -> list[Row]:
    rows = []
    for d, p in zip(deliveries, pickups):
        if p is None:
            continue
        opts = [Option(t, km(t, p) if t.get("lat") is not None and t.get("lng") is not None else None) for t in trucks]
        if not opts:
            continue
        opts.sort(key=lambda o: (o.km is None, o.km if o.km is not None else 0, o.plate))
        near = any(o.km is not None and o.km <= radius_km for o in opts)
        rows.append(Row(d, p, opts, near=near))
    return rows


def contested(rows: list[Row]) -> bool:
    """Two deliveries whose first choice is the same truck: worth a model call.
    Otherwise every delivery simply gets its nearest truck."""
    firsts = [r.options[0].truck["id"] for r in rows if r.options]
    return len(firsts) != len(set(firsts))


def rule_reason(o: Option, row: Row, radius_km: float) -> str:
    if o.km is None:
        return f"No ready truck with a known position is free; {o.plate} hasn't reported one yet."
    where = f"{o.km:.1f} km from the pickup at {row.pickup['address']}"
    if o.km <= radius_km:
        return f"Nearest ready truck: {where}."
    if row.near:
        return f"Nearest ready truck left: {where} (the ones within {radius_km:g} km went to deliveries waiting longer)."
    return f"Nearest ready truck: {where} (none within {radius_km:g} km)."


def plan_assignments(rows: list[Row], picks: list, radius_km: float) -> list[Action]:
    """The model's picks that hold up, then the rule for everything else.

    `picks` are (reference, plate, why) from the model, possibly empty. A
    pick is kept only if the plate is one of that row's options and neither
    the truck nor the delivery is already taken.
    """
    by_ref = {r.d["reference"]: r for r in rows}
    used_trucks: set[str] = set()
    chosen: dict[str, Action] = {}
    for ref, plate, why in picks:
        row = by_ref.get(ref)
        if row is None or ref in chosen:
            log.info("supervisor: dropped the model's %s for %s: not a choice it was given", plate, ref)
            continue
        opt = next((o for o in row.options if o.plate == plate), None)
        if opt is None or opt.truck["id"] in used_trucks:
            log.info("supervisor: dropped the model's %s for %s: not one of its options, or taken", plate, ref)
            continue
        used_trucks.add(opt.truck["id"])
        chosen[ref] = _assign(row, opt, (why or "").strip() or rule_reason(opt, row, radius_km), "model")

    # Oldest first: whoever has waited longest gets the nearest truck left.
    for row in rows:
        ref = row.d["reference"]
        if ref in chosen:
            continue
        opt = next((o for o in row.options if o.truck["id"] not in used_trucks), None)
        if opt is None:
            continue
        used_trucks.add(opt.truck["id"])
        chosen[ref] = _assign(row, opt, rule_reason(opt, row, radius_km), "rule")
    return [chosen[r.d["reference"]] for r in rows if r.d["reference"] in chosen]


def _assign(row: Row, o: Option, reason: str, by: str) -> Action:
    return Action(
        "assign",
        {"delivery_id": row.d["id"], "truck_id": o.truck["id"], "reason": reason[:500], "decided_by": by},
        f"{row.d['reference']} to {o.plate}",
        decided_by=by,
    )


def table(rows: list[Row], radius_km: float, now: datetime) -> str:
    lines = [
        "Pending deliveries, oldest first. Each lists its nearest ready trucks (free, "
        f"driver on shift); 'near' means at least one is within {radius_km:g} km of the "
        "pickup. Assign every delivery you can, each truck once:"
    ]
    for r in rows:
        opts = "; ".join(
            f"{o.plate} " + (f"{o.km:.1f} km" if o.km is not None else "no position yet")
            + (f", position {ago(o.truck.get('last_seen_at'), now)} old" if o.km is not None else "")
            for o in r.options[:OPTIONS_SHOWN]
        )
        lines.append(
            f"- {r.d['reference']} | {'near' if r.near else 'nothing near'} | waiting "
            f"{ago(r.d.get('created_at'), now)} | pickup {r.pickup['address']} | {opts}"
        )
    return "\n".join(lines)


# ---- the pass ----


async def run_supervisor(decide=None) -> dict:
    """One pass. `decide(table) -> [(reference, plate, why)]` is the model
    call; None, or a failure, leaves it all to the rule. Never raises for an
    expected problem: it runs on every change and on a timer."""
    try:
        cfg = await api.get_config()
    except APIError as e:
        return {"status": "error", "error": e.message}
    if not cfg.get("auto_assign") or cfg.get("agent_enabled") is False:
        return {"status": "off"}
    radius = float(cfg.get("auto_radius_km") or 2)
    try:
        trucks, drivers, pending = await asyncio.gather(
            api.list_trucks(), api.list_drivers(), api.list_deliveries("pending"))
    except APIError as e:
        return {"status": "error", "error": e.message}

    done: list[str] = []
    refused: list[str] = []

    # 1. Drivers waiting for a truck.
    for a in plan_pairs(trucks, drivers, cfg.get("depot")):
        if await _send(a, done, refused):
            a.extra["truck"]["driver_id"] = a.body["driver_id"]
            a.extra["truck"]["driver_on_shift"] = True

    # 2. Deliveries waiting for a truck.
    ready = ready_trucks(trucks)
    if pending and ready:
        pending.sort(key=lambda d: d.get("created_at") or "")
        todo = pending[:MAX_DELIVERIES]
        pickups = await asyncio.gather(*(_pickup(d["id"]) for d in todo))
        rows = build_rows(todo, pickups, ready, radius)
        picks = []
        if rows and decide is not None and contested(rows):
            try:
                picks = await decide(table(rows, radius, datetime.now(timezone.utc)))
            except Exception as e:  # the model timed out, or its answer didn't parse
                log.warning("supervisor: model call failed, using the nearest-truck rule: %r", e)
        for a in plan_assignments(rows, picks, radius):
            await _send(a, done, refused)

    if not done and not refused:
        return {"status": "idle"}
    return {"status": "ok", "done": done, "refused": refused}


async def _send(a: Action, done: list, refused: list) -> bool:
    try:
        await api.supervise(a.kind, a.body)
    except APIError as e:
        # Someone else got there first (a dispatcher assigned it, the driver
        # went off shift). The next pass sees the new state.
        refused.append(f"{a.kind} {a.label}: {e.message}")
        log.info("supervisor: %s %s refused: %s", a.kind, a.label, e.message)
        return False
    done.append(f"{a.kind} {a.label} ({a.body['decided_by']})")
    log.info("supervisor: %s %s by %s: %s", a.kind, a.label, a.body["decided_by"], a.body["reason"])
    return True


async def _pickup(delivery_id: str) -> dict | None:
    try:
        detail = await api.get_delivery(delivery_id)
    except APIError as e:
        log.info("supervisor: no detail for %s: %s", delivery_id, e.message)
        return None
    stop = next((s for s in detail["stops"] if s["stop_type"] == "pickup"), None)
    if not stop or stop.get("lat") is None or stop.get("lng") is None:
        return None
    return stop


def km(a: dict, b: dict) -> float:
    lat1, lng1, lat2, lng2 = map(math.radians, (a["lat"], a["lng"], b["lat"], b["lng"]))
    h = math.sin((lat2 - lat1) / 2) ** 2 + math.cos(lat1) * math.cos(lat2) * math.sin((lng2 - lng1) / 2) ** 2
    return 2 * 6371 * math.asin(math.sqrt(h))


def ago(iso: str | None, now: datetime) -> str:
    if not iso:
        return "unknown"
    return duration(max(0.0, (now - datetime.fromisoformat(iso)).total_seconds()) / 60)
