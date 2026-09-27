-- name: CreateDelivery :one
INSERT INTO deliveries (tracking_token, customer_name, customer_phone, goods_desc)
VALUES (@tracking_token, @customer_name, @customer_phone, @goods_desc)
RETURNING id, reference, tracking_token, customer_name, customer_phone, goods_desc, status, created_at;

-- name: CreateDeliveryStop :one
INSERT INTO delivery_stops (delivery_id, sequence, stop_type, address, position, contact_name, contact_phone)
VALUES (
    @delivery_id, @sequence, @stop_type, @address,
    ST_SetSRID(ST_MakePoint(@lng::float8, @lat::float8), 4326)::geography,
    @contact_name, @contact_phone
)
RETURNING id, sequence, stop_type, address, status;

-- name: CreateDeliveryEvent :exec
INSERT INTO delivery_events (delivery_id, stop_id, event_type, note)
VALUES (@delivery_id, @stop_id, @event_type, @note);
-- name: GetDelivery :one
SELECT id, reference, tracking_token, customer_name, status, truck_id
FROM deliveries
WHERE id = @id;

-- name: AssignTruck :one
UPDATE deliveries
SET truck_id = @truck_id,
    status = 'assigned',
    updated_at = now()
WHERE id = @id
RETURNING id, reference, status, truck_id;
-- name: SetDeliveryStatus :one
UPDATE deliveries
SET status = @status,
    updated_at = now()
WHERE id = @id
RETURNING id, reference, status, truck_id;

-- name: ReleaseTruck :one
UPDATE deliveries
SET truck_id = NULL,
    status = @status,
    updated_at = now()
WHERE id = @id
RETURNING id, reference, status, truck_id;