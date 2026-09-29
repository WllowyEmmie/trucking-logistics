package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/WllowyEmmie/trucking-logistics/internal/db"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

const (
	minLat = 4.0
	maxLat = 14.0
	minLng = 2.5
	maxLng = 15.0
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
}

func (s *Server) listDeliveries(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status != "" {
		if !validDeliveryStatuses[status] {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "invalid delivery status",
			})
			return
		}
		deliveries, err := s.queries.ListDeliveriesByStatus(r.Context(), status)
		if err != nil {
			s.logger.Error("list deliveries", "err", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not list deliveries"})
			return
		}
		writeJSON(w, http.StatusOK, deliveries)
		return
	}

	deliveries, err := s.queries.ListDeliveries(r.Context())
	if err != nil {
		s.logger.Error("list deliveries", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not list deliveries"})
		return
	}
	writeJSON(w, http.StatusOK, deliveries)
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

	qtx := s.queries.WithTx(tx)

	delivery, err := qtx.GetDelivery(r.Context(), deliveryID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "delivery not found"})
			return
		}
		s.logger.Error("get delivery", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not assign truck"})
		return
	}
	if delivery.Status != "pending" {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "delivery is " + delivery.Status + " and cannot be assigned",
		})
		return
	}

	truck, err := qtx.GetTruckForUpdate(r.Context(), truckID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "truck not found"})
			return
		}
		s.logger.Error("get truck", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not assign truck"})
		return
	}
	if truck.Status != "available" {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "truck " + truck.PlateNumber + " is " + truck.Status,
		})
		return
	}

	updated, err := qtx.AssignTruck(r.Context(), db.AssignTruckParams{
		ID:      deliveryID,
		TruckID: &truckID,
	})
	if err != nil {
		s.logger.Error("assign truck", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not assign truck"})
		return
	}

	if err := qtx.SetTruckStatus(r.Context(), db.SetTruckStatusParams{
		ID:     truckID,
		Status: "on_delivery",
	}); err != nil {
		s.logger.Error("set truck status", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not assign truck"})
		return
	}

	note := "assigned to " + truck.PlateNumber
	if err := qtx.CreateDeliveryEvent(r.Context(), db.CreateDeliveryEventParams{
		DeliveryID: deliveryID,
		EventType:  "assigned",
		Note:       &note,
	}); err != nil {
		s.logger.Error("create event", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not assign truck"})
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
	delivery, err := qtx.GetDelivery(r.Context(), deliveryID)
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

	delivery, err := qtx.GetDelivery(r.Context(), deliveryID)
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

	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.logger.Error("begin tx", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not update delivery and truck status"})
		return
	}
	defer tx.Rollback(r.Context())
	qtx := s.queries.WithTx(tx)

	delivery, err := qtx.GetDelivery(r.Context(), deliveryID)
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
		Status: "available",
	}); err != nil {
		s.logger.Error("set truck status", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not update truck status"})
		return
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

	delivery, err := qtx.GetDelivery(r.Context(), deliveryID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "delivery not found"})
			return
		}
		s.logger.Error("get delivery", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not cancel delivery"})
		return
	}
	if delivery.Status != "pending" && delivery.Status != "assigned" {
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
	if delivery.TruckID != nil {
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

	if msg := validateDeliveryInput(body); msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}

	token, err := newTrackingToken()
	if err != nil {
		s.logger.Error("generate token", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not create delivery"})
		return
	}

	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.logger.Error("begin tx", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not create delivery"})
		return
	}
	defer tx.Rollback(r.Context())

	qtx := s.queries.WithTx(tx)

	delivery, err := qtx.CreateDelivery(r.Context(), db.CreateDeliveryParams{
		TrackingToken: token,
		CustomerName:  body.CustomerName,
		CustomerPhone: body.CustomerPhone,
		GoodsDesc:     body.GoodsDesc,
	})
	if err != nil {
		s.logger.Error("create delivery", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not create delivery"})
		return
	}

	stops := make([]db.CreateDeliveryStopRow, 0, len(body.Stops))
	for i, in := range body.Stops {
		stop, err := qtx.CreateDeliveryStop(r.Context(), db.CreateDeliveryStopParams{
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
			s.logger.Error("create stop", "err", err, "sequence", i+1)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not create delivery"})
			return
		}
		stops = append(stops, stop)
	}

	if err := qtx.CreateDeliveryEvent(r.Context(), db.CreateDeliveryEventParams{
		DeliveryID: delivery.ID,
		EventType:  "created",
	}); err != nil {
		s.logger.Error("create event", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not create delivery"})
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
func validateDeliveryInput(body createDeliveryInput) string {
	if body.CustomerName == "" {
		return "customer_name is required"
	}
	if len(body.Stops) < 2 {
		return "at least two stops are required"
	}
	if body.Stops[0].StopType != "pickup" {
		return "the first stop must be a pickup"
	}

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
		if *st.Lat < minLat || *st.Lat > maxLat || *st.Lng < minLng || *st.Lng > maxLng {
			return "coordinates must be within Nigeria — check that lat and lng aren't swapped"
		}
		if i > 0 && st.StopType == "pickup" {
			return "only the first stop can be a pickup"
		}
	}
	return ""
}
