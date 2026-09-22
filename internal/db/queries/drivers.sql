-- name: CreateDriver :one
INSERT INTO drivers (name, phone)
VALUES (@name, @phone)
RETURNING id, name, phone, created_at;

-- name: ListDrivers :many
SELECT
    id,
    name,
    phone
FROM drivers
ORDER BY name;