-- name: ListEventsForDelivery :many
SELECT id, event_type, note, created_at
FROM delivery_events
WHERE delivery_id = @delivery_id
ORDER BY created_at;