// internal/db/trucks_manual.go
package db

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type TruckWithPosition struct {
	ID          uuid.UUID  `json:"id"`
	PlateNumber string     `json:"plate_number"`
	Model       *string    `json:"model"`
	Status      string     `json:"status"`
	DriverID    *uuid.UUID `json:"driver_id"`
	Lng         *float64   `json:"lng"`
	Lat         *float64   `json:"lat"`
	LastSeenAt  *time.Time `json:"last_seen_at"`
}

func (q *Queries) ListTrucks(ctx context.Context) ([]TruckWithPosition, error) {
	rows, err := q.db.Query(ctx, `
		SELECT id, plate_number, model, status, driver_id,
		       ST_X(last_position::geometry) AS lng,
		       ST_Y(last_position::geometry) AS lat,
		       last_seen_at
		FROM trucks
		ORDER BY plate_number`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	 out :=[]TruckWithPosition{}
	for rows.Next() {
		var t TruckWithPosition
		if err := rows.Scan(&t.ID, &t.PlateNumber, &t.Model, &t.Status,
			&t.DriverID, &t.Lng, &t.Lat, &t.LastSeenAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
