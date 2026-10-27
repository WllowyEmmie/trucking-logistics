package db

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// TruckSpecs is what a truck is (all optional), and whether it has a photo.
type TruckSpecs struct {
	TruckID     uuid.UUID  `json:"truck_id"`
	BodyType    *string    `json:"body_type"`
	CapacityKg  *int32     `json:"capacity_kg"`
	BodyLengthM *float64   `json:"body_length_m"`
	Pallets     *int32     `json:"pallets"`
	Year        *int32     `json:"year"`
	Fuel        *string    `json:"fuel"`
	HasPhoto    bool       `json:"has_photo"`
	PhotoAt     *time.Time `json:"photo_updated_at"`
}

// ListTruckSpecs: every truck that has specs or a photo.
func (q *Queries) ListTruckSpecs(ctx context.Context) ([]TruckSpecs, error) {
	rows, err := q.db.Query(ctx, `
		SELECT t.id, s.body_type, s.capacity_kg, s.body_length_m::float8, s.pallets, s.year, s.fuel,
		       p.truck_id IS NOT NULL, p.updated_at
		FROM trucks t
		LEFT JOIN truck_specs s ON s.truck_id = t.id
		LEFT JOIN truck_photos p ON p.truck_id = t.id
		WHERE s.truck_id IS NOT NULL OR p.truck_id IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TruckSpecs{}
	for rows.Next() {
		var s TruckSpecs
		if err := rows.Scan(&s.TruckID, &s.BodyType, &s.CapacityKg, &s.BodyLengthM, &s.Pallets, &s.Year, &s.Fuel, &s.HasPhoto, &s.PhotoAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// PutTruckSpecs replaces a truck's specs (a nil field clears it).
func (q *Queries) PutTruckSpecs(ctx context.Context, s TruckSpecs) error {
	_, err := q.db.Exec(ctx, `
		INSERT INTO truck_specs (truck_id, body_type, capacity_kg, body_length_m, pallets, year, fuel)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (truck_id) DO UPDATE SET
			body_type = EXCLUDED.body_type, capacity_kg = EXCLUDED.capacity_kg,
			body_length_m = EXCLUDED.body_length_m, pallets = EXCLUDED.pallets,
			year = EXCLUDED.year, fuel = EXCLUDED.fuel, updated_at = now()`,
		s.TruckID, s.BodyType, s.CapacityKg, s.BodyLengthM, s.Pallets, s.Year, s.Fuel)
	return err
}

func (q *Queries) PutTruckPhoto(ctx context.Context, truckID uuid.UUID, mime string, data []byte) error {
	_, err := q.db.Exec(ctx, `
		INSERT INTO truck_photos (truck_id, mime, data) VALUES ($1, $2, $3)
		ON CONFLICT (truck_id) DO UPDATE SET mime = EXCLUDED.mime, data = EXCLUDED.data, updated_at = now()`,
		truckID, mime, data)
	return err
}

func (q *Queries) TruckPhoto(ctx context.Context, truckID uuid.UUID) (string, []byte, time.Time, error) {
	var mime string
	var data []byte
	var at time.Time
	err := q.db.QueryRow(ctx, `SELECT mime, data, updated_at FROM truck_photos WHERE truck_id = $1`, truckID).Scan(&mime, &data, &at)
	return mime, data, at, err
}

func (q *Queries) DeleteTruckPhoto(ctx context.Context, truckID uuid.UUID) error {
	_, err := q.db.Exec(ctx, `DELETE FROM truck_photos WHERE truck_id = $1`, truckID)
	return err
}

// TruckExists: whether a truck with this id is there.
func (q *Queries) TruckExists(ctx context.Context, truckID uuid.UUID) (bool, error) {
	var ok bool
	err := q.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM trucks WHERE id = $1)`, truckID).Scan(&ok)
	return ok, err
}
