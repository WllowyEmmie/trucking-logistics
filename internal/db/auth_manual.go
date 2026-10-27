package db

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Queries for sign-in and sessions. Hand-written like the other *_manual.go
// files, so sqlc doesn't have to be re-run.

type Dispatcher struct {
	ID           uuid.UUID `json:"id"`
	Email        string    `json:"email"`
	Name         string    `json:"name"`
	PasswordHash string    `json:"-"`
}

func (q *Queries) GetDispatcherByEmail(ctx context.Context, email string) (Dispatcher, error) {
	var d Dispatcher
	err := q.db.QueryRow(ctx, `SELECT id, email, name, password_hash FROM dispatchers WHERE email = lower($1)`, email).
		Scan(&d.ID, &d.Email, &d.Name, &d.PasswordHash)
	return d, err
}

func (q *Queries) GetDispatcher(ctx context.Context, id uuid.UUID) (Dispatcher, error) {
	var d Dispatcher
	err := q.db.QueryRow(ctx, `SELECT id, email, name, password_hash FROM dispatchers WHERE id = $1`, id).
		Scan(&d.ID, &d.Email, &d.Name, &d.PasswordHash)
	return d, err
}

// UpsertDispatcher creates a dispatcher, or resets the name and password of
// an existing one. Used by cmd/adduser only.
func (q *Queries) UpsertDispatcher(ctx context.Context, email, name, passwordHash string) (Dispatcher, error) {
	var d Dispatcher
	err := q.db.QueryRow(ctx, `
		INSERT INTO dispatchers (email, name, password_hash) VALUES (lower($1), $2, $3)
		ON CONFLICT (email) DO UPDATE SET name = EXCLUDED.name, password_hash = EXCLUDED.password_hash
		RETURNING id, email, name, password_hash`, email, name, passwordHash).
		Scan(&d.ID, &d.Email, &d.Name, &d.PasswordHash)
	return d, err
}

type DriverLogin struct {
	ID      uuid.UUID
	Name    string
	Phone   string
	PinHash *string
	// Still on the default PIN: they must choose their own before anything else.
	MustChangePIN bool
}

// GetDriverByPhone matches on digits and a leading + only, so a number typed
// with spaces still finds a driver stored without them, and the other way.
func (q *Queries) GetDriverByPhone(ctx context.Context, phone string) (DriverLogin, error) {
	var d DriverLogin
	err := q.db.QueryRow(ctx, `
		SELECT id, name, phone, pin_hash, pin_must_change FROM drivers
		WHERE regexp_replace(phone, '[^0-9+]', '', 'g') = $1
		ORDER BY created_at LIMIT 1`, phone).Scan(&d.ID, &d.Name, &d.Phone, &d.PinHash, &d.MustChangePIN)
	return d, err
}

func (q *Queries) GetDriverLogin(ctx context.Context, id uuid.UUID) (DriverLogin, error) {
	var d DriverLogin
	err := q.db.QueryRow(ctx, `SELECT id, name, phone, pin_hash, pin_must_change FROM drivers WHERE id = $1`, id).
		Scan(&d.ID, &d.Name, &d.Phone, &d.PinHash, &d.MustChangePIN)
	return d, err
}

// SetDriverPIN stores a new PIN hash and signs the driver out everywhere, so
// a phone that knew the old PIN can't carry on. Returns false if no driver.
func (q *Queries) SetDriverPIN(ctx context.Context, id uuid.UUID, pinHash string) (bool, error) {
	return q.setPIN(ctx, id, pinHash, false)
}

// SetDefaultDriverPIN gives a new driver the default PIN, which they must
// change at their first sign-in.
func (q *Queries) SetDefaultDriverPIN(ctx context.Context, id uuid.UUID, pinHash string) (bool, error) {
	return q.setPIN(ctx, id, pinHash, true)
}

func (q *Queries) setPIN(ctx context.Context, id uuid.UUID, pinHash string, mustChange bool) (bool, error) {
	tag, err := q.db.Exec(ctx, `UPDATE drivers SET pin_hash = $2, pin_must_change = $3 WHERE id = $1`, id, pinHash, mustChange)
	if err != nil || tag.RowsAffected() == 0 {
		return false, err
	}
	_, err = q.db.Exec(ctx, `DELETE FROM sessions WHERE role = 'driver' AND subject_id = $1`, id)
	return true, err
}

// ChangeOwnPIN is the driver choosing their PIN: it clears the must-change
// mark and keeps them signed in on the phone that did it.
func (q *Queries) ChangeOwnPIN(ctx context.Context, id uuid.UUID, pinHash string) error {
	_, err := q.db.Exec(ctx, `UPDATE drivers SET pin_hash = $2, pin_must_change = false WHERE id = $1`, id, pinHash)
	return err
}

// TrucksForDriver: the trucks a driver may act for (those with them as the
// usual driver). Drivers are linked to trucks, and deliveries to trucks.
func (q *Queries) TrucksForDriver(ctx context.Context, driverID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := q.db.Query(ctx, `SELECT id FROM trucks WHERE driver_id = $1 ORDER BY plate_number`, driverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

type Session struct {
	Role      string
	SubjectID uuid.UUID
	ExpiresAt time.Time
}

func (q *Queries) CreateSession(ctx context.Context, tokenHash, role string, subject uuid.UUID, expires time.Time) error {
	// Expired rows are cleared here rather than by a job: logins are rare
	// and the index makes it cheap.
	if _, err := q.db.Exec(ctx, `DELETE FROM sessions WHERE expires_at < now()`); err != nil {
		return err
	}
	_, err := q.db.Exec(ctx, `INSERT INTO sessions (token_hash, role, subject_id, expires_at) VALUES ($1, $2, $3, $4)`,
		tokenHash, role, subject, expires)
	return err
}

func (q *Queries) GetSession(ctx context.Context, tokenHash string) (Session, error) {
	var s Session
	err := q.db.QueryRow(ctx, `SELECT role, subject_id, expires_at FROM sessions WHERE token_hash = $1 AND expires_at > now()`, tokenHash).
		Scan(&s.Role, &s.SubjectID, &s.ExpiresAt)
	return s, err
}

func (q *Queries) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := q.db.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, tokenHash)
	return err
}

// DriversWithPIN: which drivers can sign in yet.
func (q *Queries) DriversWithPIN(ctx context.Context) (map[uuid.UUID]bool, error) {
	rows, err := q.db.Query(ctx, `SELECT id FROM drivers WHERE pin_hash IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]bool{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}
