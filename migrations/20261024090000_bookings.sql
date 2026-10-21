-- +goose Up

-- Delivery requests from the public booking page. A booking is quoted and
-- paid (Paystack) first; once paid it becomes a pending delivery, and auto
-- mode or a dispatcher takes it from there.
CREATE TABLE bookings (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Ours, sent to Paystack as the transaction reference.
    reference      TEXT NOT NULL UNIQUE,
    customer_name  TEXT NOT NULL,
    email          TEXT NOT NULL,
    phone          TEXT NOT NULL,
    goods_desc     TEXT,
    -- The stops as the customer entered them: [{stop_type, address, lat, lng, contact_name, contact_phone}].
    stops          JSONB NOT NULL,
    distance_km    DOUBLE PRECISION NOT NULL,
    -- In the currency's smallest unit (kobo), as Paystack wants it.
    amount         BIGINT NOT NULL CHECK (amount > 0),
    currency       TEXT NOT NULL DEFAULT 'NGN',
    status         TEXT NOT NULL DEFAULT 'awaiting_payment'
                   CHECK (status IN ('awaiting_payment', 'paid', 'failed')),
    paid_at        TIMESTAMPTZ,
    delivery_id    UUID REFERENCES deliveries(id) ON DELETE SET NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX bookings_recent ON bookings (created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS bookings;
