"""Reopening: plan_reopen picks the plan from the failure (no reason asked),
and reopen_delivery sends exactly that plan after approval."""

import os
import uuid

os.environ.setdefault("OPENROUTER_API_KEY", "test")
os.environ.setdefault("AGENT_API_KEY", "test")

import pytest
from langchain_core.messages import AIMessage
from langgraph.checkpoint.memory import InMemorySaver
from langgraph.types import Command

from app.copilot import dispatcher_api
from app.copilot.agent import APPROVAL, build_copilot
from app.copilot.util import new_thread_id
from tests.test_copilot_harness import call, model, run

D = "33333333-3333-4333-8333-333333333319"
BROKEN = {"id": "11111111-1111-4111-8111-111111111745", "plate_number": "LAG-745-RN", "status": "maintenance",
          "driver_id": "a", "driver_on_shift": True, "lat": 6.50, "lng": 3.36}
NEAR = {"id": "11111111-1111-4111-8111-111111111874", "plate_number": "LAG-874-TY", "status": "available",
        "driver_id": "b", "driver_on_shift": True, "lat": 6.51, "lng": 3.36}
FAR = {"id": "11111111-1111-4111-8111-111111111903", "plate_number": "LAG-903-AB", "status": "available",
       "driver_id": "c", "driver_on_shift": True, "lat": 6.60, "lng": 3.40}
DETAIL = {
    "delivery": {"id": D, "reference": "DEL-2026-10019", "status": "failed", "truck_id": BROKEN["id"]},
    "stops": [{"stop_type": "pickup", "status": "completed", "address": "Surulere", "lat": 6.49, "lng": 3.35},
              {"stop_type": "dropoff", "status": "pending", "address": "Yaba", "lat": 6.51, "lng": 3.38}],
    "events": [{"event_type": "failed", "note": "breakdown"}],
}


@pytest.fixture
def api_calls(monkeypatch):
    calls = []

    async def fake_request(self, method, path, *, json=None, params=None, idempotency_key=None):
        calls.append({"method": method, "path": path, "json": json})
        if path == "/deliveries":
            return [{"id": D, "reference": "DEL-2026-10019", "status": "failed"}]
        if path == f"/deliveries/{D}":
            return DETAIL
        if path == "/trucks":
            return [BROKEN, NEAR, FAR]
        if method == "POST":
            return {"status": "assigned"}
        raise dispatcher_api.CopilotAPIError(404, "not_found", "not found")

    monkeypatch.setattr(dispatcher_api.AsDispatcher, "request", fake_request)
    return calls


def test_reopen_needs_no_reason_and_can_be_edited():
    assert "edit" in APPROVAL["reopen_delivery"]["allowed_decisions"]


async def test_breakdown_with_goods_on_board_is_a_handover_to_the_nearest_truck(api_calls):
    thread = new_thread_id(str(uuid.uuid4()))
    agent = build_copilot(
        model(call("plan_reopen", {"delivery": "10019"}),
              call("reopen_delivery", {"delivery": "10019", "mode": "handover", "truck": "LAG-874-TY"}, "call_2"),
              AIMessage(content="Handed over to LAG-874-TY.")),
        model(AIMessage(content="s")), InMemorySaver())
    first = await run(agent, {"messages": [{"role": "user", "content": "reopen 10019"}]}, thread)
    plan_out = next(d for e, d in first if e == "tool_end" and d["name"] == "plan_reopen")
    # The stream shows the first 240 characters of a tool's result.
    assert '"mode":"handover"' in plan_out["summary"] and '"goods_on_truck":true' in plan_out["summary"]
    assert '"failed_because":"breakdown"' in plan_out["summary"]
    (card,) = [d for e, d in first if e == "interrupt"]
    assert card["actions"][0]["name"] == "reopen_delivery"
    assert not [c for c in api_calls if c["method"] == "POST"]

    await run(agent, Command(resume={"decisions": [{"type": "approve"}]}), thread)
    (post,) = [c for c in api_calls if c["method"] == "POST"]
    assert post["path"] == f"/deliveries/{D}/reopen"
    assert post["json"] == {"mode": "handover", "truck_id": NEAR["id"]}
