package db

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// SupervisorAction is one thing the agent did on its own in auto mode.
type SupervisorAction struct {
	ID         uuid.UUID  `json:"id"`
	Kind       string     `json:"kind"`
	DriverID   *uuid.UUID `json:"driver_id"`
	TruckID    *uuid.UUID `json:"truck_id"`
	DeliveryID *uuid.UUID `json:"delivery_id"`
	Summary    string     `json:"summary"`
	Reason     string     `json:"reason"`
	DecidedBy  string     `json:"decided_by"`
	CreatedAt  time.Time  `json:"created_at"`
}

type NewSupervisorAction struct {
	Kind       string
	DriverID   *uuid.UUID
	TruckID    *uuid.UUID
	DeliveryID *uuid.UUID
	Summary    string
	Reason     string
	DecidedBy  string
}

func (q *Queries) AddSupervisorAction(ctx context.Context, a NewSupervisorAction) (SupervisorAction, error) {
	var out SupervisorAction
	err := q.db.QueryRow(ctx, `
		INSERT INTO supervisor_actions (kind, driver_id, truck_id, delivery_id, summary, reason, decided_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, kind, driver_id, truck_id, delivery_id, summary, reason, decided_by, created_at`,
		a.Kind, a.DriverID, a.TruckID, a.DeliveryID, a.Summary, a.Reason, a.DecidedBy,
	).Scan(&out.ID, &out.Kind, &out.DriverID, &out.TruckID, &out.DeliveryID, &out.Summary, &out.Reason, &out.DecidedBy, &out.CreatedAt)
	return out, err
}

// RecentSupervisorActions: newest first.
func (q *Queries) RecentSupervisorActions(ctx context.Context, limit int) ([]SupervisorAction, error) {
	rows, err := q.db.Query(ctx, `
		SELECT id, kind, driver_id, truck_id, delivery_id, summary, reason, decided_by, created_at
		FROM supervisor_actions ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SupervisorAction{}
	for rows.Next() {
		var a SupervisorAction
		if err := rows.Scan(&a.ID, &a.Kind, &a.DriverID, &a.TruckID, &a.DeliveryID, &a.Summary, &a.Reason, &a.DecidedBy, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// LockDriver takes the driver's row lock for the rest of the transaction
// and returns their name: two pairings of one driver queue up behind it
// instead of both seeing them truck-less. ErrNoRows when there's no such driver.
func (q *Queries) LockDriver(ctx context.Context, id uuid.UUID) (string, error) {
	var name string
	err := q.db.QueryRow(ctx, `SELECT name FROM drivers WHERE id = $1 FOR UPDATE`, id).Scan(&name)
	return name, err
}

// DriverName, or "" when the truck has no driver or they're gone.
func (q *Queries) DriverName(ctx context.Context, id uuid.UUID) (string, error) {
	var name string
	err := q.db.QueryRow(ctx, `SELECT name FROM drivers WHERE id = $1`, id).Scan(&name)
	if isNoRows(err) {
		return "", nil
	}
	return name, err
}

// TruckOfDriver is the plate of a truck the driver already drives, or "".
func (q *Queries) TruckOfDriver(ctx context.Context, driverID uuid.UUID) (string, error) {
	var plate string
	err := q.db.QueryRow(ctx, `SELECT plate_number FROM trucks WHERE driver_id = $1 LIMIT 1`, driverID).Scan(&plate)
	if isNoRows(err) {
		return "", nil
	}
	return plate, err
}

// SetTruckDriver gives a truck its driver.
func (q *Queries) SetTruckDriver(ctx context.Context, truckID, driverID uuid.UUID) error {
	_, err := q.db.Exec(ctx, `UPDATE trucks SET driver_id = $2 WHERE id = $1`, truckID, driverID)
	return err
}

// ExpirePendingProposals: a delivery the supervisor just assigned has
// nothing left for a dispatcher to approve.
func (q *Queries) ExpirePendingProposals(ctx context.Context, deliveryID uuid.UUID) error {
	_, err := q.db.Exec(ctx, `UPDATE proposals SET status = 'expired', decided_at = now() WHERE delivery_id = $1 AND status = 'pending'`, deliveryID)
	return err
}
