-- +goose Up
-- Operator settings, edited on the Settings page. One row; the API's
-- environment supplies the defaults until it's first saved.
CREATE TABLE settings (
    id          SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    data        JSONB NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by  TEXT NOT NULL
);

-- +goose Down
DROP TABLE IF EXISTS settings;
