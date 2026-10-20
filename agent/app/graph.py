"""LangGraph wiring: the ask box, and the timed proposal pass."""

import logging
from datetime import datetime, timezone
from typing import Annotated

from langchain_core.messages import HumanMessage, SystemMessage
from langgraph.graph import END, START, StateGraph
from langgraph.graph.message import add_messages
from langgraph.prebuilt import ToolNode
from typing_extensions import TypedDict

from .schema import TickOutput
from .tools import OPERATOR_TOOLS

log = logging.getLogger(__name__)


class AskState(TypedDict):
    messages: Annotated[list, add_messages]


class ProposeState(TypedDict):
    # One message: the table the proposer builds.
    messages: Annotated[list, add_messages]
    result: TickOutput | None


SYSTEM_PROMPT = """You answer questions from a truck dispatcher about their \
fleet, using the tools provided.

The operator runs a delivery fleet. Deliveries move through pending, \
assigned, in_transit, and end as completed, cancelled or failed. Trucks are \
available, on_delivery, maintenance or offline.

How the statuses fit together, so you don't report normal states as problems:
- Assigning a delivery sets its truck to on_delivery at once: that reserves \
the truck. The delivery stays assigned until the driver presses Start in the \
app; only then is it in_transit. An assigned delivery on an on_delivery truck \
is normal, not a mismatch.
- Completing a delivery frees its truck (available). Failing it frees it too, \
or sends it to maintenance after a breakdown or accident.
- Cancelling releases the truck, so a cancelled delivery has no truck. Normal.
- A delivery can show a "reopened" event after "failed": it was sent out \
again, and the events after it are the new attempt. A stop whose address \
starts "Handover from" is where it collects the goods from the truck that \
broke down.
- A truck with null lat/lng has never reported a position.

What is worth raising, when you're asked what needs attention:
- An assigned delivery that hasn't started for a long time: say how long, \
from its assigned event.
- A truck on a delivery with no position, or a position more than 20 minutes \
old: nobody can see where it is.
- A pending delivery that has waited while trucks are available.
- A failed delivery, with its reason, which stops were never done, and how \
it can go out again. The dispatcher does this from the delivery's row; you \
only suggest. If a pickup was completed and stops remain, the goods are on \
the failed truck: after a breakdown or accident, hand over to another truck \
(it collects from where the first truck stopped); if the customer wasn't \
available, try again with the same truck. If nothing was collected yet, \
reassign to any free truck with a driver. Damaged goods: start over from \
the pickup.

Quote delivery references exactly as the data has them (e.g. DEL-2026-10001); \
never shorten them. Times in the data are UTC. Give times as how long ago \
where you can (the current time is at the end of these instructions), and \
say "UTC" if you give a clock time.

Answer from tool results only. If the data does not support an answer, say \
so rather than estimating.

Due times and ETAs: due_by is when the delivery was promised (the last \
drop-off); null means no time was promised. eta is the system's estimate of \
reaching the last stop (straight-line distance with a road allowance, at an \
average speed, plus time at each stop), present only while a delivery is \
assigned or in transit and its truck has a position. Call it an estimate. \
A delivery is late when due_by has passed and it isn't completed; at risk \
when its eta is after its due_by. Those are worth raising. A delivery with \
no due_by can't be late; say so if asked.

Trucks carry a driver_id, not a name. Resolve it with list_drivers rather \
than showing the dispatcher a uuid. The driver_id is the driver who signs in \
to the driver app for that truck. A truck with a null driver_id can't be \
assigned (the API refuses it) until a driver is set on it in Fleet; say so \
if the dispatcher asks why a free truck isn't being used.

Be brief. A dispatcher is reading this mid-shift. Answer the question and \
stop — do not offer to do more, because each question is answered on its \
own and you will not remember this one."""


PROPOSE_PROMPT = """You suggest truck assignments to a dispatcher, who reviews \
each one before anything happens.

The message can have two tables. Pending deliveries: each has the available \
trucks within 2 km of the pickup, with a position under 15 min old. Failed \
deliveries to send out again: each already has its plan (reassign, try \
again with the same truck, hand over from the truck that failed, or start \
over), chosen from the failure reason and where the goods are; you only \
pick the truck. Distances are straight-line from the truck's last known \
position.

Rules:
- Use only references and plates from the table.
- At most one proposal per truck and one per delivery. Each must stand alone: \
the dispatcher may approve any of them and reject the rest. When two \
deliveries want the same truck, give it to the one that has waited longer.
- Every listed truck qualifies: all have a driver.
- why: one sentence with the numbers from the table, e.g. "0.1 km from the \
Apapa Wharf pickup, waiting 2 h 14 min, position 1 min old." For a failed \
delivery, start with why it failed and the plan, e.g. "Broke down after \
collecting: KF-03 is 0.4 km from KF-01 and can take the goods over." """


SUPERVISE_PROMPT = """You supervise a delivery fleet in auto mode: what you \
choose happens at once, and the dispatcher sees your reason.

The table lists pending deliveries, oldest first, each with the ready trucks \
(free, driver on shift) nearest first, with straight-line distances to the \
pickup. Some trucks are the nearest option for more than one delivery: \
decide who gets which.

Rules:
- Use only references and plates from the table, and only a truck listed on \
that delivery's row.
- Each truck at most once, each delivery at most once. Assign every delivery \
you can.
- Prefer the delivery that has waited longer when two want the same truck, \
unless that leaves the other with a much longer drive and the older one has \
another truck nearly as close.
- why: one sentence with the numbers from the table, e.g. "0.4 km from the \
Apapa Wharf pickup and the delivery has waited 25 min; KF-02 takes DLV-0141, \
which is closer to it." """


def build_ask_graph(llm):
    """Wire the ask-box graph. `llm` is a chat model; tools are bound here."""
    model = llm.bind_tools(OPERATOR_TOOLS)

    async def agent(state: AskState) -> dict:
        # The system prompt is prepended each turn rather than stored in
        # state, so it can't be pushed out by a long tool conversation.
        # The model doesn't know the time: without it, "how long ago" is a guess.
        now = datetime.now(timezone.utc).strftime("%Y-%m-%d %H:%M")
        messages = [SystemMessage(f"{SYSTEM_PROMPT}\n\nCurrent time: {now} UTC.")] + state["messages"]
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
    """
    # include_raw: the token counts are logged per tick, so cost stays visible.
    decider = llm.with_structured_output(TickOutput, include_raw=True)

    async def decide(state: ProposeState) -> dict:
        out = await decider.ainvoke([SystemMessage(PROPOSE_PROMPT)] + state["messages"])
        u = out["raw"].usage_metadata or {}
        log.info(
            "tick: model call in=%s out=%s reasoning=%s",
            u.get("input_tokens"),
            u.get("output_tokens"),
            (u.get("output_token_details") or {}).get("reasoning"),
        )
        if out["parsing_error"]:
            raise out["parsing_error"]
        return {"result": out["parsed"]}

    graph = StateGraph(ProposeState)
    graph.add_node("decide", decide)
    graph.add_edge(START, "decide")
    graph.add_edge("decide", END)
    return graph.compile()


def build_supervisor_decider(llm):
    """Auto mode's one model call: the contested rows of the table in, a
    list of (reference, plate, why) out. Same output shape as proposals."""
    decider = llm.with_structured_output(TickOutput, include_raw=True)

    async def decide(table: str) -> list[tuple[str, str, str]]:
        out = await decider.ainvoke([SystemMessage(SUPERVISE_PROMPT), HumanMessage(table)])
        u = out["raw"].usage_metadata or {}
        log.info("supervisor: model call in=%s out=%s", u.get("input_tokens"), u.get("output_tokens"))
        if out["parsing_error"]:
            raise out["parsing_error"]
        return [(p.delivery, p.truck, p.why) for p in out["parsed"].proposals]

    return decide
