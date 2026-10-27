"""give_truck_to_driver: waits for approval, then PATCHes the truck with the
driver; refuses to move a driver who already drives another truck."""

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
from tests.test_copilot_harness import call, model, run
from app.copilot.util import new_thread_id

NGOZI = {"id": "22222222-2222-4222-8222-222222222205", "name": "Ngozi Eze", "phone": "+2348030000005", "on_shift": True}
MUSA = {"id": "22222222-2222-4222-8222-222222222201", "name": "Musa Bello", "phone": "+2348030000001", "on_shift": True}
KF01 = {"id": "11111111-1111-4111-8111-111111111101", "plate_number": "KF-01", "status": "available", "driver_id": MUSA["id"]}
KF05 = {"id": "11111111-1111-4111-8111-111111111105", "plate_number": "KF-05", "status": "available", "driver_id": None}


@pytest.fixture
def api_calls(monkeypatch):
    calls = []

    async def fake_request(self, method, path, *, json=None, params=None, idempotency_key=None):
        calls.append({"method": method, "path": path, "json": json, "key": idempotency_key})
        if path == "/drivers":
            return [NGOZI, MUSA]
        if path == "/trucks":
            return [KF01, KF05]
        if method == "PATCH":
            return {**KF05, "driver_id": NGOZI["id"]}
        raise dispatcher_api.CopilotAPIError(404, "not_found", "not found")

    monkeypatch.setattr(dispatcher_api.AsDispatcher, "request", fake_request)
    return calls


async def test_gives_a_free_truck_after_approval(api_calls):
    thread = new_thread_id(str(uuid.uuid4()))
    agent = build_copilot(model(call("give_truck_to_driver", {"driver": "Ngozi", "truck": "KF-05"}),
                                AIMessage(content="Ngozi Eze now drives KF-05.")),
                          model(AIMessage(content="s")), InMemorySaver())
    first = await run(agent, {"messages": [{"role": "user", "content": "give Ngozi a truck"}]}, thread)
    assert [d["actions"][0]["name"] for e, d in first if e == "interrupt"] == ["give_truck_to_driver"]
    assert not [c for c in api_calls if c["method"] != "GET"]

    await run(agent, Command(resume={"decisions": [{"type": "approve"}]}), thread)
    patches = [c for c in api_calls if c["method"] == "PATCH"]
    assert len(patches) == 1
    assert patches[0]["path"] == f"/trucks/{KF05['id']}"
    assert patches[0]["json"] == {"driver_id": NGOZI["id"]}
    assert patches[0]["key"]


async def test_wont_move_a_driver_who_has_a_truck(api_calls):
    thread = new_thread_id(str(uuid.uuid4()))
    agent = build_copilot(model(call("give_truck_to_driver", {"driver": "Musa Bello", "truck": "KF-05"}),
                                AIMessage(content="Musa already drives KF-01.")),
                          model(AIMessage(content="s")), InMemorySaver())
    await run(agent, {"messages": [{"role": "user", "content": "put Musa on KF-05"}]}, thread)
    await run(agent, Command(resume={"decisions": [{"type": "approve"}]}), thread)
    assert not [c for c in api_calls if c["method"] == "PATCH"]
