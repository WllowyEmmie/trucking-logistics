"""Reports: periods, the PDF itself, the file store, and the chart_pdf tool
end to end (scripted model, fake API)."""

import os
import uuid
from datetime import date, datetime, timezone

os.environ.setdefault("OPENROUTER_API_KEY", "test")
os.environ.setdefault("AGENT_API_KEY", "test")

import pytest
from langchain_core.messages import AIMessage
from langgraph.checkpoint.memory import InMemorySaver

from app.copilot import dispatcher_api, files
from app.copilot.agent import build_copilot
from app.copilot.util import new_thread_id, resolve_period
from app.reports.pdf import ReportMeta, build_pdf
from tests.test_copilot_harness import call, model, run

REPORT = {
    "from": "2026-10-20", "to": "2026-10-22", "timezone": "Africa/Lagos", "total": 3,
    "by_status": {"pending": 0, "assigned": 0, "in_transit": 0, "completed": 2, "cancelled": 1, "failed": 0},
    "on_time": {"on_time": 1, "late": 1, "no_due": 0}, "avg_complete_min": 95.0,
    "by_day": [{"date": "2026-10-20", "created": 2, "completed": 1, "cancelled": 0, "failed": 0},
               {"date": "2026-10-21", "created": 1, "completed": 1, "cancelled": 1, "failed": 0},
               {"date": "2026-10-22", "created": 0, "completed": 0, "cancelled": 0, "failed": 0}],
    "by_truck": [{"plate": "KF-01", "completed": 2, "failed": 0, "cancelled": 0, "open": 0}],
    "deliveries": [{"reference": "DEL-2026-1", "customer_name": "Ada", "status": "completed",
                    "created_at": "2026-10-20T09:00:00+01:00", "finished_at": "2026-10-20T11:00:00+01:00", "plate": "KF-01"}],
    "truncated": False,
}


def test_periods():
    today = date(2026, 10, 22)
    assert resolve_period("today", None, None, today)[:2] == ("2026-10-22", "2026-10-22")
    assert resolve_period("last_7_days", None, None, today)[:2] == ("2026-10-16", "2026-10-22")
    assert resolve_period("last_month", None, None, today)[:2] == ("2026-09-01", "2026-09-30")
    assert resolve_period("custom", "2026-10-05", "2026-10-01", today)[:2] == ("2026-10-01", "2026-10-05")
    with pytest.raises(ValueError):
        resolve_period("custom", "2026-01-01", "2026-12-31", today)
    with pytest.raises(ValueError):
        resolve_period("fortnight", None, None, today)


def test_pdf_builds_every_chart():
    meta = ReportMeta("Delivery report", "Dispatch", "20–22 Oct 2026", "Africa/Lagos", datetime.now(timezone.utc))
    data, pages = build_pdf(REPORT, meta, ["all"], True, timezone.utc)
    assert data.startswith(b"%PDF") and pages >= 1
    one, _ = build_pdf(REPORT, meta, ["status"], False, timezone.utc)
    assert one.startswith(b"%PDF") and len(one) < len(data)
    empty = dict(REPORT, total=0, by_truck=[], deliveries=[],
                 by_status={k: 0 for k in REPORT["by_status"]}, on_time={"on_time": 0, "late": 0, "no_due": 0})
    assert build_pdf(empty, meta, ["all"], True, timezone.utc)[0].startswith(b"%PDF")


def test_files_belong_to_their_owner():
    store = files.FileStore()
    f = store.put("ada", "r.pdf", "application/pdf", b"%PDF-1")
    assert store.get(f.id, "ada").data == b"%PDF-1"
    assert store.get(f.id, "bisi") is None
    assert store.get("nope", "ada") is None


@pytest.fixture
def fake_api(monkeypatch):
    async def fake_request(self, method, path, *, json=None, params=None, idempotency_key=None):
        if path == "/reports/deliveries":
            return dict(REPORT, deliveries=list(REPORT["deliveries"]))
        if path == "/config":
            return {"operator_name": "Dispatch Test", "timezone": "Africa/Lagos"}
        raise dispatcher_api.CopilotAPIError(404, "not_found", "not found")

    monkeypatch.setattr(dispatcher_api.AsDispatcher, "request", fake_request)


async def test_chart_pdf_tool_makes_a_file_without_approval(fake_api):
    thread = new_thread_id(str(uuid.uuid4()))
    agent = build_copilot(model(call("chart_pdf", {"charts": ["status"], "period": "last_7_days"}),
                                AIMessage(content="Your report is ready.")),
                          model(AIMessage(content="s")), InMemorySaver())
    events = await run(agent, {"messages": [{"role": "user", "content": "chart this week as a pdf"}]}, thread)
    assert not [d for e, d in events if e == "interrupt"], "a report needs no approval"
    (info,) = [d for e, d in events if e == "file"]
    assert info["name"].endswith(".pdf") and info["url"] == f"/copilot/files/{info['id']}"
    # run() uses dispatcher_id "" → owned by the thread id.
    assert files.store.get(info["id"], thread).data.startswith(b"%PDF")
