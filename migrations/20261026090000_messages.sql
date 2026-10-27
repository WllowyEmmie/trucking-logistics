-- +goose Up

-- Messages between dispatch and a driver: one thread per driver. The console
-- reads and writes every thread; a driver's phone only their own.
-- read_at is when the other side opened it (a driver's message: a
-- dispatcher; a dispatcher's: the driver).
CREATE TABLE messages (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    driver_id   UUID NOT NULL REFERENCES drivers(id) ON DELETE CASCADE,
    from_driver BOOLEAN NOT NULL,
    -- Who wrote it, as shown in the thread: the dispatcher's name, or the driver's.
    sender      TEXT NOT NULL,
    body        TEXT NOT NULL CHECK (char_length(body) BETWEEN 1 AND 2000),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    read_at     TIMESTAMPTZ
);
CREATE INDEX messages_thread ON messages (driver_id, created_at DESC);
CREATE INDEX messages_unread ON messages (driver_id, from_driver) WHERE read_at IS NULL;

-- +goose Down
DROP TABLE messages;
