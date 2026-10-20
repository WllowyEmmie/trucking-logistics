package db

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type DeliveryDetail struct {
	ID            uuid.UUID  `json:"id"`
	Reference     string     `json:"reference"`
	Status        string     `json:"status"`
	CustomerName  string     `json:"customer_name"`
	CustomerPhone *string    `json:"customer_phone"`
	GoodsDesc     *string    `json:"goods_desc"`
	TrackingToken string     `json:"tracking_token"`
	TruckID       *uuid.UUID `json:"truck_id"`
	PlateNumber   *string    `json:"plate_number"`
	TruckLng      *float64   `json:"truck_lng"`
	TruckLat      *float64   `json:"truck_lat"`
	TruckSeenAt   *time.Time `json:"truck_seen_at"`
	CreatedAt     time.Time  `json:"created_at"`
	DueBy         *time.Time `json:"due_by"`
}

func (q *Queries) GetDeliveryDetail(ctx context.Context, id uuid.UUID) (DeliveryDetail, error) {
	var d DeliveryDetail
	err := q.db.QueryRow(ctx, `
		SELECT d.id, d.reference, d.status, d.customer_name, d.customer_phone,
		       d.goods_desc, d.tracking_token, d.truck_id, t.plate_number,
		       ST_X(t.last_position::geometry) AS truck_lng,
		       ST_Y(t.last_position::geometry) AS truck_lat,
		       t.last_seen_at, d.created_at, d.due_by
		FROM deliveries d
		LEFT JOIN trucks t ON t.id = d.truck_id
		WHERE d.id = $1`, id).Scan(
		&d.ID, &d.Reference, &d.Status, &d.CustomerName, &d.CustomerPhone,
		&d.GoodsDesc, &d.TrackingToken, &d.TruckID, &d.PlateNumber,
		&d.TruckLng, &d.TruckLat, &d.TruckSeenAt, &d.CreatedAt, &d.DueBy)
	return d, err
}
