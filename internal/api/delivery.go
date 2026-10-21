package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/WllowyEmmie/trucking-logistics/internal/db"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

var validDeliveryStatuses = map[string]bool{
	"pending":    true,
	"assigned":   true,
	"in_transit": true,
	"completed":  true,
	"cancelled":  true,
	"failed":     true,
}
var validFailureReasons = map[string]bool{
	"breakdown":            true,
	"customer_unavailable": true,
	"goods_damaged":        true,
	"accident":             true,
	"other":                true,
}

type stopInput struct {
	StopType     string   `json:"stop_type"`
	Address      string   `json:"address"`
	Lat          *float64 `json:"lat"`
	Lng          *float64 `json:"lng"`
	ContactName  *string  `json:"contact_name"`
	ContactPhone *string  `json:"contact_phone"`
}

type createDeliveryInput struct {
	CustomerName  string      `json:"customer_name"`
	CustomerPhone *string     `json:"customer_phone"`
	GoodsDesc     *string     `json:"goods_desc"`
	Stops         []stopInput `json:"stops"`
	// Promised by, for the last drop-off. Optional.
	DueBy *time.Time `json:"due_by"`
}

func (s *Server) listDeliveries(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status != "" && !validDeliveryStatuses[status] {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid delivery status"})
		return
	}
	// ?board=1: what the console shows (see ListDeliveryItems). Older
	// finished deliveries are in GET /deliveries/history.
	var since *time.Time
	if r.URL.Query().Get("board") == "1" {
		t := time.Now().Add(-time.Duration(s.st().BoardHours) * time.Hour)
		since = &t
	}
	items, err := s.queries.ListDeliveryItems(r.Context(), status, since)
	if err != nil {
		s.respondErr(w, err, "list deliveries", "could not list deliveries")
		return
	}
	items = onlyMine(r, items, func(p *principal, d db.DeliveryItem) bool { return onTruckOf(p, d.TruckID) })
	out, err := s.withETAs(r, items)
	if err != nil {
		s.respondErr(w, err, "estimate arrivals", "could not list deliveries")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// assignTruckTx applies the assignment rules inside a caller's transaction.
// Both the dispatcher's endpoint and the agent's proposal approval go
// through here, so neither can create a state the other forbids.
func (s *Server) assignTruckTx(
	ctx context.Context, qtx *db.Queries, deliveryID, truckID uuid.UUID,
) (db.AssignTruckRow, error) {
	var zero db.AssignTruckRow

	delivery, err := qtx.GetDeliveryForUpdate(ctx, deliveryID)
	if errors.Is(err, pgx.ErrNoRows) {
		return zero, errNotFound("delivery not found")
	}
	if err != nil {
		return zero, err
	}
	if delivery.Status != "pending" {
		return zero, errConflict("delivery is " + delivery.Status + " and cannot be assigned")
	}

	truck, err := qtx.GetTruckForUpdate(ctx, truckID)
	if errors.Is(err, pgx.ErrNoRows) {
		return zero, errNotFound("truck not found")
	}
	if err != nil {
		return zero, err
	}
	if truck.Status != "available" {
		return zero, errConflict("truck " + truck.PlateNumber + " is " + truck.Status)
	}
	// Nobody would drive it: the delivery would sit "assigned" with no one
	// to start it, and no driver app would ever show it.
	if truck.DriverID == nil {
		return zero, errConflict("truck " + truck.PlateNumber + " has no driver; give it a driver in Fleet first")
	}

	updated, err := qtx.AssignTruck(ctx, db.AssignTruckParams{
		ID:      deliveryID,
		TruckID: &truckID,
	})
	if err != nil {
		return zero, err
	}

	if err := qtx.SetTruckStatus(ctx, db.SetTruckStatusParams{
		ID:     truckID,
		Status: "on_delivery",
	}); err != nil {
		return zero, err
	}

	note := "assigned to " + truck.PlateNumber
	if err := qtx.CreateDeliveryEvent(ctx, db.CreateDeliveryEventParams{
		DeliveryID: deliveryID,
		EventType:  "assigned",
		Note:       &note,
	}); err != nil {
		return zero, err
	}

	return updated, nil
}

func (s *Server) assignTruck(w http.ResponseWriter, r *http.Request) {
	deliveryID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid delivery id"})
		return
	}

	var body struct {
		TruckID string `json:"truck_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	truckID, err := uuid.Parse(body.TruckID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid truck id"})
		return
	}

	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.logger.Error("begin tx", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not assign truck"})
		return
	}
	defer tx.Rollback(r.Context())

	updated, err := s.assignTruckTx(r.Context(), s.queries.WithTx(tx), deliveryID, truckID)
	if err != nil {
		s.respondErr(w, err, "assign truck", "could not assign truck")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		s.logger.Error("commit tx", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not assign truck"})
		return
	}

	writeJSON(w, http.StatusOK, updated)
}
func (s *Server) startDelivery(w http.ResponseWriter, r *http.Request) {
	deliveryID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid delivery id"})
		return
	}

	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.logger.Error("begin tx", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not update delivery status"})
		return
	}
	defer tx.Rollback(r.Context())

	qtx := s.queries.WithTx(tx)
	delivery, err := qtx.GetDeliveryForUpdate(r.Context(), deliveryID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "delivery not found"})
			return
		}
		s.logger.Error("get delivery", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not update delivery status"})
		return
	}
	if delivery.Status != "assigned" {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "delivery is " + delivery.Status + " and cannot be set to in_transit",
		})
		return
	}
	updated, err := qtx.SetDeliveryStatus(r.Context(), db.SetDeliveryStatusParams{
		ID:     delivery.ID,
		Status: "in_transit",
	})
	if err != nil {
		s.logger.Error("set status", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not update delivery status"})
		return
	}
	// Setting off starts the driver's shift, so their GPS is recorded.
	if p := principalFrom(r.Context()); p != nil && p.Role == roleDriver {
		if _, err := qtx.StartShift(r.Context(), p.ID); err != nil {
			s.respondErr(w, err, "start shift", "could not update delivery status")
			return
		}
	}
	if err := qtx.CreateDeliveryEvent(r.Context(), db.CreateDeliveryEventParams{
		DeliveryID: deliveryID,
		EventType:  "in_transit",
	}); err != nil {
		s.logger.Error("create event", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not update delivery"})
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		s.logger.Error("commit tx", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not update delivery status"})
		return
	}

	writeJSON(w, http.StatusOK, updated)
}
func (s *Server) completeDelivery(w http.ResponseWriter, r *http.Request) {
	deliveryID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid delivery id"})
		return
	}

	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.logger.Error("begin tx", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not update delivery and truck status"})
		return
	}
	defer tx.Rollback(r.Context())
	qtx := s.queries.WithTx(tx)

	delivery, err := qtx.GetDeliveryForUpdate(r.Context(), deliveryID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "delivery not found"})
			return
		}
		s.logger.Error("get delivery", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not update delivery status"})
		return
	}
	if delivery.Status != "in_transit" {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "delivery is " + delivery.Status + " and cannot be updated to completed",
		})
		return
	}
	// Every stop must be done first, or the record says "delivered" while
	// goods are still on the truck. The driver app sends the last stop's
	// arrive/complete ahead of /complete, so this costs it nothing.
	open, err := qtx.CountIncompleteStops(r.Context(), deliveryID)
	if err != nil {
		s.logger.Error("count stops", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not update delivery status"})
		return
	}
	if open > 0 {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "delivery still has open stops; complete every stop first",
		})
		return
	}
	updatedDelivery, err := qtx.SetDeliveryStatus(r.Context(), db.SetDeliveryStatusParams{
		ID:     delivery.ID,
		Status: "completed",
	})
	if err != nil {
		s.logger.Error("set status", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not update delivery status"})
		return
	}
	if err := qtx.CreateDeliveryEvent(r.Context(), db.CreateDeliveryEventParams{
		DeliveryID: deliveryID,
		EventType:  "completed",
	}); err != nil {
		s.logger.Error("create event", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not update delivery"})
		return
	}
	if delivery.TruckID == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "delivery has no truck assigned"})
		return
	}

	truck, err := qtx.GetTruckForUpdate(r.Context(), *delivery.TruckID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "truck not found"})
			return
		}
		s.logger.Error("get truck", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not update truck status"})
		return
	}
	if truck.Status != "on_delivery" {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "truck " + truck.PlateNumber + " is " + truck.Status,
		})
		return
	}

	err = qtx.SetTruckStatus(r.Context(), db.SetTruckStatusParams{
		ID:     truck.ID,
		Status: "available",
	})
	if err != nil {
		s.logger.Error("set status", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not update trucks status"})
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		s.logger.Error("commit tx", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not update delivery and truck status"})
		return
	}

	writeJSON(w, http.StatusOK, updatedDelivery)

}
func (s *Server) failDelivery(w http.ResponseWriter, r *http.Request) {
	deliveryID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid delivery id"})
		return
	}

	var body struct {
		Reason string `json:"reason"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if !validFailureReasons[body.Reason] {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "reason must be one of: breakdown, customer_unavailable, goods_damaged, accident, other",
		})
		return
	}
	nextStatus := "available"
	switch body.Reason {
	case "breakdown", "accident":
		nextStatus = "maintenance"
	}

	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.logger.Error("begin tx", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not update delivery and truck status"})
		return
	}
	defer tx.Rollback(r.Context())
	qtx := s.queries.WithTx(tx)

	delivery, err := qtx.GetDeliveryForUpdate(r.Context(), deliveryID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "delivery not found"})
			return
		}
		s.logger.Error("get delivery", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not update delivery status"})
		return
	}
	if delivery.Status != "in_transit" {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "delivery is " + delivery.Status + " and cannot be marked failed",
		})
		return
	}

	updatedDelivery, err := qtx.SetDeliveryStatus(r.Context(), db.SetDeliveryStatusParams{
		ID:     delivery.ID,
		Status: "failed",
	})
	if err != nil {
		s.logger.Error("set status", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not update delivery status"})
		return
	}
	// The note is the bare reason: clients read it as data. The truck going
	// to maintenance is its own event, below.
	if err := qtx.CreateDeliveryEvent(r.Context(), db.CreateDeliveryEventParams{
		DeliveryID: deliveryID,
		EventType:  "failed",
		Note:       &body.Reason,
	}); err != nil {
		s.logger.Error("create event", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not update delivery"})
		return
	}

	if delivery.TruckID == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "delivery has no truck assigned"})
		return
	}

	truck, err := qtx.GetTruckForUpdate(r.Context(), *delivery.TruckID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "truck not found"})
			return
		}
		s.logger.Error("get truck", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not update truck status"})
		return
	}
	if truck.Status != "on_delivery" {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "truck " + truck.PlateNumber + " is " + truck.Status,
		})
		return
	}

	if err := qtx.SetTruckStatus(r.Context(), db.SetTruckStatusParams{
		ID:     truck.ID,
		Status: nextStatus,
	}); err != nil {
		s.logger.Error("set truck status", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not update truck status"})
		return
	}

	if nextStatus == "maintenance" {
		if err := qtx.CreateDeliveryEvent(r.Context(), db.CreateDeliveryEventParams{
			DeliveryID: deliveryID,
			EventType:  "truck_maintenance",
		}); err != nil {
			s.logger.Error("create event", "err", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not update delivery"})
			return
		}
	}

	if err := tx.Commit(r.Context()); err != nil {
		s.logger.Error("commit tx", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not update delivery and truck status"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"delivery": updatedDelivery,
		"reason":   body.Reason,
	})
}
func (s *Server) cancelDelivery(w http.ResponseWriter, r *http.Request) {
	deliveryID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid delivery id"})
		return
	}

	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.logger.Error("begin tx", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not update delivery and truck status"})
		return
	}
	defer tx.Rollback(r.Context())
	qtx := s.queries.WithTx(tx)

	delivery, err := qtx.GetDeliveryForUpdate(r.Context(), deliveryID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "delivery not found"})
			return
		}
		s.logger.Error("get delivery", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not cancel delivery"})
		return
	}
	// In transit: the customer called it off on the way. Failed: it won't
	// be sent out again, so it leaves the console's failed list.
	switch delivery.Status {
	case "pending", "assigned", "in_transit", "failed":
	default:
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "delivery is " + delivery.Status + " and cannot be cancelled",
		})
		return
	}
	updatedDelivery, err := qtx.ReleaseTruck(r.Context(), db.ReleaseTruckParams{
		ID:     delivery.ID,
		Status: "cancelled",
	})
	if err != nil {
		s.logger.Error("set status", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not cancel delivery"})
		return
	}
	if err := qtx.CreateDeliveryEvent(r.Context(), db.CreateDeliveryEventParams{
		DeliveryID: deliveryID,
		EventType:  "cancelled",
	}); err != nil {
		s.logger.Error("create event", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not cancel delivery"})
		return
	}
	// A failed delivery already let its truck go (available, or the workshop).
	if delivery.TruckID != nil && delivery.Status != "failed" {
		truck, err := qtx.GetTruckForUpdate(r.Context(), *delivery.TruckID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "truck not found"})
				return
			}
			s.logger.Error("get truck", "err", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not update truck status"})
			return
		}
		if truck.Status != "on_delivery" {
			writeJSON(w, http.StatusConflict, map[string]string{
				"error": "truck " + truck.PlateNumber + " is " + truck.Status,
			})
			return
		}
		err = qtx.SetTruckStatus(r.Context(), db.SetTruckStatusParams{
			ID:     truck.ID,
			Status: "available",
		})
		if err != nil {
			s.logger.Error("set status", "err", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not update trucks status"})
			return
		}
	}

	if err := tx.Commit(r.Context()); err != nil {
		s.logger.Error("commit tx", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not update delivery and truck status"})
		return
	}

	writeJSON(w, http.StatusOK, updatedDelivery)
}

func (s *Server) createDelivery(w http.ResponseWriter, r *http.Request) {
	var body createDeliveryInput
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}

	if msg := s.validateDeliveryInput(body); msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}

	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.logger.Error("begin tx", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not create delivery"})
		return
	}
	defer tx.Rollback(r.Context())

	delivery, stops, err := s.insertDelivery(r.Context(), s.queries.WithTx(tx), body, "")
	if err != nil {
		s.respondErr(w, err, "create delivery", "could not create delivery")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		s.logger.Error("commit tx", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not create delivery"})
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"delivery": delivery,
		"stops":    stops,
	})
}

// insertDelivery writes a checked delivery, its stops, its due time and its
// "created" event in the caller's transaction. note goes on the event (e.g.
// "booked online").
func (s *Server) insertDelivery(ctx context.Context, qtx *db.Queries, body createDeliveryInput, note string) (db.CreateDeliveryRow, []db.CreateDeliveryStopRow, error) {
	token, err := newTrackingToken()
	if err != nil {
		return db.CreateDeliveryRow{}, nil, err
	}
	delivery, err := qtx.CreateDelivery(ctx, db.CreateDeliveryParams{
		TrackingToken: token,
		CustomerName:  body.CustomerName,
		CustomerPhone: body.CustomerPhone,
		GoodsDesc:     body.GoodsDesc,
	})
	if err != nil {
		return delivery, nil, err
	}
	stops := make([]db.CreateDeliveryStopRow, 0, len(body.Stops))
	for i, in := range body.Stops {
		stop, err := qtx.CreateDeliveryStop(ctx, db.CreateDeliveryStopParams{
			DeliveryID:   delivery.ID,
			Sequence:     int32(i + 1),
			StopType:     in.StopType,
			Address:      in.Address,
			Lng:          *in.Lng,
			Lat:          *in.Lat,
			ContactName:  in.ContactName,
			ContactPhone: in.ContactPhone,
		})
		if err != nil {
			return delivery, nil, err
		}
		stops = append(stops, stop)
	}
	if body.DueBy != nil {
		if _, err := qtx.SetDueBy(ctx, delivery.ID, body.DueBy); err != nil {
			return delivery, nil, err
		}
	}
	var n *string
	if note != "" {
		n = &note
	}
	if err := qtx.CreateDeliveryEvent(ctx, db.CreateDeliveryEventParams{
		DeliveryID: delivery.ID,
		EventType:  "created",
		Note:       n,
	}); err != nil {
		return delivery, nil, err
	}
	return delivery, stops, nil
}

func (s *Server) getDelivery(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid delivery id"})
		return
	}

	detail, err := s.queries.GetDeliveryDetail(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "delivery not found"})
		return
	}
	if err != nil {
		s.logger.Error("get delivery", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not load delivery"})
		return
	}

	stops, err := s.queries.ListStopsForDelivery(r.Context(), id)
	if err != nil {
		s.logger.Error("list stops", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not load delivery"})
		return
	}

	events, err := s.queries.ListEventsForDelivery(r.Context(), id)
	if err != nil {
		s.logger.Error("list events", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not load delivery"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"delivery": detail,
		"stops":    stops,
		"events":   events,
		// Estimated arrival at each stop left, and at the last; null when
		// it isn't on the road or the truck has no position.
		"eta": s.estimate(detail.Status, detail.TruckLat, detail.TruckLng, stops),
	})
}
func (s *Server) validateDeliveryInput(body createDeliveryInput) string {
	if body.CustomerName == "" {
		return "customer_name is required"
	}
	if msg := checkDue(body.DueBy); msg != "" {
		return msg
	}
	if len(body.Stops) < 2 {
		return "at least two stops are required"
	}
	if body.Stops[0].StopType != "pickup" {
		return "the first stop must be a pickup"
	}

	area := s.st()
	for i, st := range body.Stops {
		if st.StopType != "pickup" && st.StopType != "dropoff" {
			return "stop_type must be pickup or dropoff"
		}
		if st.Address == "" {
			return "every stop needs an address"
		}
		if st.Lat == nil || st.Lng == nil {
			return "every stop needs lat and lng"
		}
		if !area.Contains(*st.Lat, *st.Lng) {
			return "coordinates must be within the service area — check that lat and lng aren't swapped"
		}
		if i > 0 && st.StopType == "pickup" {
			return "only the first stop can be a pickup"
		}
	}
	return ""
}
