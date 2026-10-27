-- +goose Up

-- What a truck is, for the Fleet page: body type, how much it carries and
-- how big it is, year and fuel. All optional. One row per truck that has any.
CREATE TABLE truck_specs (
    truck_id      UUID PRIMARY KEY REFERENCES trucks(id) ON DELETE CASCADE,
    body_type     TEXT CHECK (body_type IN ('box', 'dropside', 'flatbed', 'tipper', 'tanker', 'refrigerated', 'curtainsider', 'tractor')),
    capacity_kg   INTEGER CHECK (capacity_kg BETWEEN 100 AND 60000),
    body_length_m NUMERIC(4, 1) CHECK (body_length_m BETWEEN 1 AND 20),
    pallets       INTEGER CHECK (pallets BETWEEN 0 AND 40),
    year          INTEGER CHECK (year BETWEEN 1960 AND 2100),
    fuel          TEXT CHECK (fuel IN ('diesel', 'petrol', 'cng', 'lng', 'electric', 'hybrid')),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- A photo of the truck itself (the page shows a stock photo for its body
-- type without one). In the database so backups include it.
CREATE TABLE truck_photos (
    truck_id   UUID PRIMARY KEY REFERENCES trucks(id) ON DELETE CASCADE,
    mime       TEXT NOT NULL,
    data       BYTEA NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE truck_photos;
DROP TABLE truck_specs;
