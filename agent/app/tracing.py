"""Tracing: every model run carries a name, tags and metadata, so a trace
(LangSmith, or anything reading LangChain callbacks) says what it was, which
prompt version it used, and whose it was.

LangSmith is switched on by its own environment variables, read by
LangChain: LANGSMITH_TRACING=true, LANGSMITH_API_KEY, and optionally
LANGSMITH_PROJECT (default "default"). Nothing here sends anything by
itself; without those variables the tags are simply unused.

Runs:
  ask         the console's ask box             (dispatcher)
  copilot     the Copilot chat                   (dispatcher, thread_id)
  tick        the timed proposal pass            (source)
  supervisor  auto mode's contested-truck call
  customer    the tracking page's question box   (no personal data)
"""

import logging
import os
from typing import Any

from langchain_core.runnables import RunnableConfig

log = logging.getLogger("app.tracing")


def enabled() -> bool:
    flag = (os.getenv("LANGSMITH_TRACING") or os.getenv("LANGCHAIN_TRACING_V2") or "").lower()
    key = os.getenv("LANGSMITH_API_KEY") or os.getenv("LANGCHAIN_API_KEY")
    return flag in ("true", "1", "yes") and bool(key)


def log_status() -> None:
    if enabled():
        log.info("tracing: on (LangSmith project %s)", os.getenv("LANGSMITH_PROJECT") or os.getenv("LANGCHAIN_PROJECT") or "default")
    else:
        log.info("tracing: off (set LANGSMITH_TRACING=true and LANGSMITH_API_KEY to turn it on)")


def run_config(
    kind: str,
    prompt_version: str | None = None,
    *,
    recursion_limit: int | None = None,
    thread_id: str | None = None,
    **metadata: Any,
) -> RunnableConfig:
    """A RunnableConfig for one run. Child runs (model calls, tools)
    inherit its tags and metadata. thread_id also goes in "configurable",
    where a checkpointer looks for it."""
    tags = [kind]
    meta: dict[str, Any] = {"run_kind": kind}
    if prompt_version:
        tags.append(f"prompt:{prompt_version}")
        meta["prompt_version"] = prompt_version
    if thread_id:
        meta["thread_id"] = thread_id
    meta.update({k: v for k, v in metadata.items() if v is not None})
    config = RunnableConfig(run_name=kind, tags=tags, metadata=meta)
    if recursion_limit is not None:
        config["recursion_limit"] = recursion_limit
    if thread_id:
        config["configurable"] = {"thread_id": thread_id}
    return config
