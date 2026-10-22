"""The harness end to end with a scripted model and a fake API: reads run at
once, changes wait for approval, a rejection never reaches the API, and an
approved change goes out once, as the dispatcher, labelled as the Copilot.

Needs the agent's dependencies (pip install -e ".[dev]"); no network, no
model, no database. The settings module wants these two variables."""

import os
import uuid

os.environ.setdefault("OPENROUTER_API_KEY", "test")
os.environ.setdefault("AGENT_API_KEY", "test")

import pytest
from langchain_core.language_models.fake_chat_models import GenericFakeChatModel
from langchain_core.messages import AIMessage
from langgraph.checkpoint.memory import InMemorySaver
from langgraph.types import Command

from app.copilot import dispatcher_api
from app.copilot.agent import build_copilot
from app.copilot.context import CopilotContext
from app.copilot.stream import interrupt_event, to_resume, translate
from app.copilot.tools import TIER
from app.copilot.util import new_thread_id

DELIVERY_ID = str(uuid.uuid4())
DELIVERIES = [{"id": DELIVERY_ID, "reference": "DEL-2026-10042", "status": "pending",
               "customer_name": "Chidi", "truck_id": None, "created_at": "2026-10-22T09:00:00Z"}]


class ScriptedModel(GenericFakeChatModel):
    """Replies from a list; tools are 'bound' by ignoring them."""

    def bind_tools(self, tools, **kwargs):
        return self


def model(*replies):
    # disable_streaming: the fake model's streaming path drops tool calls.
    return ScriptedModel(messages=iter(list(replies)), disable_streaming=True)


def call(name, args, cid="call_1"):
    return AIMessage(content="", tool_calls=[{"id": cid, "name": name, "args": args, "type": "tool_call"}])


@pytest.fixture
def api_calls(monkeypatch):
    """Every request the tools make, answered by a tiny fake API."""
    calls = []

    async def fake_request(self, method, path, *, json=None, params=None, idempotency_key=None):
        calls.append({"method": method, "path": path, "json": json, "key": idempotency_key,
                      "headers": self._headers(idempotency_key)})
        if method == "GET" and path == "/deliveries":
            return DELIVERIES
        if method == "POST" and path.endswith("/cancel"):
            return {"status": "cancelled"}
        raise dispatcher_api.CopilotAPIError(404, "not_found", "not found")

    monkeypatch.setattr(dispatcher_api.AsDispatcher, "request", fake_request)
    return calls


async def run(agent, payload, thread):
    ctx = CopilotContext(token="dispatcher-session", thread_id=thread, dispatcher_name="Ada")
    out = []
    async for mode, chunk in agent.astream(payload, {"configurable": {"thread_id": thread}}, context=ctx,
                                           stream_mode=["updates", "messages", "custom"]):
        out.extend(translate(mode, chunk, TIER))
    return out


def names(events, kind):
    return [d for e, d in events if e == kind]


async def test_read_runs_without_approval(api_calls):
    agent = build_copilot(model(call("list_deliveries", {"status": "pending"}), AIMessage(content="One is waiting.")),
                          model(AIMessage(content="summary")), InMemorySaver())
    events = await run(agent, {"messages": [{"role": "user", "content": "what's waiting?"}]},
                       new_thread_id(str(uuid.uuid4())))
    assert not names(events, "interrupt")
    assert [c["path"] for c in api_calls] == ["/deliveries"]
    assert names(events, "message")[-1]["text"] == "One is waiting."


async def test_cancel_waits_for_approval_then_runs_once(api_calls):
    thread = new_thread_id(str(uuid.uuid4()))
    agent = build_copilot(
        model(call("cancel_delivery", {"delivery": "10042", "reason": "customer called it off"}),
              AIMessage(content="Cancelled DEL-2026-10042.")),
        model(AIMessage(content="summary")), InMemorySaver())

    first = await run(agent, {"messages": [{"role": "user", "content": "cancel 10042, customer called it off"}]}, thread)
    (card,) = names(first, "interrupt")
    assert card["actions"][0]["name"] == "cancel_delivery"
    assert card["actions"][0]["tier"] == "destructive"
    assert api_calls == [], "nothing may reach the API before approval"

    pending = interrupt_event(type("I", (), {"value": {
        "action_requests": [{"name": a["name"], "args": a["args"]} for a in card["actions"]],
        "review_configs": [{"action_name": a["name"], "allowed_decisions": a["allowed_decisions"]} for a in card["actions"]],
    }, "id": card["id"]})(), TIER)
    second = await run(agent, Command(resume=to_resume([{"type": "approve"}], pending)), thread)

    posts = [c for c in api_calls if c["method"] == "POST"]
    assert len(posts) == 1
    assert posts[0]["path"] == f"/deliveries/{DELIVERY_ID}/cancel"
    assert posts[0]["json"] == {"reason": "customer called it off"}
    h = posts[0]["headers"]
    assert h["Authorization"] == "Bearer dispatcher-session"
    assert h["X-Via"] == "copilot" and h["X-Copilot-Thread"] == thread
    assert h["Idempotency-Key"].startswith("cp:")
    assert names(second, "message")[-1]["text"] == "Cancelled DEL-2026-10042."


async def test_rejected_change_never_reaches_the_api(api_calls):
    thread = new_thread_id(str(uuid.uuid4()))
    agent = build_copilot(
        model(call("cancel_delivery", {"delivery": "10042", "reason": "wrong"}), AIMessage(content="Left it as it is.")),
        model(AIMessage(content="summary")), InMemorySaver())
    await run(agent, {"messages": [{"role": "user", "content": "cancel 10042"}]}, thread)
    await run(agent, Command(resume={"decisions": [{"type": "reject", "message": "not that one"}]}), thread)
    assert not [c for c in api_calls if c["method"] == "POST"]
