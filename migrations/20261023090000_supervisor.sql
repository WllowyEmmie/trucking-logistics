-- +goose Up

-- What the agent did on its own in auto mode: gave a driver a truck, or a
-- truck a delivery. The console lists these so a dispatcher can see (and
-- undo) every move, with the reason the agent gave.
CREATE TABLE supervisor_actions (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    kind        TEXT NOT NULL CHECK (kind IN ('pair', 'assign')),
    driver_id   UUID REFERENCES drivers(id) ON DELETE SET NULL,
    truck_id    UUID REFERENCES trucks(id) ON DELETE SET NULL,
    delivery_id UUID REFERENCES deliveries(id) ON DELETE SET NULL,
    summary     TEXT NOT NULL,
    reason      TEXT NOT NULL,
    -- 'model' when the AI chose, 'rule' when the nearest-truck rule did
    -- (the model was off, slow, or skipped it).
    decided_by  TEXT NOT NULL CHECK (decided_by IN ('model', 'rule')),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX supervisor_actions_recent ON supervisor_actions (created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS supervisor_actions;
