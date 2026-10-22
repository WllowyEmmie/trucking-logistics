"""Turns the agent's stream into the events the console reads.

The agent is streamed with stream_mode=["updates", "messages", "custom"];
each item is (mode, chunk). Out come (event, data) pairs:

  token       {"text"}                       model text as it's written
  message     {"text"}                       a finished reply, whole
  tool_start  {"id", "name", "args", "tier"} the model called a tool
  tool_end    {"id", "name", "ok", "summary"}
  ui          {"action", "kind", "target"}   move the console (focus_map...)
  interrupt   {"id", "actions": [...]}       changes waiting for approval

Duck-typed on purpose (no LangChain imports), so it's testable alone."""

from typing import Any, Iterator

MODEL_NODE = "model"
TOOLS_NODE = "tools"


def text_of(content: Any) -> str:
    if isinstance(content, str):
        return content
    parts = []
    for block in content or []:
        if isinstance(block, str):
            parts.append(block)
        elif isinstance(block, dict) and block.get("type") == "text":
            parts.append(block.get("text", ""))
    return "".join(parts)


def _type(msg: Any) -> str:
    return getattr(msg, "type", "") or ""


def interrupt_event(intr: Any, tiers: dict[str, str]) -> dict:
    """An approval request: one entry per paused tool call, in order (the
    resume must answer them in the same order)."""
    value = getattr(intr, "value", intr) or {}
    requests = value.get("action_requests", []) if isinstance(value, dict) else []
    configs = value.get("review_configs", []) if isinstance(value, dict) else []
    allowed = {c.get("action_name"): c.get("allowed_decisions", []) for c in configs}
    actions = []
    for r in requests:
        name = r.get("name", "")
        actions.append({
            "name": name,
            "args": r.get("args", {}),
            "description": r.get("description", ""),
            "tier": tiers.get(name, "write"),
            "allowed_decisions": allowed.get(name, ["approve", "reject"]),
        })
    return {"id": getattr(intr, "id", None), "actions": actions}


def translate(mode: str, chunk: Any, tiers: dict[str, str]) -> Iterator[tuple[str, dict]]:
    if mode == "messages":
        msg, meta = chunk
        if (meta or {}).get("langgraph_node") == MODEL_NODE and _type(msg) in ("AIMessageChunk", "ai"):
            text = text_of(getattr(msg, "content", ""))
            if text:
                yield "token", {"text": text}
        return

    if mode == "custom":
        if isinstance(chunk, dict) and isinstance(chunk.get("ui"), dict):
            yield "ui", chunk["ui"]
        return

    if mode != "updates" or not isinstance(chunk, dict):
        return
    for node, update in chunk.items():
        if node == "__interrupt__":
            for intr in update or ():
                yield "interrupt", interrupt_event(intr, tiers)
            continue
        if node not in (MODEL_NODE, TOOLS_NODE) or not isinstance(update, dict):
            continue
        for msg in update.get("messages", []) or []:
            kind = _type(msg)
            if kind == "ai":
                calls = getattr(msg, "tool_calls", None) or []
                for c in calls:
                    yield "tool_start", {
                        "id": c.get("id"), "name": c.get("name"), "args": c.get("args", {}),
                        "tier": tiers.get(c.get("name", ""), "read"),
                    }
                text = text_of(getattr(msg, "content", "")).strip()
                if text and not calls:
                    yield "message", {"text": text}
            elif kind == "tool":
                content = text_of(getattr(msg, "content", ""))
                ok = getattr(msg, "status", "success") != "error" and not content.startswith("Refused (")
                yield "tool_end", {
                    "id": getattr(msg, "tool_call_id", None),
                    "name": getattr(msg, "name", None),
                    "ok": ok,
                    "summary": content[:240],
                }


def to_resume(decisions: list[dict], pending: dict) -> dict:
    """The console's decisions → the middleware's resume value.

    Console: [{"type": "approve"} | {"type": "edit", "args": {...}} |
              {"type": "reject", "message": "..."}], one per pending action,
    in order. Raises ValueError when they don't fit what's pending."""
    actions = pending.get("actions", [])
    if len(decisions) != len(actions):
        raise ValueError(f"{len(actions)} actions are waiting; got {len(decisions)} decisions")
    out = []
    for d, a in zip(decisions, actions):
        kind = d.get("type")
        if kind not in a.get("allowed_decisions", []):
            raise ValueError(f"{kind!r} isn't allowed for {a['name']}")
        if kind == "approve":
            out.append({"type": "approve"})
        elif kind == "edit":
            args = d.get("args")
            if not isinstance(args, dict):
                raise ValueError("an edit needs args")
            out.append({"type": "edit", "edited_action": {"name": a["name"], "args": args}})
        elif kind == "reject":
            msg = str(d.get("message") or "The dispatcher rejected this.")[:500]
            out.append({"type": "reject", "message": msg})
    return {"decisions": out}
