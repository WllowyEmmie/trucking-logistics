-- +goose Up
-- A failed event's note used to carry "<reason>; truck moved to maintenance".
-- The note is the bare reason again; the maintenance move is its own event,
-- as failDelivery now writes it.
INSERT INTO delivery_events (delivery_id, event_type, created_at)
SELECT delivery_id, 'truck_maintenance', created_at
FROM delivery_events
WHERE event_type = 'failed' AND note LIKE '%; truck moved to maintenance';

UPDATE delivery_events
SET note = split_part(note, ';', 1)
WHERE event_type = 'failed' AND note LIKE '%; truck moved to maintenance';

-- +goose Down
UPDATE delivery_events f
SET note = f.note || '; truck moved to maintenance'
WHERE f.event_type = 'failed'
  AND EXISTS (SELECT 1 FROM delivery_events m
              WHERE m.delivery_id = f.delivery_id AND m.event_type = 'truck_maintenance'
                AND m.created_at = f.created_at);

DELETE FROM delivery_events WHERE event_type = 'truck_maintenance';
