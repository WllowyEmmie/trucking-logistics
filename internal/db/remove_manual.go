package db

import (
	"context"

	"github.com/google/uuid"
)

// TruckOpenWork counts the deliveries that still need this truck: pending
// ones can't have one, so assigned, in transit, and failed (a failed
// delivery keeps its truck for "try again").
func (q *Queries) TruckOpenWork(ctx context.Context, truckID uuid.UUID) (int, error) {
	var n int
	err := q.db.QueryRow(ctx, `
		SELECT count(*) FROM deliveries
		WHERE truck_id = $1 AND status IN ('assigned', 'in_transit', 'failed')`, truckID).Scan(&n)
	return n, err
}

// DeleteTruck removes a truck. Its positions and proposals go with it;
// finished deliveries keep their history (the plate is in their events) and
// lose only the link.
func (q *Queries) DeleteTruck(ctx context.Context, truckID uuid.UUID) (bool, error) {
	tag, err := q.db.Exec(ctx, `DELETE FROM trucks WHERE id = $1`, truckID)
	return tag.RowsAffected() > 0, err
}

// DeleteDriver removes a driver and signs them out everywhere. Their truck
// is left without a driver; their shifts and messages go with them.
func (q *Queries) DeleteDriver(ctx context.Context, driverID uuid.UUID) (bool, error) {
	if _, err := q.db.Exec(ctx, `DELETE FROM sessions WHERE role = 'driver' AND subject_id = $1`, driverID); err != nil {
		return false, err
	}
	tag, err := q.db.Exec(ctx, `DELETE FROM drivers WHERE id = $1`, driverID)
	return tag.RowsAffected() > 0, err
}
