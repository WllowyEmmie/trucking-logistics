-- +goose Up
-- Who can sign in. Dispatchers use email and password; drivers use their
-- phone number and a PIN set by a dispatcher. The agent uses a service key
-- from the environment and has no row here.
CREATE TABLE dispatchers (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email         TEXT NOT NULL UNIQUE CHECK (email = lower(email)),
    name          TEXT NOT NULL,
    password_hash TEXT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE drivers ADD COLUMN pin_hash TEXT;

-- One row per signed-in browser or phone. Only a hash of the token is kept.
CREATE TABLE sessions (
    token_hash  TEXT PRIMARY KEY,
    role        TEXT NOT NULL CHECK (role IN ('dispatcher', 'driver')),
    subject_id  UUID NOT NULL,
    expires_at  TIMESTAMPTZ NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_sessions_expires ON sessions (expires_at);
CREATE INDEX idx_sessions_subject ON sessions (role, subject_id);

-- +goose Down
DROP TABLE IF EXISTS sessions;
ALTER TABLE drivers DROP COLUMN IF EXISTS pin_hash;
DROP TABLE IF EXISTS dispatchers;
