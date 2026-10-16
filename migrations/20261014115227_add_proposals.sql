-- +goose Up
CREATE TABLE proposals (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    kind         TEXT NOT NULL CHECK (kind IN ('change', 'flag')),
    status       TEXT NOT NULL DEFAULT 'pending'
                 CHECK (status IN ('pending', 'approved', 'rejected', 'expired')),
    title        TEXT NOT NULL,
    why          TEXT NOT NULL,
    delivery_id  UUID REFERENCES deliveries(id) ON DELETE CASCADE,
    truck_id     UUID REFERENCES trucks(id) ON DELETE CASCADE,
    action       JSONB,
    changes      JSONB NOT NULL DEFAULT '[]',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    decided_at   TIMESTAMPTZ,
    -- 'agent' when auto mode approved it, otherwise who decided.
    decided_by   TEXT,
    -- A change needs something to execute; a flag has nothing to approve.
    CHECK ((kind = 'change' AND action IS NOT NULL)
        OR (kind = 'flag' AND action IS NULL))
);

CREATE INDEX idx_proposals_pending ON proposals (created_at DESC)
    WHERE status = 'pending';

-- +goose Down
DROP TABLE IF EXISTS proposals;