package db

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// DeliveryItem is a row of GET /deliveries.
type DeliveryItem struct {
	ID           uuid.UUID  `json:"id"`
	Reference    string     `json:"reference"`
	Status       string     `json:"status"`
	CustomerName string     `json:"customer_name"`
	TruckID      *uuid.UUID `json:"truck_id"`
	CreatedAt    time.Time  `json:"created_at"`
	DueBy        *time.Time `json:"due_by"`
	// When it was completed, cancelled or failed; null while it's open.
	FinishedAt *time.Time `json:"finished_at"`
}

// ListDeliveryItems: every delivery, or those with one status; newest first.
// With boardSince, only what the console shows: everything open, everything
// failed (until it's sent out again or cancelled), and completed or
// cancelled ones that finished since then.
func (q *Queries) ListDeliveryItems(ctx context.Context, status string, boardSince *time.Time) ([]DeliveryItem, error) {
	rows, err := q.db.Query(ctx, `
		SELECT d.id, d.reference, d.status, d.customer_name, d.truck_id, d.created_at, d.due_by, f.at
		FROM deliveries d
		LEFT JOIN LATERAL (
		  SELECT max(e.created_at) AS at FROM delivery_events e
		  WHERE e.delivery_id = d.id AND e.event_type IN ('completed', 'cancelled', 'failed')
		) f ON d.status IN ('completed', 'cancelled', 'failed')
		WHERE ($1 = '' OR d.status = $1)
		  AND ($2::timestamptz IS NULL OR d.status IN ('pending', 'assigned', 'in_transit', 'failed') OR f.at >= $2)
		ORDER BY d.created_at DESC`, status, boardSince)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DeliveryItem{}
	for rows.Next() {
		var d DeliveryItem
		if err := rows.Scan(&d.ID, &d.Reference, &d.Status, &d.CustomerName, &d.TruckID, &d.CreatedAt, &d.DueBy, &d.FinishedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// RouteLeft is what an ETA needs for one delivery: the truck's position and
// the stops still to do, in order.
type RouteLeft struct {
	TruckLat, TruckLng *float64
	Stops              []StopRow
}

// RoutesLeft for the given deliveries (the active ones in a list).
func (q *Queries) RoutesLeft(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]*RouteLeft, error) {
	out := map[uuid.UUID]*RouteLeft{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.db.Query(ctx, `
		SELECT d.id, ST_Y(t.last_position::geometry), ST_X(t.last_position::geometry),
		       s.id, s.sequence, ST_Y(s.position::geometry), ST_X(s.position::geometry), s.status, s.arrived_at
		FROM deliveries d
		JOIN delivery_stops s ON s.delivery_id = d.id AND s.status IN ('pending', 'arrived')
		LEFT JOIN trucks t ON t.id = d.truck_id
		WHERE d.id = ANY($1)
		ORDER BY d.id, s.sequence`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var tlat, tlng *float64
		var s StopRow
		if err := rows.Scan(&id, &tlat, &tlng, &s.ID, &s.Sequence, &s.Lat, &s.Lng, &s.Status, &s.ArrivedAt); err != nil {
			return nil, err
		}
		r := out[id]
		if r == nil {
			r = &RouteLeft{TruckLat: tlat, TruckLng: tlng}
			out[id] = r
		}
		r.Stops = append(r.Stops, s)
	}
	return out, rows.Err()
}

// SetDueBy sets or clears (nil) when a delivery is due. It returns the
// status, so the caller can refuse a finished delivery; ErrNoRows if none.
func (q *Queries) SetDueBy(ctx context.Context, id uuid.UUID, due *time.Time) (status string, err error) {
	err = q.db.QueryRow(ctx, `
		UPDATE deliveries SET due_by = $2, updated_at = now()
		WHERE id = $1 AND status NOT IN ('completed', 'cancelled')
		RETURNING status`, id, due).Scan(&status)
	if err != nil {
		// Tell "finished" from "not there".
		var st string
		if e := q.db.QueryRow(ctx, `SELECT status FROM deliveries WHERE id = $1`, id).Scan(&st); e == nil {
			return st, nil
		}
	}
	return status, err
}
