"""find_problems runs at once; assign_trucks applies several assignments
on one approval, each with its own idempotency key; reassign calls the
reassign endpoint."""

import os
import uuid

os.environ.setdefault("OPENROUTER_API_KEY", "test")
os.environ.setdefault("AGENT_API_KEY", "test")

import pytest
from langchain_core.messages import AIMessage
from langgraph.checkpoint.memory import InMemorySaver
from langgraph.types import Command

from app.copilot import dispatcher_api
from app.copilot.agent import build_copilot
from app.copilot.util import new_thread_id
from tests.test_copilot_harness import call, model, run

D1, D2 = "33333333-3333-4333-8333-333333333341", "33333333-3333-4333-8333-333333333342"
T1, T2 = "11111111-1111-4111-8111-111111111101", "11111111-1111-4111-8111-111111111103"
DELIVERIES = [{"id": D1, "reference": "DEL-2026-10041", "status": "pending", "created_at": "2026-10-23T08:00:00Z"},
              {"id": D2, "reference": "DEL-2026-10042", "status": "pending", "created_at": "2026-10-23T09:00:00Z"}]
TRUCKS = [{"id": T1, "plate_number": "KF-01", "status": "available", "driver_id": "x", "driver_on_shift": True, "lat": 6.44, "lng": 3.36},
          {"id": T2, "plate_number": "LAG-915-CD", "status": "available", "driver_id": "y", "driver_on_shift": True, "lat": 6.58, "lng": 3.35}]


@pytest.fixture
def api_calls(monkeypatch):
    calls = []

    async def fake_request(self, method, path, *, json=None, params=None, idempotency_key=None):
        calls.append({"method": method, "path": path, "json": json, "key": idempotency_key})
        if path == "/problems":
            return {"count": 0, "problems": [], "free_trucks": 2}
        if path == "/deliveries":
            return DELIVERIES
        if path == "/trucks":
            return TRUCKS
        if method == "POST":
            return {"status": "assigned"}
        raise dispatcher_api.CopilotAPIError(404, "not_found", "not found")

    monkeypatch.setattr(dispatcher_api.AsDispatcher, "request", fake_request)
    return calls


async def test_find_problems_needs_no_approval(api_calls):
    agent = build_copilot(model(call("find_problems", {}), AIMessage(content="Nothing needs attention.")),
                          model(AIMessage(content="s")), InMemorySaver())
    events = await run(agent, {"messages": [{"role": "user", "content": "any problems?"}]}, new_thread_id(str(uuid.uuid4())))
    assert not [d for e, d in events if e == "interrupt"]
    assert [c["path"] for c in api_calls] == ["/problems"]


async def test_assign_trucks_is_one_approval_and_one_key_each(api_calls):
    thread = new_thread_id(str(uuid.uuid4()))
    pairs = [{"delivery": "10041", "truck": "KF-01"}, {"delivery": "10042", "truck": "LAG-915-CD"}]
    agent = build_copilot(model(call("assign_trucks", {"assignments": pairs}), AIMessage(content="Both assigned.")),
                          model(AIMessage(content="s")), InMemorySaver())
    first = await run(agent, {"messages": [{"role": "user", "content": "assign them"}]}, thread)
    (card,) = [d for e, d in first if e == "interrupt"]
    assert [a["name"] for a in card["actions"]] == ["assign_trucks"]
    assert not [c for c in api_calls if c["method"] == "POST"]

    await run(agent, Command(resume={"decisions": [{"type": "approve"}]}), thread)
    posts = [c for c in api_calls if c["method"] == "POST"]
    assert [p["path"] for p in posts] == [f"/deliveries/{D1}/assign", f"/deliveries/{D2}/assign"]
    assert [p["json"]["truck_id"] for p in posts] == [T1, T2]
    assert len({p["key"] for p in posts}) == 2


async def test_reassign_calls_the_endpoint(api_calls):
    thread = new_thread_id(str(uuid.uuid4()))
    agent = build_copilot(model(call("reassign", {"delivery": "10041", "truck": "LAG-915-CD"}), AIMessage(content="Moved.")),
                          model(AIMessage(content="s")), InMemorySaver())
    await run(agent, {"messages": [{"role": "user", "content": "move 10041 to LAG-915-CD"}]}, thread)
    await run(agent, Command(resume={"decisions": [{"type": "approve"}]}), thread)
    (post,) = [c for c in api_calls if c["method"] == "POST"]
    assert post["path"] == f"/deliveries/{D1}/reassign" and post["json"] == {"truck_id": T2}
