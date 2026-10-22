from dataclasses import dataclass


@dataclass(frozen=True)
class CopilotContext:
    """Per-run context the tools read (runtime.context). Not stored in the
    conversation: the token never reaches the model or the checkpoint."""

    token: str  # the dispatcher's session token
    thread_id: str
    dispatcher_name: str
    timezone: str = "Africa/Lagos"
