package api

import (
	"encoding/json"
	"net/http"

	"github.com/WllowyEmmie/trucking-logistics/internal/db"
)

func (s *Server) listTrucks(w http.ResponseWriter, r *http.Request) {
	trucks, err := s.queries.ListTrucks(r.Context())
	if err != nil {
		s.logger.Error("list trucks", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not list trucks"})
		return
	}
	writeJSON(w, http.StatusOK, trucks)
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
		s.logger.Error("create truck", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not create truck"})
		return
	}
	writeJSON(w, http.StatusCreated, truck)
}
