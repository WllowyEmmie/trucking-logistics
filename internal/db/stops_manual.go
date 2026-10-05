package db

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type StopRow struct {
	ID          uuid.UUID  `json:"id"`
	Sequence    int32      `json:"sequence"`
	StopType    string     `json:"stop_type"`
	Address     string     `json:"address"`
	Lng         *float64   `json:"lng"`
	Lat         *float64   `json:"lat"`
	Status      string     `json:"status"`
	ArrivedAt   *time.Time `json:"arrived_at"`
	CompletedAt *time.Time `json:"completed_at"`
}

func (q *Queries) ListStopsForDelivery(ctx context.Context, deliveryID uuid.UUID) ([]StopRow, error) {
	rows, err := q.db.Query(ctx, `
		SELECT id, sequence, stop_type, address,
		       ST_X("position"::geometry) AS lng,
		       ST_Y("position"::geometry) AS lat,
		       status, arrived_at, completed_at
		FROM delivery_stops
		WHERE delivery_id = $1
		ORDER BY sequence`, deliveryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []StopRow{}
	for rows.Next() {
		var s StopRow
		if err := rows.Scan(&s.ID, &s.Sequence, &s.StopType, &s.Address,
			&s.Lng, &s.Lat, &s.Status, &s.ArrivedAt, &s.CompletedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
