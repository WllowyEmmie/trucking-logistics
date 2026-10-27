-- +goose Up

-- A driver added without a PIN starts with the default (1234) and must pick
-- their own the first time they sign in; until then the API lets them do
-- nothing else.
ALTER TABLE drivers ADD COLUMN pin_must_change BOOLEAN NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE drivers DROP COLUMN pin_must_change;
