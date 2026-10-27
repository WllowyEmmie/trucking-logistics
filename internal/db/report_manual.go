package db

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ReportRow is one delivery created in a report's range, with when it
// finished (completed, cancelled or failed) and the truck it last had.
type ReportRow struct {
	Reference    string     `json:"reference"`
	CustomerName string     `json:"customer_name"`
	Status       string     `json:"status"`
	CreatedAt    time.Time  `json:"created_at"`
	FinishedAt   *time.Time `json:"finished_at"`
	DueBy        *time.Time `json:"due_by"`
	Plate        *string    `json:"plate"`
}

// ReportDeliveries: deliveries created in [from, to), oldest first, at most limit.
func (q *Queries) ReportDeliveries(ctx context.Context, from, to time.Time, limit int) ([]ReportRow, error) {
	rows, err := q.db.Query(ctx, `
		SELECT d.reference, d.customer_name, d.status, d.created_at,
		  (SELECT max(e.created_at) FROM delivery_events e
		    WHERE e.delivery_id = d.id AND e.event_type = d.status
		      AND d.status IN ('completed', 'cancelled', 'failed')) AS finished_at,
		  d.due_by,
		  COALESCE(t.plate_number,
		    (SELECT substring(e.note FROM 'assigned to (.*)$') FROM delivery_events e
		      WHERE e.delivery_id = d.id AND e.event_type IN ('assigned', 'auto_assigned', 'reassigned')
		      ORDER BY e.created_at DESC, e.id DESC LIMIT 1)) AS plate
		FROM deliveries d
		LEFT JOIN trucks t ON t.id = d.truck_id
		WHERE d.created_at >= $1 AND d.created_at < $2
		ORDER BY d.created_at, d.id
		LIMIT $3`, from, to, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ReportRow{}
	for rows.Next() {
		var r ReportRow
		if err := rows.Scan(&r.Reference, &r.CustomerName, &r.Status, &r.CreatedAt, &r.FinishedAt, &r.DueBy, &r.Plate); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LastAssignedAt: when each delivery was last given its truck (assigned,
// by auto mode, or reassigned).
func (q *Queries) LastAssignedAt(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]time.Time, error) {
	out := map[uuid.UUID]time.Time{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.db.Query(ctx, `
		SELECT delivery_id, max(created_at) FROM delivery_events
		WHERE delivery_id = ANY($1) AND event_type IN ('assigned', 'auto_assigned', 'reassigned')
		GROUP BY delivery_id`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var at time.Time
		if err := rows.Scan(&id, &at); err != nil {
			return nil, err
		}
		out[id] = at
	}
	return out, rows.Err()
}
