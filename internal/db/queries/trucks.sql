
-- name: CreateTruck :one
INSERT INTO trucks (plate_number, model)
VALUES (@plate_number, @model)
RETURNING id, plate_number, model, status, created_at;

-- name: UpdateTruckPosition :exec
UPDATE trucks
SET last_position = ST_SetSRID(ST_MakePoint(@lng::float8, @lat::float8), 4326)::geography,
    last_seen_at  = @recorded_at
WHERE id = @id;

-- name: GetTruckForUpdate :one
SELECT id, plate_number, status
FROM trucks
WHERE id = @id
FOR UPDATE;

-- name: SetTruckStatus :exec
UPDATE trucks
SET status = @status
WHERE id = @id;