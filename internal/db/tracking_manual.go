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
	// Who received it, and whether a photo and a signature can be fetched.
	Proof *TrackingProof `json:"proof"`
}

type TrackingProof struct {
	ReceiverName string    `json:"receiver_name"`
	CapturedAt   time.Time `json:"captured_at"`
	HasPhoto     bool      `json:"has_photo"`
	HasSignature bool      `json:"has_signature"`
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
	DueBy        *time.Time      `json:"due_by"`
	// Estimated arrival at the last stop; set by the API, not the query.
	ETA          *time.Time      `json:"eta"`
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
		       t.last_seen_at, t.plate_number, d.due_by
		FROM deliveries d
		LEFT JOIN trucks t ON t.id = d.truck_id
		WHERE d.tracking_token = $1`, token).
		Scan(&deliveryID, &d.Reference, &d.Status, &d.CustomerName, &d.GoodsDesc,
			&d.CreatedAt, &d.TruckLng, &d.TruckLat, &d.TruckSeenAt, &d.PlateNumber, &d.DueBy)
	if err != nil {
		return d, err
	}
	// A finished delivery keeps its truck_id, so without this an old link
	// would keep showing that truck's live position on its later jobs.
	if d.Status != "assigned" && d.Status != "in_transit" {
		d.TruckLng, d.TruckLat, d.TruckSeenAt = nil, nil, nil
	}

	stopRows, err := q.db.Query(ctx, `
		SELECT s.sequence, s.stop_type, s.address,
		       ST_X(s.position::geometry) AS lng,
		       ST_Y(s.position::geometry) AS lat,
		       s.status, s.arrived_at,
		       p.receiver_name, p.captured_at, p.photo IS NOT NULL, p.signature IS NOT NULL
		FROM delivery_stops s
		LEFT JOIN stop_proofs p ON p.stop_id = s.id
		WHERE s.delivery_id = $1
		ORDER BY s.sequence`, deliveryID)
	if err != nil {
		return d, err
	}
	defer stopRows.Close()

	d.Stops = []TrackingStop{}
	for stopRows.Next() {
		var s TrackingStop
		var name *string
		var at *time.Time
		var photo, sig *bool
		if err := stopRows.Scan(&s.Sequence, &s.StopType, &s.Address,
			&s.Lng, &s.Lat, &s.Status, &s.ArrivedAt, &name, &at, &photo, &sig); err != nil {
			return d, err
		}
		if name != nil {
			s.Proof = &TrackingProof{ReceiverName: *name, CapturedAt: *at, HasPhoto: *photo, HasSignature: *sig}
		}
		d.Stops = append(d.Stops, s)
	}
	if err := stopRows.Err(); err != nil {
		return d, err
	}

	eventRows, err := q.db.Query(ctx, `
		SELECT event_type,
		       -- Only these notes are for the customer: why it failed, and who signed.
		       -- Others hold internal detail (who reopened it, the agent's reasoning).
		       CASE WHEN event_type IN ('failed', 'proof') THEN note END,
		       created_at
		FROM delivery_events
		WHERE delivery_id = $1
		  AND event_type IN ('created', 'assigned', 'auto_assigned', 'in_transit', 'stop_arrived',
		                     'stop_completed', 'completed', 'failed', 'proof', 'cancelled', 'reopened')
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
