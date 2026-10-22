-- +goose Up
-- Who made each change, and through what: the console, the Copilot (with its
-- conversation), auto mode, a driver, or the booking page. The API sets these
-- per transaction (set_config(..., true)), so every insert into
-- delivery_events picks them up without each query having to pass them.
ALTER TABLE delivery_events
    ADD COLUMN actor     TEXT DEFAULT NULLIF(current_setting('app.actor', true), ''),
    ADD COLUMN via       TEXT DEFAULT NULLIF(current_setting('app.via', true), ''),
    ADD COLUMN thread_id TEXT DEFAULT NULLIF(current_setting('app.thread', true), '');

-- +goose Down
ALTER TABLE delivery_events
    DROP COLUMN thread_id,
    DROP COLUMN via,
    DROP COLUMN actor;
