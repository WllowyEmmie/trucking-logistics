#!/bin/sh
# What the container runs: migrations, then the agent in the background,
# then the API in the foreground. If the API stops, the container stops and
# Render restarts it; if the agent stops, it's restarted here.
set -eu
: "${DATABASE_URL:?DATABASE_URL must be set}"
: "${AGENT_API_KEY:?AGENT_API_KEY must be set (the API and the agent share it)}"

cd /app
echo "migrating the database"
goose -dir migrations postgres "$DATABASE_URL" up

# The agent reaches the API inside the container, and the API serves it at /agent.
export API_URL="http://127.0.0.1:${PORT}"
export AGENT_INTERNAL_URL="http://127.0.0.1:${AGENT_PORT}"

(
  cd /app/agent
  while true; do
    python -m uvicorn app.main:app --host 127.0.0.1 --port "$AGENT_PORT" --no-access-log || true
    echo "agent stopped; starting it again in 5 s" >&2
    sleep 5
  done
) &

exec api
