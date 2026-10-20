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
	// Whether the truck's driver is on shift (false with no driver).
	DriverOnShift bool `json:"driver_on_shift"`
}

func (q *Queries) ListTrucks(ctx context.Context) ([]TruckWithPosition, error) {
	rows, err := q.db.Query(ctx, `
		SELECT t.id, t.plate_number, t.model, t.status, t.driver_id,
		       ST_X(t.last_position::geometry) AS lng,
		       ST_Y(t.last_position::geometry) AS lat,
		       t.last_seen_at,
		       EXISTS (SELECT 1 FROM driver_shifts s WHERE s.driver_id = t.driver_id AND s.ended_at IS NULL)
		FROM trucks t
		ORDER BY t.plate_number`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	 out :=[]TruckWithPosition{}
	for rows.Next() {
		var t TruckWithPosition
		if err := rows.Scan(&t.ID, &t.PlateNumber, &t.Model, &t.Status,
			&t.DriverID, &t.Lng, &t.Lat, &t.LastSeenAt, &t.DriverOnShift); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
