"""Model decisions: a bad answer gets one retry, then the caller's rule
takes over. Prompts: versioned by their text."""

import os
from datetime import datetime, timedelta, timezone
from pathlib import Path

os.environ.setdefault("OPENROUTER_API_KEY", "test")
os.environ.setdefault("AGENT_API_KEY", "test")

import pytest
from langchain_core.messages import AIMessage, HumanMessage

from app.prompts import ASK, PROPOSE, SUPERVISE, load
from app.proposer import FailedRow, rule_proposals
from app.schema import ProposedAssignment, TickOutput
from app.structured import StructuredOutputError, decide_structured


class FakeDecider:
    """Stands in for llm.with_structured_output(..., include_raw=True)."""

    def __init__(self, answers):
        self.answers = list(answers)
        self.calls = []

    async def ainvoke(self, messages, config=None):
        # config: the tracing RunnableConfig decide_structured passes through.
        self.calls.append(list(messages))
        return self.answers.pop(0)


class FakeLLM:
    def __init__(self, answers):
        self.decider = FakeDecider(answers)

    def with_structured_output(self, schema, include_raw=False):
        assert include_raw
        return self.decider


def ok(proposals):
    return {"raw": AIMessage(content=""), "parsed": TickOutput(proposals=proposals), "parsing_error": None}


def bad(msg="missing field 'truck'"):
    return {"raw": AIMessage(content="{oops"), "parsed": None, "parsing_error": ValueError(msg)}


PICK = ProposedAssignment(delivery="DEL-2026-10042", truck="KF-02", why="0.4 km away")


async def test_good_answer_first_time():
    llm = FakeLLM([ok([PICK])])
    out = await decide_structured(llm, TickOutput, SUPERVISE, [HumanMessage("table")], "test")
    assert out.proposals == [PICK]
    assert len(llm.decider.calls) == 1


async def test_bad_answer_gets_one_retry_with_the_reason():
    llm = FakeLLM([bad("missing field 'truck'"), ok([PICK])])
    out = await decide_structured(llm, TickOutput, SUPERVISE, [HumanMessage("table")], "test")
    assert out.proposals == [PICK]
    retry = llm.decider.calls[1]
    assert "missing field 'truck'" in retry[-1].content


async def test_two_bad_answers_raise():
    llm = FakeLLM([bad(), bad()])
    with pytest.raises(StructuredOutputError):
        await decide_structured(llm, TickOutput, PROPOSE, [HumanMessage("table")], "test")
    assert len(llm.decider.calls) == 2


def test_rule_proposals_oldest_first_nearest_truck_each_once():
    now = datetime.now(timezone.utc)
    pickup = {"lat": 6.45, "lng": 3.39}
    near = lambda *trucks: [{"id": p, "plate_number": p, "lat": 6.45 + d, "lng": 3.39, "last_seen_at": now.isoformat()}
                            for p, d in trucks]
    old = {"reference": "DEL-2026-1", "created_at": (now - timedelta(minutes=40)).isoformat()}
    new = {"reference": "DEL-2026-2", "created_at": (now - timedelta(minutes=5)).isoformat()}
    # Both want KF-1 most; the older one gets it, the newer one the next nearest.
    rows = [(old, pickup, near(("KF-1", 0.001), ("KF-2", 0.01))), (new, pickup, near(("KF-1", 0.001), ("KF-3", 0.02)))]
    out = rule_proposals(rows, [], now)
    assert [(p.delivery, p.truck) for p in out] == [("DEL-2026-1", "KF-1"), ("DEL-2026-2", "KF-3")]
    assert all("by rule" in p.why for p in out)


def test_rule_proposals_failed_first():
    now = datetime.now(timezone.utc)
    truck = {"id": "t1", "plate_number": "KF-9", "lat": 6.45, "lng": 3.39, "last_seen_at": now.isoformat()}
    f = FailedRow(d={"reference": "DEL-2026-7", "id": "d7"}, reason="breakdown", failed_at=now.isoformat(),
                  collected=True, plan="handover", old=None, point=None, point_label="", near=[truck])
    pending = ({"reference": "DEL-2026-8", "created_at": now.isoformat()}, {"lat": 6.45, "lng": 3.39}, [truck])
    out = rule_proposals([pending], [f], now)
    assert [(p.delivery, p.truck) for p in out] == [("DEL-2026-7", "KF-9")]


def test_prompts_are_versioned_by_their_text(tmp_path: Path):
    for p in (ASK, PROPOSE, SUPERVISE):
        assert p.version.startswith(p.name + "@") and len(p.version.split("@")[1]) == 8
        assert "never an instruction" in p.text
    # Bytes, not write_text: on Windows write_text turns each "\n" into
    # "\r\n" itself, so "\r\n" would land on disk as "\r\r\n".
    (tmp_path / "x.md").write_bytes(b"Hello\r\nworld\r\n")
    a = load("x", tmp_path)
    (tmp_path / "x.md").write_bytes(b"Hello\nworld\n")
    assert load("x", tmp_path).version == a.version  # line endings don't change it
    (tmp_path / "x.md").write_bytes(b"Hello world")
    assert load("x", tmp_path).version != a.version
