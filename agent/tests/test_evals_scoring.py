"""The eval scorer itself (no model calls)."""

import os

os.environ.setdefault("OPENROUTER_API_KEY", "test")
os.environ.setdefault("AGENT_API_KEY", "test")

from evals.run_evals import score_copilot, score_supervisor


def run(calls=(), reply="", pending=None, error=None):
    return {"calls": list(calls), "reply": reply, "pending": pending, "error": error}


def passed(checks):
    return [name for name, ok, _ in checks if ok], [name for name, ok, _ in checks if not ok]


def test_approval_and_tools():
    case = {"expect": {"tools": ["nearest_trucks"],
                       "approval": {"tool": "assign_truck", "args": {"delivery": "10041", "truck": "KF-01"}}}}
    out = run(calls=[{"name": "nearest_trucks", "args": {}, "tier": "read"},
                     {"name": "assign_truck", "args": {"delivery": "DEL-2026-10041", "truck": "KF-01"}, "tier": "write"}],
              pending=[{"name": "assign_truck", "args": {"delivery": "DEL-2026-10041", "truck": "KF-01"}}])
    ok, bad = passed(score_copilot(case, out))
    assert bad == [] and set(ok) == {"tools", "approval"}


def test_no_writes_and_asks():
    case = {"expect": {"no_writes": True, "asks": True}}
    ok, bad = passed(score_copilot(case, run(reply="Which Musa: Musa Bello or Musa Ibrahim?")))
    assert bad == []
    ok, bad = passed(score_copilot(case, run(calls=[{"name": "end_shift", "args": {}, "tier": "write"}],
                                             pending=[{"name": "end_shift", "args": {}}], reply="Ending it.")))
    assert set(bad) == {"no_writes", "asks"}


def test_supervisor_scoring():
    case = {"table": "- DEL-1 | KF-1 0.1 km; KF-2 1 km\n- DEL-2 | KF-1 0.2 km; KF-2 2 km",
            "accept": [[["DEL-1", "KF-1"], ["DEL-2", "KF-2"]]]}
    ok, bad = passed(score_supervisor(case, [("DEL-1", "KF-1", "closest"), ("DEL-2", "KF-2", "next")], None))
    assert bad == []
    ok, bad = passed(score_supervisor(case, [("DEL-1", "KF-1", "x"), ("DEL-2", "KF-1", "x")], None))
    assert "truck_once" in bad and "assignment" in bad
    ok, bad = passed(score_supervisor(case, None, "TimeoutError"))
    assert bad == ["ran"]
