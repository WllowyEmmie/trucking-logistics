package db

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type StopRow struct {
	ID           uuid.UUID  `json:"id"`
	Sequence     int32      `json:"sequence"`
	StopType     string     `json:"stop_type"`
	Address      string     `json:"address"`
	Lng          *float64   `json:"lng"`
	Lat          *float64   `json:"lat"`
	Status       string     `json:"status"`
	ArrivedAt    *time.Time `json:"arrived_at"`
	CompletedAt  *time.Time `json:"completed_at"`
	ContactName  *string    `json:"contact_name"`
	ContactPhone *string    `json:"contact_phone"`
	// Proof of delivery, at a drop-off that has one.
	Proof *ProofSummary `json:"proof"`
}

func (q *Queries) ListStopsForDelivery(ctx context.Context, deliveryID uuid.UUID) ([]StopRow, error) {
	rows, err := q.db.Query(ctx, `
		SELECT s.id, s.sequence, s.stop_type, s.address,
		       ST_X(s."position"::geometry) AS lng,
		       ST_Y(s."position"::geometry) AS lat,
		       s.status, s.arrived_at, s.completed_at, s.contact_name, s.contact_phone,
		       p.receiver_name, p.captured_at, p.photo IS NOT NULL, p.signature IS NOT NULL, p.lat, p.lng, p.saved_by
		FROM delivery_stops s
		LEFT JOIN stop_proofs p ON p.stop_id = s.id
		WHERE s.delivery_id = $1
		ORDER BY s.sequence`, deliveryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []StopRow{}
	for rows.Next() {
		var s StopRow
		var name, by *string
		var at *time.Time
		var photo, sig *bool
		var plat, plng *float64
		if err := rows.Scan(&s.ID, &s.Sequence, &s.StopType, &s.Address,
			&s.Lng, &s.Lat, &s.Status, &s.ArrivedAt, &s.CompletedAt, &s.ContactName, &s.ContactPhone,
			&name, &at, &photo, &sig, &plat, &plng, &by); err != nil {
			return nil, err
		}
		if name != nil {
			s.Proof = &ProofSummary{ReceiverName: *name, CapturedAt: *at, HasPhoto: *photo, HasSignature: *sig, Lat: plat, Lng: plng, SavedBy: *by}
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// CountIncompleteStops is how many stops of a delivery are neither completed
// nor skipped. completeDelivery refuses while any are open.
func (q *Queries) CountIncompleteStops(ctx context.Context, deliveryID uuid.UUID) (int64, error) {
	var n int64
	err := q.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM delivery_stops
		WHERE delivery_id = $1 AND status NOT IN ('completed', 'skipped')`, deliveryID).Scan(&n)
	return n, err
}
