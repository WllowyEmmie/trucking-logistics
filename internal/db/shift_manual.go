package db

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// OpenShift is when the driver's current shift started, or nil when they're
// off shift.
func (q *Queries) OpenShift(ctx context.Context, driverID uuid.UUID) (*time.Time, error) {
	var at time.Time
	err := q.db.QueryRow(ctx, `SELECT started_at FROM driver_shifts WHERE driver_id = $1 AND ended_at IS NULL`, driverID).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &at, nil
}

// StartShift opens a shift unless one is open already; either way it
// returns when the open one started.
func (q *Queries) StartShift(ctx context.Context, driverID uuid.UUID) (time.Time, error) {
	if _, err := q.db.Exec(ctx, `
		INSERT INTO driver_shifts (driver_id) VALUES ($1)
		ON CONFLICT (driver_id) WHERE ended_at IS NULL DO NOTHING`, driverID); err != nil {
		return time.Time{}, err
	}
	at, err := q.OpenShift(ctx, driverID)
	if err != nil || at == nil {
		return time.Time{}, err
	}
	return *at, nil
}

// EndShift closes the open shift, if any. It reports whether there was one.
func (q *Queries) EndShift(ctx context.Context, driverID uuid.UUID, by string) (bool, error) {
	tag, err := q.db.Exec(ctx, `
		UPDATE driver_shifts SET ended_at = now(), ended_by = $2
		WHERE driver_id = $1 AND ended_at IS NULL`, driverID, by)
	return tag.RowsAffected() > 0, err
}

// OpenShifts: every driver on shift now, and since when.
func (q *Queries) OpenShifts(ctx context.Context) (map[uuid.UUID]time.Time, error) {
	rows, err := q.db.Query(ctx, `SELECT driver_id, started_at FROM driver_shifts WHERE ended_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]time.Time{}
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

// DriverOnTheRoad: whether any of the driver's trucks has a delivery in
// transit (they can't end their shift then).
func (q *Queries) DriverOnTheRoad(ctx context.Context, driverID uuid.UUID) (bool, error) {
	var n int
	err := q.db.QueryRow(ctx, `
		SELECT count(*) FROM deliveries d JOIN trucks t ON t.id = d.truck_id
		WHERE t.driver_id = $1 AND d.status = 'in_transit'`, driverID).Scan(&n)
	return n > 0, err
}
