"""Where conversations are kept: Postgres in production (the API's own
database), so a pending approval survives a restart; memory when no
DATABASE_URL is set (local development).

The Postgres saver needs autocommit and no prepared statements (Supabase's
pooler); AsyncPostgresSaver.from_conn_string sets both. setup() creates its
tables (checkpoints, checkpoint_writes, checkpoint_blobs,
checkpoint_migrations) once and is safe to call on every start."""

import asyncio
import logging
import sys
from contextlib import AsyncExitStack

from langgraph.checkpoint.memory import InMemorySaver

from ..config import settings

log = logging.getLogger("app.copilot")


def _proactor_loop() -> bool:
    """Windows' default event loop, which psycopg's async mode can't use."""
    loop = asyncio.get_running_loop()
    return sys.platform == "win32" and type(loop).__name__ == "ProactorEventLoop"


async def open_checkpointer(stack: AsyncExitStack):
    if not settings.database_url:
        log.warning("copilot: no DATABASE_URL, conversations are kept in memory only")
        return InMemorySaver()
    if _proactor_loop():
        # Local development on Windows: uvicorn runs the Proactor loop and
        # psycopg refuses it. Production (Linux) isn't affected.
        log.warning("copilot: Windows event loop; conversations are kept in memory only (Postgres works on Linux)")
        return InMemorySaver()
    from langgraph.checkpoint.postgres.aio import AsyncPostgresSaver

    saver = await stack.enter_async_context(AsyncPostgresSaver.from_conn_string(settings.database_url))
    await saver.setup()
    log.info("copilot: conversations are kept in Postgres")
    return saver
