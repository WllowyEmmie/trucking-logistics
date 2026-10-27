package db

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
)

// HistoryRow is a finished delivery (completed, cancelled or failed) and
// how it got there.
type HistoryRow struct {
	ID           uuid.UUID  `json:"id"`
	Reference    string     `json:"reference"`
	CustomerName string     `json:"customer_name"`
	Status       string     `json:"status"`
	CreatedAt    time.Time  `json:"created_at"`
	FinishedAt   time.Time  `json:"finished_at"`
	DueBy        *time.Time `json:"due_by"`
	// How many times it failed, and was sent out again, on the way.
	Failures int `json:"failures"`
	Reopens  int `json:"reopens"`
	// The first failure's reason, when there was one.
	FirstFailReason *string `json:"first_fail_reason"`
	// The plate on the last assignment (a cancelled delivery has no truck any more).
	Plate *string `json:"plate"`
}

type HistoryFilter struct {
	// completed, cancelled, failed, recovered (failed, then completed),
	// late (completed after its due time), or "" for all.
	Outcome string
	Query   string
	// The plate on its last assignment (one truck's history), or "".
	Plate string
	From  *time.Time
	To      *time.Time
	// Keyset cursor: rows finished before this (and, at the same instant,
	// with a smaller id).
	BeforeAt *time.Time
	BeforeID *uuid.UUID
	Limit    int
}

func (q *Queries) DeliveryHistory(ctx context.Context, f HistoryFilter) ([]HistoryRow, error) {
	like := ""
	if f.Query != "" {
		r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
		like = "%" + r.Replace(f.Query) + "%"
	}
	rows, err := q.db.Query(ctx, `
		WITH h AS (
		  SELECT d.id, d.reference, d.customer_name, d.status, d.created_at, d.due_by,
		    (SELECT max(e.created_at) FROM delivery_events e
		      WHERE e.delivery_id = d.id AND e.event_type IN ('completed', 'cancelled', 'failed')) AS finished_at,
		    (SELECT count(*) FROM delivery_events e WHERE e.delivery_id = d.id AND e.event_type = 'failed')::int AS failures,
		    (SELECT count(*) FROM delivery_events e WHERE e.delivery_id = d.id AND e.event_type = 'reopened')::int AS reopens,
		    (SELECT e.note FROM delivery_events e WHERE e.delivery_id = d.id AND e.event_type = 'failed'
		      ORDER BY e.created_at, e.id LIMIT 1) AS first_fail,
		    (SELECT substring(e.note FROM 'assigned to (.*)$') FROM delivery_events e
		      WHERE e.delivery_id = d.id AND e.event_type IN ('assigned', 'auto_assigned', 'reassigned') ORDER BY e.created_at DESC, e.id DESC LIMIT 1) AS plate
		  FROM deliveries d
		  WHERE d.status IN ('completed', 'cancelled', 'failed')
		    AND ($2 = '' OR d.reference ILIKE $2 OR d.customer_name ILIKE $2)
		)
		SELECT id, reference, customer_name, status, created_at, finished_at, due_by, failures, reopens, first_fail, plate
		FROM h
		WHERE finished_at IS NOT NULL
		  AND CASE $1
		        WHEN 'completed' THEN status = 'completed'
		        WHEN 'cancelled' THEN status = 'cancelled'
		        WHEN 'failed'    THEN status = 'failed'
		        WHEN 'recovered' THEN status = 'completed' AND failures > 0
		        WHEN 'late'      THEN status = 'completed' AND due_by IS NOT NULL AND finished_at > due_by
		        ELSE true END
		  AND ($3::timestamptz IS NULL OR finished_at >= $3)
		  AND ($4::timestamptz IS NULL OR finished_at < $4)
		  AND ($5::timestamptz IS NULL OR (finished_at, id) < ($5, $6::uuid))
		  AND ($8 = '' OR plate = $8)
		ORDER BY finished_at DESC, id DESC
		LIMIT $7`,
		f.Outcome, like, f.From, f.To, f.BeforeAt, f.BeforeID, f.Limit, f.Plate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HistoryRow{}
	for rows.Next() {
		var h HistoryRow
		if err := rows.Scan(&h.ID, &h.Reference, &h.CustomerName, &h.Status, &h.CreatedAt, &h.FinishedAt, &h.DueBy,
			&h.Failures, &h.Reopens, &h.FirstFailReason, &h.Plate); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}
