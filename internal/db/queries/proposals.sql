-- name: CreateProposal :one
-- name: CreateProposal :one
INSERT INTO proposals (kind, title, why, delivery_id, truck_id, action, changes)
VALUES (@kind, @title, @why, @delivery_id, @truck_id, @action, @changes)
RETURNING id, kind, status, title, why, delivery_id, truck_id, action, changes,
          created_at, decided_at, decided_by;
-- name: ListProposalsByStatus :many
SELECT id, kind, status, title, why, delivery_id, truck_id, action, changes,
       created_at, decided_at, decided_by
FROM proposals
WHERE status = @status
ORDER BY created_at DESC;

-- name: GetProposalForUpdate :one
SELECT id, kind, status, title, why, delivery_id, truck_id, action, changes, created_at
FROM proposals
WHERE id = @id
FOR UPDATE;

-- name: DecideProposal :one
UPDATE proposals
SET status = @status,
    decided_at = now(),
    decided_by = @decided_by
WHERE id = @id
RETURNING id, kind, status, title, why, delivery_id, truck_id, action, changes,
          created_at, decided_at, decided_by;