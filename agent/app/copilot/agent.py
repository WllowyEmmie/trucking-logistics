"""The Copilot harness: LangChain's create_agent loop with middleware around
every model and tool call.

Middleware, in order (each wraps the ones after it):
1. system prompt, rebuilt each turn with the dispatcher's name and the time
2. SummarizationMiddleware: long conversations are condensed by the small
   model so they stay inside the context window
3. ModelCallLimitMiddleware / ToolCallLimitMiddleware: a turn that loops
   stops with a message instead of running up the bill
4. ModelFallbackMiddleware: if the main model fails, the fallback answers
5. HumanInTheLoopMiddleware: every write and destructive tool pauses for the
   dispatcher's decision (approve, edit or reject) before it runs

State is checkpointed per thread (checkpoint.py), which is what lets a turn
pause on an approval and resume later, even after a restart."""

from datetime import datetime
from pathlib import Path
from zoneinfo import ZoneInfo

from langchain.agents import create_agent
from langchain.agents.middleware import (
    HumanInTheLoopMiddleware,
    ModelCallLimitMiddleware,
    ModelFallbackMiddleware,
    ModelRequest,
    SummarizationMiddleware,
    ToolCallLimitMiddleware,
    dynamic_prompt,
)

from .context import CopilotContext
from .tools import ALL_TOOLS, DESTRUCTIVE_TOOLS, WRITE_TOOLS

PROMPT_FILE = Path(__file__).parent / "prompts" / "system.md"
PROMPT_VERSION = "copilot-system-v1"  # logged with every run; bump when the prompt changes
SYSTEM_PROMPT = PROMPT_FILE.read_text(encoding="utf-8")

MODEL_CALLS_PER_TURN = 12
TOOL_CALLS_PER_TURN = 25

# What the dispatcher may decide on each kind of change. Destructive actions
# can't be edited from the card (the reason and target are the point of the
# approval); the dispatcher rejects with a note instead.
APPROVAL = {t.name: {"allowed_decisions": ["approve", "edit", "reject"]} for t in WRITE_TOOLS} | {
    t.name: {"allowed_decisions": ["approve", "reject"]} for t in DESTRUCTIVE_TOOLS
}


@dynamic_prompt
def copilot_prompt(request: ModelRequest) -> str:
    ctx: CopilotContext = request.runtime.context
    try:
        tz = ZoneInfo(ctx.timezone)
    except Exception:
        tz = ZoneInfo("UTC")
    now = datetime.now(tz).strftime("%a %d %b %Y, %H:%M")
    return SYSTEM_PROMPT.format(dispatcher_name=ctx.dispatcher_name, timezone=ctx.timezone, now=now)


def build_copilot(model, small_model, checkpointer, fallback_model=None):
    middleware = [
        copilot_prompt,
        SummarizationMiddleware(model=small_model, trigger=("tokens", 6000), keep=("messages", 20)),
        ModelCallLimitMiddleware(run_limit=MODEL_CALLS_PER_TURN, exit_behavior="end"),
        ToolCallLimitMiddleware(run_limit=TOOL_CALLS_PER_TURN, exit_behavior="continue"),
    ]
    if fallback_model is not None:
        middleware.append(ModelFallbackMiddleware(fallback_model))
    middleware.append(
        HumanInTheLoopMiddleware(interrupt_on=APPROVAL, description_prefix="Needs your approval")
    )
    return create_agent(
        model=model,
        tools=ALL_TOOLS,
        middleware=middleware,
        context_schema=CopilotContext,
        checkpointer=checkpointer,
    )
