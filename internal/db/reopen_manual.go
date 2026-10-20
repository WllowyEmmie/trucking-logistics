package db

import (
	"context"

	"github.com/google/uuid"
)

// What a failed delivery's stops become when it goes out again. Completed
// and skipped stops are history and stay as they are, except on a restart.

// ResetStops: every stop not done back to pending. all=true resets the
// completed ones too (start over: fresh goods from the pickup).
func (q *Queries) ResetStops(ctx context.Context, deliveryID uuid.UUID, all bool) error {
	_, err := q.db.Exec(ctx, `
		UPDATE delivery_stops SET status = 'pending', arrived_at = NULL, completed_at = NULL
		WHERE delivery_id = $1 AND ($2 OR status IN ('pending', 'arrived'))`, deliveryID, all)
	return err
}

// InsertHandoverStop puts a pickup at the broken-down truck's last position
// in front of the first stop still to do, moving the later stops back one.
// It returns false when that truck has never reported a position.
func (q *Queries) InsertHandoverStop(ctx context.Context, deliveryID uuid.UUID, at int32, fromTruck uuid.UUID, address string, contactName, contactPhone *string) (bool, error) {
	var has bool
	if err := q.db.QueryRow(ctx, `SELECT last_position IS NOT NULL FROM trucks WHERE id = $1`, fromTruck).Scan(&has); err != nil || !has {
		return false, err
	}
	// Two steps: (delivery_id, sequence) is unique, checked row by row.
	if _, err := q.db.Exec(ctx, `
		UPDATE delivery_stops SET sequence = sequence + 100000
		WHERE delivery_id = $1 AND sequence >= $2`, deliveryID, at); err != nil {
		return false, err
	}
	if _, err := q.db.Exec(ctx, `
		UPDATE delivery_stops SET sequence = sequence - 100000 + 1
		WHERE delivery_id = $1 AND sequence >= 100000`, deliveryID); err != nil {
		return false, err
	}
	_, err := q.db.Exec(ctx, `
		INSERT INTO delivery_stops (delivery_id, sequence, stop_type, address, position, contact_name, contact_phone)
		SELECT $1, $2, 'pickup', $3, last_position, $5, $6 FROM trucks WHERE id = $4`,
		deliveryID, at, address, fromTruck, contactName, contactPhone)
	return err == nil, err
}

// ClearDeliveryTruck: back to pending with no truck, ready to be assigned.
func (q *Queries) ClearDeliveryTruck(ctx context.Context, deliveryID uuid.UUID) error {
	_, err := q.db.Exec(ctx, `
		UPDATE deliveries SET status = 'pending', truck_id = NULL, updated_at = now()
		WHERE id = $1`, deliveryID)
	return err
}

// TruckPlateAndDriver: for the handover stop's wording and contact.
func (q *Queries) TruckPlateAndDriver(ctx context.Context, truckID uuid.UUID) (plate string, driverName, driverPhone *string, err error) {
	err = q.db.QueryRow(ctx, `
		SELECT t.plate_number, d.name, d.phone
		FROM trucks t LEFT JOIN drivers d ON d.id = t.driver_id
		WHERE t.id = $1`, truckID).Scan(&plate, &driverName, &driverPhone)
	return
}
