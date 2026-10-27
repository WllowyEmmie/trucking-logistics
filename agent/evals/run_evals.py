"""Evals: does the agent pick the right tools for real requests, and does
auto mode split contested trucks sensibly? Unlike the unit tests these call
the real model, so they cost a little and their score can move when the
prompt, the tools or the model change. That's the point: run them on every
such change, and fail when the score drops below the baseline.

    cd agent
    python -m evals.run_evals                  # all cases
    python -m evals.run_evals --only cancel    # cases whose id contains "cancel"
    python -m evals.run_evals --json out.json  # also write the full results

Needs OPENROUTER_API_KEY (and MODEL, if not the default). Nothing reaches
the dispatch API or a database: the Copilot talks to a fake API with the
fleet in fixtures.py, and every change stops at the approval card.

Exit code 1 when the overall score is below evals/baseline.json's minimum,
so CI fails the build."""

import argparse
import asyncio
import json
import os
import sys
import time
import uuid
from pathlib import Path

os.environ.setdefault("AGENT_API_KEY", "evals")

import yaml
from langchain_core.runnables import RunnableConfig
from langchain_openai import ChatOpenAI
from langgraph.checkpoint.memory import InMemorySaver

from app.config import settings
from app.copilot import dispatcher_api
from app.copilot.agent import PROMPT_VERSION, build_copilot
from app.copilot.context import CopilotContext
from app.copilot.stream import translate
from app.copilot.tools import TIER
from app.copilot.util import new_thread_id
from app.graph import build_supervisor_decider
from app.prompts import SUPERVISE

from . import fixtures

HERE = Path(__file__).parent


def model() -> ChatOpenAI:
    return ChatOpenAI(model=settings.model, base_url=settings.model_base_url, api_key=settings.openrouter_api_key,
                      timeout=settings.model_timeout_s, max_retries=settings.model_max_retries, temperature=0)


def install_fake_api() -> None:
    async def fake_request(self, method, path, *, json=None, params=None, idempotency_key=None):
        out = fixtures.respond(method, path, params)
        if out is None:
            raise dispatcher_api.CopilotAPIError(404, "not_found", "not found")
        return out

    dispatcher_api.AsDispatcher.request = fake_request  # type: ignore[method-assign]


# ---- Copilot ----

async def run_copilot_case(agent, case: dict) -> dict:
    thread = new_thread_id(str(uuid.uuid4()))
    ctx = CopilotContext(token="evals", thread_id=thread, dispatcher_name="Ada")
    config = RunnableConfig(configurable={"thread_id": thread}, recursion_limit=40, tags=["evals", case["id"]])
    calls, reply, pending, error = [], "", None, None
    started = time.monotonic()
    try:
        async with asyncio.timeout(120):
            async for mode, chunk in agent.astream({"messages": [{"role": "user", "content": case["say"]}]}, config,
                                                   context=ctx, stream_mode=["updates", "custom"]):
                for event, data in translate(mode, chunk, TIER):
                    if event == "tool_start":
                        calls.append({"name": data["name"], "args": data["args"], "tier": data["tier"]})
                    elif event == "message":
                        reply = data["text"]
                    elif event == "interrupt":
                        pending = data["actions"]
    except Exception as e:
        error = f"{type(e).__name__}: {e}"[:300]
    return {"calls": calls, "reply": reply, "pending": pending, "error": error, "seconds": round(time.monotonic() - started, 1)}


def _subsequence(want: list[str], got: list[str]) -> bool:
    it = iter(got)
    return all(any(g == w for g in it) for w in want)


def _args_match(want: dict, got: dict) -> bool:
    return all(str(v).lower() in json.dumps(got.get(k), default=str).lower() for k, v in want.items())


def score_copilot(case: dict, out: dict) -> list[tuple[str, bool, str]]:
    exp = case.get("expect", {})
    names = [c["name"] for c in out["calls"]]
    writes = [c["name"] for c in out["calls"] if c["tier"] in ("write", "destructive")]
    reply = (out["reply"] or "").lower()
    checks: list[tuple[str, bool, str]] = []
    if out["error"]:
        checks.append(("ran", False, out["error"]))
    if "tools" in exp:
        checks.append(("tools", _subsequence(exp["tools"], names), f"called {names}"))
    if exp.get("no_writes"):
        checks.append(("no_writes", not writes and not out["pending"], f"writes {writes}"))
    if "approval" in exp:
        want = exp["approval"]
        hit = next((a for a in out["pending"] or [] if a["name"] == want["tool"]), None)
        ok = hit is not None and _args_match(want.get("args", {}), hit["args"])
        checks.append(("approval", ok, f"pending {[(a['name'], a['args']) for a in out['pending'] or []]}"))
    if exp.get("asks"):
        checks.append(("asks", not writes and "?" in reply, f"reply {reply[:120]!r}"))
    for w in exp.get("reply_has", []):
        checks.append((f"reply_has {w}", w.lower() in reply, ""))
    if "reply_has_any" in exp:
        checks.append(("reply_has_any", any(w.lower() in reply for w in exp["reply_has_any"]), f"reply {reply[:120]!r}"))
    if exp.get("no_file"):
        checks.append(("no_file", "chart_pdf" not in names, f"called {names}"))
    for w in exp.get("reply_lacks", []):
        checks.append((f"reply_lacks {w}", w.lower() not in reply, ""))
    return checks


# ---- Supervisor ----

def score_supervisor(case: dict, picks: list[tuple[str, str, str]] | None, error: str | None) -> list[tuple[str, bool, str]]:
    if error is not None or picks is None:
        return [("ran", False, error or "no answer")]
    table = case["table"]
    pairs = [(d, t) for d, t, _ in picks]
    trucks = [t for _, t in pairs]
    checks = [
        ("from_table", all(d in table and t in table for d, t in pairs), f"{pairs}"),
        ("truck_once", len(trucks) == len(set(trucks)), f"{trucks}"),
        ("reasons", all((w or "").strip() for _, _, w in picks), ""),
    ]
    accepted = [sorted(tuple(p) for p in option) for option in case["accept"]]
    checks.append(("assignment", sorted(pairs) in accepted, f"got {sorted(pairs)}"))
    return checks


# ---- main ----

async def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--only", default="")
    ap.add_argument("--json", default="")
    args = ap.parse_args()

    cases = yaml.safe_load((HERE / "cases.yaml").read_text(encoding="utf-8"))
    baseline = json.loads((HERE / "baseline.json").read_text(encoding="utf-8"))
    install_fake_api()
    llm = model()
    agent = build_copilot(llm, llm, InMemorySaver())
    decide = build_supervisor_decider(llm)

    results, points, total = [], 0, 0
    print(f"model {settings.model} · copilot prompt {PROMPT_VERSION} · supervisor prompt {SUPERVISE.version}\n")
    for case in cases.get("copilot", []):
        if args.only not in case["id"]:
            continue
        out = await run_copilot_case(agent, case)
        checks = score_copilot(case, out)
        results.append({"kind": "copilot", "id": case["id"], "checks": checks, **out})
    for case in cases.get("supervisor", []):
        if args.only not in case["id"]:
            continue
        picks, error = None, None
        try:
            picks = await decide(case["table"])
        except Exception as e:
            error = f"{type(e).__name__}: {e}"[:300]
        results.append({"kind": "supervisor", "id": case["id"], "checks": score_supervisor(case, picks, error), "picks": picks})

    for r in results:
        got = sum(1 for _, ok, _ in r["checks"] if ok)
        points, total = points + got, total + len(r["checks"])
        mark = "PASS" if got == len(r["checks"]) else "FAIL"
        print(f"{mark}  {r['kind']:<10} {r['id']:<40} {got}/{len(r['checks'])}")
        for name, ok, detail in r["checks"]:
            if not ok:
                print(f"        x {name}: {detail}")

    score = points / total if total else 0.0
    minimum = float(baseline.get("min_score", 0.85))
    print(f"\nscore {score:.2%} ({points}/{total}); minimum {minimum:.0%}")
    if args.json:
        Path(args.json).write_text(json.dumps({"score": score, "results": results}, default=str, indent=2), encoding="utf-8")
    if args.only:
        return 0
    return 0 if score >= minimum else 1


if __name__ == "__main__":
    sys.exit(asyncio.run(main()))
