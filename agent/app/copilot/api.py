"""HTTP for the Copilot (mounted under /copilot; the console reaches it as
/agent/copilot/... through the Go API's proxy, cookie included).

  POST /copilot/chat    {"message", "thread_id"?}        → event stream
  POST /copilot/resume  {"thread_id", "decisions": [...]} → event stream
  GET  /copilot/threads/{thread_id}                       → messages + pending approval

Only the dispatcher who started a thread can read or continue it (the
thread id starts with their id; util.owns_thread). One turn per thread at a
time."""

import asyncio
import logging
from collections import defaultdict
from typing import Any, AsyncIterator

from fastapi import APIRouter, Depends, HTTPException, Request
from fastapi.responses import Response, StreamingResponse
from langchain_core.runnables import RunnableConfig
from langgraph.types import Command
from pydantic import BaseModel, Field

from ..tracing import run_config
from .agent import PROMPT_VERSION
from . import files
from .context import CopilotContext
from .stream import interrupt_event, text_of, to_resume, translate
from .tools import TIER
from .util import new_thread_id, owns_thread, session_token, sse

log = logging.getLogger("app.copilot")

TURN_TIMEOUT_S = 150


class Runtime:
    """Filled in at startup (main.py): the compiled agent and the checks
    main.py owns."""

    agent: Any = None
    require_dispatcher: Any = None
    agent_enabled: Any = None
    operator_settings: Any = None


rt = Runtime()
router = APIRouter(prefix="/copilot")
_locks: dict[str, asyncio.Lock] = defaultdict(asyncio.Lock)


async def _dispatcher(request: Request) -> dict:
    return await rt.require_dispatcher(request)


class ChatIn(BaseModel):
    message: str = Field(min_length=1, max_length=2000)
    thread_id: str | None = Field(default=None, max_length=64)


class Decision(BaseModel):
    type: str = Field(pattern="^(approve|edit|reject)$")
    args: dict | None = None
    message: str | None = Field(default=None, max_length=500)


class ResumeIn(BaseModel):
    thread_id: str = Field(max_length=64)
    decisions: list[Decision] = Field(min_length=1, max_length=20)


def _config(thread_id: str, me: dict) -> RunnableConfig:
    return run_config("copilot", PROMPT_VERSION, recursion_limit=60, thread_id=thread_id,
                      dispatcher=me.get("email") or me.get("name"))


async def _context(request: Request, me: dict, thread_id: str) -> CopilotContext:
    token = session_token(request.headers.get("cookie"), request.headers.get("authorization"))
    if not token:
        raise HTTPException(401, detail="Sign in to the console first.")
    tz = "Africa/Lagos"
    try:
        tz = (await rt.operator_settings()).get("timezone") or tz
    except Exception:
        pass
    return CopilotContext(token=token, thread_id=thread_id, dispatcher_name=me.get("name") or "the dispatcher",
                          timezone=tz, dispatcher_id=str(me.get("id") or ""))


async def _pending(config: RunnableConfig) -> dict | None:
    """The approval a thread is waiting on, if any."""
    state = await rt.agent.aget_state(config)
    intrs = list(getattr(state, "interrupts", ()) or ())
    if not intrs:
        for task in getattr(state, "tasks", ()) or ():
            intrs.extend(getattr(task, "interrupts", ()) or ())
    return interrupt_event(intrs[0], TIER) if intrs else None


async def _run(payload: Any, config: RunnableConfig, ctx: CopilotContext, thread_id: str) -> AsyncIterator[str]:
    yield sse("thread", {"thread_id": thread_id})
    lock = _locks[thread_id]
    if lock.locked():
        yield sse("error", {"message": "This conversation is still answering. Wait for it, or start a new one."})
        return
    async with lock:
        try:
            async with asyncio.timeout(TURN_TIMEOUT_S):
                async for mode, chunk in rt.agent.astream(
                    payload, config, context=ctx, stream_mode=["updates", "messages", "custom"]
                ):
                    for event, data in translate(mode, chunk, TIER):
                        if event == "tool_start":
                            log.info("copilot %s: %s %s", thread_id[-8:], data["name"], data["tier"])
                        yield sse(event, data)
        except TimeoutError:
            yield sse("error", {"message": "That took too long and was stopped. Try a narrower request."})
            return
        except Exception as e:  # provider outage, out of credits, a bug
            log.exception("copilot turn failed")
            yield sse("error", {"message": f"The Copilot failed: {type(e).__name__}"})
            return
    yield sse("done", {})


def _stream(gen: AsyncIterator[str]) -> StreamingResponse:
    return StreamingResponse(
        gen,
        media_type="text/event-stream",
        # No buffering on the way (Next, proxies): events should arrive as written.
        headers={"Cache-Control": "no-cache", "X-Accel-Buffering": "no"},
    )


async def _enabled() -> None:
    if not await rt.agent_enabled():
        raise HTTPException(503, detail="The agent is turned off in Settings.")


@router.post("/chat")
async def chat(body: ChatIn, request: Request, me: dict = Depends(_dispatcher)) -> StreamingResponse:
    await _enabled()
    if body.thread_id:
        if not owns_thread(me["id"], body.thread_id):
            raise HTTPException(404, detail="No such conversation.")
        thread_id = body.thread_id
        if await _pending(_config(thread_id, me)):
            raise HTTPException(409, detail="Approve or reject the waiting change first.")
    else:
        thread_id = new_thread_id(me["id"])
    ctx = await _context(request, me, thread_id)
    payload = {"messages": [{"role": "user", "content": body.message}]}
    return _stream(_run(payload, _config(thread_id, me), ctx, thread_id))


@router.post("/resume")
async def resume(body: ResumeIn, request: Request, me: dict = Depends(_dispatcher)) -> StreamingResponse:
    await _enabled()
    if not owns_thread(me["id"], body.thread_id):
        raise HTTPException(404, detail="No such conversation.")
    config = _config(body.thread_id, me)
    pending = await _pending(config)
    if not pending:
        raise HTTPException(409, detail="Nothing is waiting for approval.")
    try:
        value = to_resume([d.model_dump() for d in body.decisions], pending)
    except ValueError as e:
        raise HTTPException(400, detail=str(e))
    ctx = await _context(request, me, body.thread_id)
    log.info("copilot %s: decisions %s", body.thread_id[-8:], [d.type for d in body.decisions])
    return _stream(_run(Command(resume=value), config, ctx, body.thread_id))


@router.get("/files/{file_id}")
async def download(file_id: str, me: dict = Depends(_dispatcher)) -> Response:
    """A file the Copilot made for this dispatcher (a PDF report), for an
    hour after it was made. Anyone else's id is a 404."""
    f = files.store.get(file_id, str(me.get("id") or ""))
    if f is None:
        raise HTTPException(404, detail="No such file, or it has expired (files are kept for an hour).")
    safe = "".join(c if c.isalnum() or c in "-_." else "_" for c in f.name)
    return Response(f.data, media_type=f.mime, headers={
        "Content-Disposition": f'attachment; filename="{safe}"',
        "Cache-Control": "private, no-store",
    })


@router.get("/threads/{thread_id}")
async def thread(thread_id: str, me: dict = Depends(_dispatcher)) -> dict:
    if not owns_thread(me["id"], thread_id):
        raise HTTPException(404, detail="No such conversation.")
    config = _config(thread_id, me)
    state = await rt.agent.aget_state(config)
    messages = []
    for m in (getattr(state, "values", {}) or {}).get("messages", []):
        kind = getattr(m, "type", "")
        if kind == "human":
            messages.append({"role": "user", "text": text_of(m.content)})
        elif kind == "ai":
            text = text_of(m.content).strip()
            calls = [{"id": c.get("id"), "name": c.get("name"), "args": c.get("args", {}),
                      "tier": TIER.get(c.get("name", ""), "read")} for c in (getattr(m, "tool_calls", None) or [])]
            if text or calls:
                messages.append({"role": "assistant", "text": text, "tool_calls": calls})
        elif kind == "tool":
            messages.append({"role": "tool", "name": getattr(m, "name", None),
                             "tool_call_id": getattr(m, "tool_call_id", None), "text": text_of(m.content)[:240]})
    return {"thread_id": thread_id, "messages": messages, "pending": await _pending(config)}
