
-- name: CreateTruck :one
INSERT INTO trucks (plate_number, model)
VALUES (@plate_number, @model)
RETURNING id, plate_number, model, status, created_at;

-- name: UpdateTruckPosition :execrows
UPDATE trucks
SET last_position = ST_SetSRID(ST_MakePoint(@lng, @lat), 4326)::geography,
    last_seen_at  = @recorded_at
WHERE id = @id
  AND (last_seen_at IS NULL OR last_seen_at < @recorded_at);
  
-- name: GetTruckForUpdate :one
SELECT id, plate_number, status, driver_id
FROM trucks
WHERE id = @id
FOR UPDATE;

-- name: SetTruckStatus :exec
UPDATE trucks
SET status = @status
WHERE id = @id;

-- name: UpdateTruck :one
UPDATE trucks
SET driver_id = CASE WHEN @clear_driver::bool THEN NULL
                     ELSE COALESCE(sqlc.narg('driver_id'), driver_id) END,
    status = @status
WHERE id = @id
RETURNING id, plate_number, model, status, driver_id, last_seen_at;