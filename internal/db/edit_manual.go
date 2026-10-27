package db

import (
	"context"

	"github.com/google/uuid"
)

// EditableDelivery is what an edit checks and compares against.
type EditableDelivery struct {
	ID            uuid.UUID
	Reference     string
	Status        string
	CustomerName  string
	CustomerPhone *string
	GoodsDesc     *string
	TruckID       *uuid.UUID
	// The assigned truck's driver, who's told about the change.
	DriverID *uuid.UUID
}

// GetDeliveryForEdit locks the delivery for the rest of the transaction.
func (q *Queries) GetDeliveryForEdit(ctx context.Context, id uuid.UUID) (EditableDelivery, error) {
	var d EditableDelivery
	err := q.db.QueryRow(ctx, `
		SELECT d.id, d.reference, d.status, d.customer_name, d.customer_phone, d.goods_desc, d.truck_id, t.driver_id
		FROM deliveries d
		LEFT JOIN trucks t ON t.id = d.truck_id
		WHERE d.id = $1
		FOR UPDATE OF d`, id).Scan(&d.ID, &d.Reference, &d.Status, &d.CustomerName, &d.CustomerPhone, &d.GoodsDesc, &d.TruckID, &d.DriverID)
	return d, err
}

func (q *Queries) UpdateDeliveryDetails(ctx context.Context, id uuid.UUID, name string, phone, goods *string) error {
	_, err := q.db.Exec(ctx, `
		UPDATE deliveries SET customer_name = $2, customer_phone = $3, goods_desc = $4, updated_at = now()
		WHERE id = $1`, id, name, phone, goods)
	return err
}

func (q *Queries) UpdateStopDetails(ctx context.Context, stopID uuid.UUID, address string, lat, lng float64, contactName, contactPhone *string) error {
	_, err := q.db.Exec(ctx, `
		UPDATE delivery_stops
		SET address = $2, position = ST_SetSRID(ST_MakePoint($4::float8, $3::float8), 4326)::geography,
		    contact_name = $5, contact_phone = $6
		WHERE id = $1`, stopID, address, lat, lng, contactName, contactPhone)
	return err
}
