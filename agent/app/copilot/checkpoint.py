"""Where conversations are kept: Postgres in production (the API's own
database), so a pending approval survives a restart; memory when no
DATABASE_URL is set (local development).

The Postgres saver needs autocommit and no prepared statements (Supabase's
pooler); AsyncPostgresSaver.from_conn_string sets both. setup() creates its
tables (checkpoints, checkpoint_writes, checkpoint_blobs,
checkpoint_migrations) once and is safe to call on every start."""

import logging
from contextlib import AsyncExitStack

from langgraph.checkpoint.memory import InMemorySaver

from ..config import settings

log = logging.getLogger("app.copilot")


async def open_checkpointer(stack: AsyncExitStack):
    if not settings.database_url:
        log.warning("copilot: no DATABASE_URL, conversations are kept in memory only")
        return InMemorySaver()
    from langgraph.checkpoint.postgres.aio import AsyncPostgresSaver

    saver = await stack.enter_async_context(AsyncPostgresSaver.from_conn_string(settings.database_url))
    await saver.setup()
    log.info("copilot: conversations are kept in Postgres")
    return saver
