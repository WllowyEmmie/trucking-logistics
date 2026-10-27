"""Model decisions with a checked shape.

Every call that decides something (proposals, auto mode) goes through
decide_structured: the answer must parse into the given Pydantic model. A
bad answer gets one retry, told what was wrong; a second bad answer raises
StructuredOutputError, and the caller falls back to its rule. Token counts
and the prompt version are logged on every call."""

import logging
import time
from typing import TypeVar

from langchain_core.messages import BaseMessage, HumanMessage, SystemMessage
from langchain_core.runnables import RunnableConfig
from pydantic import BaseModel

from .prompts import Prompt

log = logging.getLogger("app.structured")

T = TypeVar("T", bound=BaseModel)


class StructuredOutputError(Exception):
    """The model's answer didn't fit the schema, twice."""


async def decide_structured(
    llm, schema: type[T], prompt: Prompt, messages: list[BaseMessage], label: str,
    config: RunnableConfig | None = None,
) -> T:
    decider = llm.with_structured_output(schema, include_raw=True)
    convo: list[BaseMessage] = [SystemMessage(prompt.text), *messages]
    problem = ""
    for attempt in (1, 2):
        started = time.monotonic()
        out = await decider.ainvoke(convo, config=config)
        raw = out.get("raw")
        u = getattr(raw, "usage_metadata", None) or {}
        log.info(
            "%s: model call %d prompt=%s in=%s out=%s %.1fs",
            label, attempt, prompt.version, u.get("input_tokens"), u.get("output_tokens"),
            time.monotonic() - started,
        )
        parsed, error = out.get("parsed"), out.get("parsing_error")
        if error is None and isinstance(parsed, schema):
            return parsed
        problem = str(error or "no answer in the required format")[:300]
        log.warning("%s: answer didn't fit %s (%s)", label, schema.__name__, problem)
        # Only a note, not the bad answer itself: with tool-calling structured
        # output that answer is a tool call, and resending it without a tool
        # result is refused by the provider.
        convo.append(HumanMessage(
            f"That answer didn't match the required format: {problem}. "
            "Answer again, using only the required format and only references and plates from the table."
        ))
    raise StructuredOutputError(f"{label}: {problem}")
