-- +goose Up

CREATE TABLE drivers (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT NOT NULL,
    phone       TEXT NOT NULL UNIQUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE trucks (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    plate_number   TEXT NOT NULL UNIQUE,
    model          TEXT,
    status         TEXT NOT NULL DEFAULT 'available'
                   CHECK (status IN ('available','on_delivery','maintenance','offline')),
    driver_id      UUID REFERENCES drivers(id) ON DELETE SET NULL,
    last_position  GEOGRAPHY(Point, 4326),
    last_seen_at   TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE deliveries (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    reference       TEXT NOT NULL UNIQUE,
    tracking_token  TEXT NOT NULL UNIQUE,
    customer_name   TEXT NOT NULL,
    customer_phone  TEXT,
    goods_desc      TEXT,
    status          TEXT NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending','assigned','in_transit','completed','cancelled')),
    truck_id        UUID REFERENCES trucks(id) ON DELETE SET NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE delivery_stops (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    delivery_id  UUID NOT NULL REFERENCES deliveries(id) ON DELETE CASCADE,
    sequence     INT NOT NULL,
    stop_type    TEXT NOT NULL CHECK (stop_type IN ('pickup','dropoff')),
    address      TEXT NOT NULL,
    position     GEOGRAPHY(Point, 4326) NOT NULL,
    contact_name  TEXT,
    contact_phone TEXT,
    status       TEXT NOT NULL DEFAULT 'pending'
                 CHECK (status IN ('pending','arrived','completed','skipped')),
    arrived_at   TIMESTAMPTZ,
    UNIQUE (delivery_id, sequence)
);

CREATE TABLE locations (
    id          BIGSERIAL PRIMARY KEY,
    truck_id    UUID NOT NULL REFERENCES trucks(id) ON DELETE CASCADE,
    position    GEOGRAPHY(Point, 4326) NOT NULL,
    accuracy_m  REAL,
    speed_kph   REAL,
    heading     REAL,
    recorded_at TIMESTAMPTZ NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE delivery_events (
    id          BIGSERIAL PRIMARY KEY,
    delivery_id UUID NOT NULL REFERENCES deliveries(id) ON DELETE CASCADE,
    stop_id     UUID REFERENCES delivery_stops(id) ON DELETE SET NULL,
    event_type  TEXT NOT NULL,
    note        TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_locations_truck_time ON locations (truck_id, recorded_at DESC);
CREATE INDEX idx_trucks_last_position ON trucks USING GIST (last_position);
CREATE INDEX idx_stops_position       ON delivery_stops USING GIST (position);
CREATE INDEX idx_deliveries_status    ON deliveries (status);
CREATE INDEX idx_deliveries_truck     ON deliveries (truck_id);
CREATE INDEX idx_events_delivery      ON delivery_events (delivery_id, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS delivery_events;
DROP TABLE IF EXISTS locations;
DROP TABLE IF EXISTS delivery_stops;
DROP TABLE IF EXISTS deliveries;
DROP TABLE IF EXISTS trucks;
DROP TABLE IF EXISTS drivers;