-- +goose Up
-- Idempotency-Key: the first response to a change is kept for a day, so a
-- retry (a double click, a Copilot resume sent twice, a dropped connection)
-- gets that response back instead of doing the change again.
CREATE TABLE idempotency_keys (
    scope        TEXT NOT NULL,          -- who sent it: role and id
    key          TEXT NOT NULL,
    method       TEXT NOT NULL,
    path         TEXT NOT NULL,
    body_hash    TEXT NOT NULL,
    status       INT,                    -- NULL while the first request runs
    content_type TEXT,
    response     BYTEA,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (scope, key)
);
CREATE INDEX idx_idempotency_keys_created ON idempotency_keys (created_at);

-- +goose Down
DROP TABLE idempotency_keys;
