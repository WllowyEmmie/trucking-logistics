-- +goose Up
ALTER TABLE delivery_stops ADD COLUMN completed_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE delivery_stops DROP COLUMN completed_at;