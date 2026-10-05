-- name: InsertLocation :exec
INSERT INTO locations (truck_id, position, accuracy_m, speed_kph, heading, recorded_at)
VALUES (
    @truck_id,
    ST_SetSRID(ST_MakePoint(@lng::float8, @lat::float8), 4326)::geography,
    @accuracy_m, @speed_kph, @heading, @recorded_at
);