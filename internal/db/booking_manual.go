package db

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Booking is a request from the public booking page.
type Booking struct {
	ID           uuid.UUID       `json:"id"`
	Reference    string          `json:"reference"`
	CustomerName string          `json:"customer_name"`
	Email        string          `json:"email"`
	Phone        string          `json:"phone"`
	GoodsDesc    *string         `json:"goods_desc"`
	Stops        json.RawMessage `json:"stops"`
	DistanceKm   float64         `json:"distance_km"`
	Amount       int64           `json:"amount"`
	Currency     string          `json:"currency"`
	Status       string          `json:"status"`
	PaidAt       *time.Time      `json:"paid_at"`
	DeliveryID   *uuid.UUID      `json:"delivery_id"`
	CreatedAt    time.Time       `json:"created_at"`
}

const bookingCols = `id, reference, customer_name, email, phone, goods_desc, stops, distance_km,
	amount, currency, status, paid_at, delivery_id, created_at`

func scanBooking(row interface{ Scan(...any) error }) (Booking, error) {
	var b Booking
	err := row.Scan(&b.ID, &b.Reference, &b.CustomerName, &b.Email, &b.Phone, &b.GoodsDesc, &b.Stops,
		&b.DistanceKm, &b.Amount, &b.Currency, &b.Status, &b.PaidAt, &b.DeliveryID, &b.CreatedAt)
	return b, err
}

func (q *Queries) InsertBooking(ctx context.Context, b Booking) (Booking, error) {
	return scanBooking(q.db.QueryRow(ctx, `
		INSERT INTO bookings (reference, customer_name, email, phone, goods_desc, stops, distance_km, amount, currency)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING `+bookingCols,
		b.Reference, b.CustomerName, b.Email, b.Phone, b.GoodsDesc, b.Stops, b.DistanceKm, b.Amount, b.Currency))
}

func (q *Queries) GetBooking(ctx context.Context, reference string) (Booking, error) {
	return scanBooking(q.db.QueryRow(ctx, `SELECT `+bookingCols+` FROM bookings WHERE reference = $1`, reference))
}

// GetBookingForUpdate locks the booking: the webhook and the customer's
// return can arrive together, and only one may turn it into a delivery.
func (q *Queries) GetBookingForUpdate(ctx context.Context, reference string) (Booking, error) {
	return scanBooking(q.db.QueryRow(ctx, `SELECT `+bookingCols+` FROM bookings WHERE reference = $1 FOR UPDATE`, reference))
}

func (q *Queries) MarkBookingPaid(ctx context.Context, id, deliveryID uuid.UUID) error {
	_, err := q.db.Exec(ctx, `UPDATE bookings SET status = 'paid', paid_at = now(), delivery_id = $2 WHERE id = $1`, id, deliveryID)
	return err
}

func (q *Queries) MarkBookingFailed(ctx context.Context, id uuid.UUID) error {
	_, err := q.db.Exec(ctx, `UPDATE bookings SET status = 'failed' WHERE id = $1 AND status = 'awaiting_payment'`, id)
	return err
}

// DeliveryLink is a delivery's reference and tracking token, for the
// customer who just paid for it.
func (q *Queries) DeliveryLink(ctx context.Context, id uuid.UUID) (reference, token string, err error) {
	err = q.db.QueryRow(ctx, `SELECT reference, tracking_token FROM deliveries WHERE id = $1`, id).Scan(&reference, &token)
	return
}
