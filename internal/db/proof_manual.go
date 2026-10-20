package db

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

// ProofSummary is what lists and timelines show of a proof of delivery; the
// images are fetched separately.
type ProofSummary struct {
	ReceiverName string     `json:"receiver_name"`
	CapturedAt   time.Time  `json:"captured_at"`
	HasPhoto     bool       `json:"has_photo"`
	HasSignature bool       `json:"has_signature"`
	Lat          *float64   `json:"lat"`
	Lng          *float64   `json:"lng"`
	SavedBy      string     `json:"saved_by"`
}

type SaveProofParams struct {
	StopID, DeliveryID uuid.UUID
	ReceiverName       string
	Photo              []byte
	PhotoType          string
	Signature          []byte
	SignatureType      string
	Lat, Lng           *float64
	CapturedAt         time.Time
	SavedBy            string
}

// SaveProof writes the stop's proof, replacing an earlier one (a retake
// before the stop is completed).
func (q *Queries) SaveProof(ctx context.Context, p SaveProofParams) error {
	nilIfEmpty := func(b []byte) any {
		if len(b) == 0 {
			return nil
		}
		return b
	}
	strOrNil := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	_, err := q.db.Exec(ctx, `
		INSERT INTO stop_proofs (stop_id, delivery_id, receiver_name, photo, photo_type, signature, signature_type, lat, lng, captured_at, saved_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (stop_id) DO UPDATE SET
		  receiver_name = EXCLUDED.receiver_name, photo = EXCLUDED.photo, photo_type = EXCLUDED.photo_type,
		  signature = EXCLUDED.signature, signature_type = EXCLUDED.signature_type,
		  lat = EXCLUDED.lat, lng = EXCLUDED.lng, captured_at = EXCLUDED.captured_at,
		  saved_at = now(), saved_by = EXCLUDED.saved_by`,
		p.StopID, p.DeliveryID, p.ReceiverName, nilIfEmpty(p.Photo), strOrNil(p.PhotoType),
		nilIfEmpty(p.Signature), strOrNil(p.SignatureType), p.Lat, p.Lng, p.CapturedAt, p.SavedBy)
	return err
}

// GetProof is the stop's proof summary, or nil when there's none.
func (q *Queries) GetProof(ctx context.Context, stopID uuid.UUID) (*ProofSummary, error) {
	var s ProofSummary
	err := q.db.QueryRow(ctx, `
		SELECT receiver_name, captured_at, photo IS NOT NULL, signature IS NOT NULL, lat, lng, saved_by
		FROM stop_proofs WHERE stop_id = $1`, stopID).
		Scan(&s.ReceiverName, &s.CapturedAt, &s.HasPhoto, &s.HasSignature, &s.Lat, &s.Lng, &s.SavedBy)
	if err != nil {
		if isNoRows(err) {
			return nil, nil
		}
		return nil, err
	}
	return &s, nil
}

// ProofImage is one image of a proof: kind "photo" or "signature". ErrNoRows
// when the stop (on that delivery) has no such image.
func (q *Queries) ProofImage(ctx context.Context, deliveryID, stopID uuid.UUID, kind string) (data []byte, contentType string, err error) {
	col, typ := "photo", "photo_type"
	if kind == "signature" {
		col, typ = "signature", "signature_type"
	}
	err = q.db.QueryRow(ctx, `SELECT `+col+`, `+typ+` FROM stop_proofs
		WHERE stop_id = $1 AND delivery_id = $2 AND `+col+` IS NOT NULL`, stopID, deliveryID).Scan(&data, &contentType)
	return
}

// StopIDBySequence: for the public tracking page, which knows stops by number.
func (q *Queries) StopIDBySequence(ctx context.Context, deliveryID uuid.UUID, seq int32) (uuid.UUID, error) {
	var id uuid.UUID
	err := q.db.QueryRow(ctx, `SELECT id FROM delivery_stops WHERE delivery_id = $1 AND sequence = $2`, deliveryID, seq).Scan(&id)
	return id, err
}

// DeliveryIDByToken: the delivery behind a tracking link.
func (q *Queries) DeliveryIDByToken(ctx context.Context, token string) (uuid.UUID, error) {
	var id uuid.UUID
	err := q.db.QueryRow(ctx, `SELECT id FROM deliveries WHERE tracking_token = $1`, token).Scan(&id)
	return id, err
}
