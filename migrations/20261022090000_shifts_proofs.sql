-- +goose Up
-- Driver shifts: the phone only sends GPS while one is open.
CREATE TABLE driver_shifts (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    driver_id   UUID NOT NULL REFERENCES drivers(id) ON DELETE CASCADE,
    started_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    ended_at    TIMESTAMPTZ,
    -- "driver", or the dispatcher who ended it for them.
    ended_by    TEXT
);
-- At most one open shift per driver.
CREATE UNIQUE INDEX driver_shifts_one_open ON driver_shifts (driver_id) WHERE ended_at IS NULL;
CREATE INDEX driver_shifts_driver_time ON driver_shifts (driver_id, started_at DESC);

-- Proof of delivery, one per drop-off. Images are small (shrunk on the
-- phone) and kept in the database so backups carry them.
CREATE TABLE stop_proofs (
    stop_id        UUID PRIMARY KEY REFERENCES delivery_stops(id) ON DELETE CASCADE,
    delivery_id    UUID NOT NULL REFERENCES deliveries(id) ON DELETE CASCADE,
    receiver_name  TEXT NOT NULL,
    photo          BYTEA,
    photo_type     TEXT,
    signature      BYTEA,
    signature_type TEXT,
    -- Where the phone was when it was taken, if it knew.
    lat            DOUBLE PRECISION,
    lng            DOUBLE PRECISION,
    captured_at    TIMESTAMPTZ NOT NULL,
    saved_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    saved_by       TEXT NOT NULL
);
CREATE INDEX stop_proofs_delivery ON stop_proofs (delivery_id);

-- One open agent proposal per delivery. Older duplicates are expired first.
UPDATE proposals p SET status = 'expired', decided_at = now(), decided_by = 'system: duplicate'
WHERE p.status = 'pending' AND p.delivery_id IS NOT NULL
  AND EXISTS (SELECT 1 FROM proposals q
              WHERE q.delivery_id = p.delivery_id AND q.status = 'pending'
                AND (q.created_at, q.id) > (p.created_at, p.id));
CREATE UNIQUE INDEX proposals_one_pending_per_delivery ON proposals (delivery_id)
    WHERE status = 'pending' AND delivery_id IS NOT NULL;

-- +goose Down
DROP INDEX proposals_one_pending_per_delivery;
DROP TABLE stop_proofs;
DROP TABLE driver_shifts;
