-- name: ListEventsForDelivery :many
SELECT id, event_type, stop_id, note, created_at, actor, via, thread_id
FROM delivery_events
WHERE delivery_id = @delivery_id
ORDER BY created_at, id;