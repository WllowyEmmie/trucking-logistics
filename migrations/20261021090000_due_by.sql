-- +goose Up
-- When the delivery was promised by (the last drop-off). Optional.
ALTER TABLE deliveries ADD COLUMN due_by TIMESTAMPTZ;

-- +goose Down
ALTER TABLE deliveries DROP COLUMN due_by;
