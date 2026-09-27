-- +goose Up
ALTER TABLE deliveries DROP CONSTRAINT deliveries_status_check;
ALTER TABLE deliveries ADD CONSTRAINT deliveries_status_check
    CHECK (status IN ('pending','assigned','in_transit','completed','cancelled','failed'));

-- +goose Down
ALTER TABLE deliveries DROP CONSTRAINT deliveries_status_check;
ALTER TABLE deliveries ADD CONSTRAINT deliveries_status_check
    CHECK (status IN ('pending','assigned','in_transit','completed','cancelled'));