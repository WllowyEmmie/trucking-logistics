-- name: GetStopForUpdate :one
SELECT id, delivery_id, sequence, stop_type, status, arrived_at, completed_at
FROM delivery_stops
WHERE id = @id AND delivery_id = @delivery_id
FOR UPDATE;

-- name: CountIncompleteStopsBefore :one
SELECT COUNT(*) FROM delivery_stops
WHERE delivery_id = @delivery_id
  AND sequence < @sequence
  AND status NOT IN ('completed', 'skipped');

-- name: MarkStopArrived :one
UPDATE delivery_stops
SET status = 'arrived', arrived_at = now()
WHERE id = @id
RETURNING id, sequence, stop_type, status, arrived_at, completed_at;

-- name: MarkStopCompleted :one
UPDATE delivery_stops
SET status = 'completed', completed_at = now()
WHERE id = @id
RETURNING id, sequence, stop_type, status, arrived_at, completed_at;