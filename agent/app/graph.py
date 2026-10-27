"""LangGraph wiring: the ask box, and the timed proposal pass."""

import logging
from datetime import datetime, timezone
from typing import Annotated

from langchain_core.messages import HumanMessage, SystemMessage
from langgraph.graph import END, START, StateGraph
from langgraph.graph.message import add_messages
from langgraph.prebuilt import ToolNode
from typing_extensions import TypedDict

from .prompts import ASK, PROPOSE, SUPERVISE
from .schema import TickOutput
from .structured import decide_structured
from .tracing import run_config
from .tools import OPERATOR_TOOLS

log = logging.getLogger(__name__)


class AskState(TypedDict):
    messages: Annotated[list, add_messages]


class ProposeState(TypedDict):
    # One message: the table the proposer builds.
    messages: Annotated[list, add_messages]
    result: TickOutput | None








def build_ask_graph(llm):
    """Wire the ask-box graph. `llm` is a chat model; tools are bound here."""
    model = llm.bind_tools(OPERATOR_TOOLS)

    async def agent(state: AskState) -> dict:
        # The system prompt is prepended each turn rather than stored in
        # state, so it can't be pushed out by a long tool conversation.
        # The model doesn't know the time: without it, "how long ago" is a guess.
        now = datetime.now(timezone.utc).strftime("%Y-%m-%d %H:%M")
        messages = [SystemMessage(f"{ASK.text}\n\nCurrent time: {now} UTC.")] + state["messages"]
        reply = await model.ainvoke(messages)
        return {"messages": [reply]}

    def needs_tools(state: AskState) -> str:
        last = state["messages"][-1]
        return "tools" if getattr(last, "tool_calls", None) else END

    graph = StateGraph(AskState)
    graph.add_node("agent", agent)
    graph.add_node("tools", ToolNode(OPERATOR_TOOLS))

    graph.add_edge(START, "agent")
    graph.add_conditional_edges("agent", needs_tools, {"tools": "tools", END: END})
    # Unconditional: after tools run, the model always gets to see the result.
    graph.add_edge("tools", "agent")

    return graph.compile()


def build_proposal_graph(llm):
    """The timed pass: one model call, no tools. The proposer hands it a table
    with the distances already worked out, so there is nothing to fetch and
    little to reason about. With tool rounds and open-ended reasoning a tick
    took 9-43 s.

    The answer must parse into TickOutput (one retry); otherwise this raises
    and the proposer falls back to the nearest-truck rule.
    """

    async def decide(state: ProposeState) -> dict:
        result = await decide_structured(llm, TickOutput, PROPOSE, state["messages"], "tick")
        return {"result": result}

    graph = StateGraph(ProposeState)
    graph.add_node("decide", decide)
    graph.add_edge(START, "decide")
    graph.add_edge("decide", END)
    return graph.compile()


def build_supervisor_decider(llm):
    """Auto mode's one model call: the contested rows of the table in, a
    list of (reference, plate, why) out. Same output shape as proposals.
    A bad answer gets one retry; then this raises and run_supervisor uses
    the nearest-truck rule for everything."""

    async def decide(table: str) -> list[tuple[str, str, str]]:
        out = await decide_structured(
            llm, TickOutput, SUPERVISE, [HumanMessage(table)], "supervisor",
            config=run_config("supervisor", SUPERVISE.version),
        )
        return [(p.delivery, p.truck, p.why) for p in out.proposals]

    return decide
