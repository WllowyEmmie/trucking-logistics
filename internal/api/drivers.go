package api

import (
	"encoding/json"
	"net/http"

	"github.com/WllowyEmmie/trucking-logistics/internal/db"
)

func (s *Server) listDriver(w http.ResponseWriter, r *http.Request) {
	drivers, err := s.queries.ListDrivers(r.Context())
	if err != nil {
		s.logger.Error("list drivers", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not list drivers"})
		return
	}
	writeJSON(w, http.StatusOK, drivers)
}
func (s *Server) createDriver(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name  string `json:"name"`
		Phone string `json:"phone"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if body.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}
	if body.Phone == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "phone number is required"})
		return
	}

	driver, err := s.queries.CreateDriver(r.Context(), db.CreateDriverParams{
		Name:  body.Name,
		Phone: body.Phone,
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "a driver with that phone number already exists"})
			return
		}

		s.logger.Error("create driver", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not create driver"})
		return
	}
	writeJSON(w, http.StatusCreated, driver)
}
