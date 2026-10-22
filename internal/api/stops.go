package api

import (
	"errors"
	"net/http"

	"github.com/WllowyEmmie/trucking-logistics/internal/db"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *Server) arriveAtStop(w http.ResponseWriter, r *http.Request) {
	s.advanceStop(w, r, "arrive")
}

func (s *Server) completeStop(w http.ResponseWriter, r *http.Request) {
	s.advanceStop(w, r, "complete")
}

func (s *Server) advanceStop(w http.ResponseWriter, r *http.Request, action string) {
	deliveryID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid delivery id"})
		return
	}
	stopID, err := uuid.Parse(chi.URLParam(r, "stopId"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid stop id"})
		return
	}

	tx, err := s.beginTx(r.Context())
	if err != nil {
		s.logger.Error("begin tx", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not update stop"})
		return
	}
	defer tx.Rollback(r.Context())
	qtx := s.queries.WithTx(tx)

	delivery, err := qtx.GetDeliveryForUpdate(r.Context(), deliveryID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "delivery not found"})
		return
	}
	if err != nil {
		s.logger.Error("get delivery", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not update stop"})
		return
	}
	if delivery.Status != "in_transit" {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "delivery is " + delivery.Status + "; stops can only change while it is in transit"})
		return
	}

	stop, err := qtx.GetStopForUpdate(r.Context(), db.GetStopForUpdateParams{
		ID: stopID, DeliveryID: deliveryID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "stop not found on this delivery"})
		return
	}
	if err != nil {
		s.logger.Error("get stop", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not update stop"})
		return
	}

	behind, err := qtx.CountIncompleteStopsBefore(r.Context(), db.CountIncompleteStopsBeforeParams{
		DeliveryID: deliveryID, Sequence: stop.Sequence,
	})
	if err != nil {
		s.logger.Error("count stops", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not update stop"})
		return
	}
	if behind > 0 {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "an earlier stop is still open; finish stops in order"})
		return
	}

	var updated db.MarkStopArrivedRow
	var eventType string

	switch action {
	case "arrive":
		if stop.Status != "pending" {
			writeJSON(w, http.StatusConflict, map[string]string{
				"error": "stop is already " + stop.Status})
			return
		}
		updated, err = qtx.MarkStopArrived(r.Context(), stopID)
		eventType = "stop_arrived"
	case "complete":
		if stop.Status != "arrived" {
			writeJSON(w, http.StatusConflict, map[string]string{
				"error": "stop is " + stop.Status + "; mark arrival first"})
			return
		}
		// A driver completes a drop-off with proof of delivery. A dispatcher
		// stepping in for a dead phone doesn't need it.
		if p := principalFrom(r.Context()); p != nil && p.Role == roleDriver && stop.StopType == "dropoff" {
			proof, perr := qtx.GetProof(r.Context(), stopID)
			if perr != nil {
				s.respondErr(w, perr, "get proof", "could not update stop")
				return
			}
			if msg := s.proofMissing(proof); msg != "" {
				writeJSON(w, http.StatusConflict, map[string]string{"error": msg})
				return
			}
		}
		var c db.MarkStopCompletedRow
		c, err = qtx.MarkStopCompleted(r.Context(), stopID)
		updated = db.MarkStopArrivedRow(c)
		eventType = "stop_completed"
	}
	if err != nil {
		s.logger.Error("update stop", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not update stop"})
		return
	}

	if err := qtx.CreateDeliveryEvent(r.Context(), db.CreateDeliveryEventParams{
		DeliveryID: deliveryID,
		StopID:     &stopID,
		EventType:  eventType,
		Note:       nil,
	}); err != nil {
		s.logger.Error("create event", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not update stop"})
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		s.logger.Error("commit", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not update stop"})
		return
	}

	writeJSON(w, http.StatusOK, updated)
}
