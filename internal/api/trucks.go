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

var validTruckStatuses = map[string]bool{
	"available":   true,
	"on_delivery": true,
	"maintenance": true,
	"offline":     true,
}

func (s *Server) listTrucks(w http.ResponseWriter, r *http.Request) {
	trucks, err := s.queries.ListTrucks(r.Context())
	if err != nil {
		s.logger.Error("list trucks", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not list trucks"})
		return
	}
	writeJSON(w, http.StatusOK, onlyMine(r, trucks, func(p *principal, t db.TruckWithPosition) bool { return p.ownsTruck(t.ID) }))
}

func (s *Server) createTruck(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PlateNumber string  `json:"plate_number"`
		Model       *string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if body.PlateNumber == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "plate_number is required"})
		return
	}

	truck, err := s.queries.CreateTruck(r.Context(), db.CreateTruckParams{
		PlateNumber: body.PlateNumber,
		Model:       body.Model,
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "a truck with that plate number already exists"})
			return
		}

		s.logger.Error("create truck", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not create truck"})
		return
	}
	writeJSON(w, http.StatusCreated, truck)
}

func (s *Server) updateTruck(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid truck id"})
		return
	}

	// Fields left out are kept. "driver_id": "" unassigns the driver; a
	// uuid assigns one.
	var body struct {
		DriverID *string `json:"driver_id"`
		Status   *string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}

	var driverID *uuid.UUID
	clearDriver := body.DriverID != nil && *body.DriverID == ""
	if body.DriverID != nil && *body.DriverID != "" {
		parsed, err := uuid.Parse(*body.DriverID)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid driver id"})
			return
		}
		driverID = &parsed
	}

	if body.Status != nil && !validTruckStatuses[*body.Status] {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "status must be one of: available, on_delivery, maintenance, offline",
		})
		return
	}

	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.logger.Error("begin tx", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not update truck"})
		return
	}
	defer tx.Rollback(r.Context())
	qtx := s.queries.WithTx(tx)

	truck, err := qtx.GetTruckForUpdate(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "truck not found"})
		return
	}
	if err != nil {
		s.logger.Error("get truck", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not update truck"})
		return
	}

	// A truck on a delivery can't change status: the delivery would be
	// pointing at a truck that isn't working.
	if body.Status != nil && truck.Status == "on_delivery" && *body.Status != "on_delivery" {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "truck " + truck.PlateNumber + " is on a delivery; cancel or complete it first",
		})
		return
	}
	// Its delivery would be left with no one to drive it.
	if clearDriver && truck.Status == "on_delivery" {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "truck " + truck.PlateNumber + " is on a delivery; it can't lose its driver until the delivery is done or cancelled",
		})
		return
	}
	// on_delivery belongs to assign, which also links the delivery. Setting
	// it by hand would make a busy truck with nothing to do.
	if body.Status != nil && *body.Status == "on_delivery" && truck.Status != "on_delivery" {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "truck " + truck.PlateNumber + " goes on_delivery by assigning it a delivery",
		})
		return
	}

	status := truck.Status
	if body.Status != nil {
		status = *body.Status
	}

	updated, err := qtx.UpdateTruck(r.Context(), db.UpdateTruckParams{
		ID:          id,
		DriverID:    driverID,
		ClearDriver: clearDriver,
		Status:      status,
	})
	if err != nil {
		if isForeignKeyViolation(err) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "driver not found"})
			return
		}
		s.logger.Error("update truck", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not update truck"})
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		s.logger.Error("commit tx", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not update truck"})
		return
	}

	writeJSON(w, http.StatusOK, updated)
}
