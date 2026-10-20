# syntax=docker/dockerfile:1
#
# One image for Render's free plan, which keeps one service running: the Go
# API, its database migrations, and the Python agent beside it. The API
# serves the agent at /agent (AGENT_INTERNAL_URL), so the website only needs
# one address. See DEPLOY.md.

# ---- the API and goose ----
FROM golang:1.26-bookworm AS go
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api
# Only the Postgres driver: the other build tags drop drivers goose would
# otherwise compile in.
RUN CGO_ENABLED=0 GOBIN=/out go install -trimpath -ldflags="-s -w" \
      -tags='no_clickhouse no_libsql no_mssql no_mysql no_sqlite3 no_vertica no_ydb' \
      github.com/pressly/goose/v3/cmd/goose@v3.24.1

# ---- the agent, and what runs ----
FROM python:3.12-slim-bookworm
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 PIP_NO_CACHE_DIR=1 PIP_DISABLE_PIP_VERSION_CHECK=1
WORKDIR /app
COPY agent/pyproject.toml agent/pyproject.toml
COPY agent/app agent/app
RUN pip install ./agent
COPY --from=go /out/api /out/goose /usr/local/bin/
COPY migrations ./migrations
COPY deploy/start.sh ./start.sh
RUN chmod +x ./start.sh && useradd --system --uid 10001 --no-create-home app
USER app
# Render sets PORT; the agent listens only inside the container.
ENV PORT=10000 AGENT_PORT=8200
EXPOSE 10000
CMD ["/app/start.sh"]
