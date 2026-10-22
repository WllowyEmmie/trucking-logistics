from types import SimpleNamespace as NS

import pytest

from app.copilot.stream import interrupt_event, to_resume, translate

TIERS = {"get_delivery": "read", "cancel_delivery": "destructive", "assign_truck": "write"}


def events(mode, chunk):
    return list(translate(mode, chunk, TIERS))


def test_tokens_only_from_the_model_node():
    chunk = NS(type="AIMessageChunk", content="Three are waiting")
    assert events("messages", (chunk, {"langgraph_node": "model"})) == [("token", {"text": "Three are waiting"})]
    assert events("messages", (chunk, {"langgraph_node": "SummarizationMiddleware.before_model"})) == []


def test_tool_calls_and_results():
    ai = NS(type="ai", content="", tool_calls=[{"id": "c1", "name": "get_delivery", "args": {"delivery": "10042"}}])
    out = events("updates", {"model": {"messages": [ai]}})
    assert out == [("tool_start", {"id": "c1", "name": "get_delivery", "args": {"delivery": "10042"}, "tier": "read"})]

    tm = NS(type="tool", content="Refused (truck_busy): truck KF-02 is on_delivery", tool_call_id="c2",
            name="assign_truck", status="success")
    (ev, data), = events("updates", {"tools": {"messages": [tm]}})
    assert ev == "tool_end" and data["ok"] is False and data["id"] == "c2"


def test_final_reply_is_sent_whole():
    ai = NS(type="ai", content=[{"type": "text", "text": "Cancelled DEL-2026-10042."}], tool_calls=[])
    assert events("updates", {"model": {"messages": [ai]}}) == [("message", {"text": "Cancelled DEL-2026-10042."})]


def test_ui_events_pass_through():
    ui = {"action": "focus_map", "kind": "truck", "target": "KF-02"}
    assert events("custom", {"ui": ui}) == [("ui", ui)]
    assert events("custom", {"other": 1}) == []


def _interrupt():
    return NS(id="i1", value={
        "action_requests": [{"name": "cancel_delivery", "args": {"delivery": "10042", "reason": "customer called"},
                             "description": "Needs your approval"}],
        "review_configs": [{"action_name": "cancel_delivery", "allowed_decisions": ["approve", "reject"]}],
    })


def test_interrupts_become_approval_cards():
    (ev, data), = events("updates", {"__interrupt__": (_interrupt(),)})
    assert ev == "interrupt"
    assert data["id"] == "i1"
    a = data["actions"][0]
    assert a["name"] == "cancel_delivery" and a["tier"] == "destructive"
    assert a["allowed_decisions"] == ["approve", "reject"]


def test_resume_decisions():
    pending = interrupt_event(_interrupt(), TIERS)
    assert to_resume([{"type": "approve"}], pending) == {"decisions": [{"type": "approve"}]}
    assert to_resume([{"type": "reject", "message": "wrong one"}], pending) == {
        "decisions": [{"type": "reject", "message": "wrong one"}]}
    with pytest.raises(ValueError):
        to_resume([{"type": "edit", "args": {}}], pending)  # not allowed for a destructive action
    with pytest.raises(ValueError):
        to_resume([], pending)


def test_edit_carries_the_tool_name():
    pending = {"actions": [{"name": "assign_truck", "allowed_decisions": ["approve", "edit", "reject"]}]}
    out = to_resume([{"type": "edit", "args": {"delivery": "10042", "truck": "KF-07"}}], pending)
    assert out == {"decisions": [{"type": "edit", "edited_action": {
        "name": "assign_truck", "args": {"delivery": "10042", "truck": "KF-07"}}}]}
