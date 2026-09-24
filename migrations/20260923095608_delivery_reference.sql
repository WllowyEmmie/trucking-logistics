-- +goose Up
CREATE SEQUENCE delivery_ref_seq START 10001;

ALTER TABLE deliveries
    ALTER COLUMN reference
    SET DEFAULT ('DEL-' || to_char(now(), 'YYYY') || '-' || nextval('delivery_ref_seq')::text);

-- +goose Down
ALTER TABLE deliveries ALTER COLUMN reference DROP DEFAULT;
DROP SEQUENCE IF EXISTS delivery_ref_seq;
