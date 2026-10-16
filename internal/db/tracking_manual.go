package db

import (
	"context"
	"time"
)

type TrackingStop struct {
	Sequence  int32      `json:"sequence"`
	StopType  string     `json:"stop_type"`
	Address   string     `json:"address"`
	Lng       float64    `json:"lng"`
	Lat       float64    `json:"lat"`
	Status    string     `json:"status"`
	ArrivedAt *time.Time `json:"arrived_at"`
}

type TrackingEvent struct {
	EventType string    `json:"event_type"`
	Note      *string   `json:"note"`
	CreatedAt time.Time `json:"created_at"`
}

type TrackingDelivery struct {
	Reference    string          `json:"reference"`
	Status       string          `json:"status"`
	PlateNumber  *string         `json:"plate_number"`
	CustomerName string          `json:"customer_name"`
	GoodsDesc    *string         `json:"goods_desc"`
	CreatedAt    time.Time       `json:"created_at"`
	TruckLng     *float64        `json:"truck_lng"`
	TruckLat     *float64        `json:"truck_lat"`
	TruckSeenAt  *time.Time      `json:"truck_seen_at"`
	Stops        []TrackingStop  `json:"stops"`
	Events       []TrackingEvent `json:"events"`
}

func (q *Queries) GetDeliveryByToken(ctx context.Context, token string) (TrackingDelivery, error) {
	var d TrackingDelivery
	var deliveryID string

	err := q.db.QueryRow(ctx, `
		SELECT d.id, d.reference, d.status, d.customer_name, d.goods_desc, d.created_at,
		       ST_X(t.last_position::geometry) AS truck_lng,
		       ST_Y(t.last_position::geometry) AS truck_lat,
		       t.last_seen_at, t.plate_number
		FROM deliveries d
		LEFT JOIN trucks t ON t.id = d.truck_id
		WHERE d.tracking_token = $1`, token).
		Scan(&deliveryID, &d.Reference, &d.Status, &d.CustomerName, &d.GoodsDesc,
			&d.CreatedAt, &d.TruckLng, &d.TruckLat, &d.TruckSeenAt, &d.PlateNumber)
	if err != nil {
		return d, err
	}

	stopRows, err := q.db.Query(ctx, `
		SELECT sequence, stop_type, address,
		       ST_X(position::geometry) AS lng,
		       ST_Y(position::geometry) AS lat,
		       status, arrived_at
		FROM delivery_stops
		WHERE delivery_id = $1
		ORDER BY sequence`, deliveryID)
	if err != nil {
		return d, err
	}
	defer stopRows.Close()

	d.Stops = []TrackingStop{}
	for stopRows.Next() {
		var s TrackingStop
		if err := stopRows.Scan(&s.Sequence, &s.StopType, &s.Address,
			&s.Lng, &s.Lat, &s.Status, &s.ArrivedAt); err != nil {
			return d, err
		}
		d.Stops = append(d.Stops, s)
	}
	if err := stopRows.Err(); err != nil {
		return d, err
	}

	eventRows, err := q.db.Query(ctx, `
		SELECT event_type, note, created_at
		FROM delivery_events
		WHERE delivery_id = $1
		ORDER BY created_at, id`, deliveryID)
	if err != nil {
		return d, err
	}
	defer eventRows.Close()

	d.Events = []TrackingEvent{}
	for eventRows.Next() {
		var e TrackingEvent
		if err := eventRows.Scan(&e.EventType, &e.Note, &e.CreatedAt); err != nil {
			return d, err
		}
		d.Events = append(d.Events, e)
	}
	return d, eventRows.Err()
}
