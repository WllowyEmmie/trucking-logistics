package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Shifts: a driver's phone sends GPS only while they're on shift, and the
// API refuses a driver's positions otherwise (see ingestLocations).

type shiftResponse struct {
	OnShift bool       `json:"on_shift"`
	Since   *time.Time `json:"since"`
}

// setMyShift is POST /auth/me/shift {"on": bool} (drivers).
func (s *Server) setMyShift(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	if p == nil || p.Role != roleDriver {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "only a driver has a shift"})
		return
	}
	var body struct {
		On *bool `json:"on"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.On == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `send {"on": true} or {"on": false}`})
		return
	}
	s.changeShift(w, r, p.ID, *body.On, "driver")
}

// endDriverShift is POST /drivers/{id}/shift/end (dispatchers): for a
// driver who went home with the app still on shift.
func (s *Server) endDriverShift(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid driver id"})
		return
	}
	if _, err := s.queries.GetDriverLogin(r.Context(), id); errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "driver not found"})
		return
	} else if err != nil {
		s.respondErr(w, err, "get driver", "could not end the shift")
		return
	}
	s.changeShift(w, r, id, false, actorName(r))
}

func (s *Server) changeShift(w http.ResponseWriter, r *http.Request, driverID uuid.UUID, on bool, by string) {
	ctx := r.Context()
	if on {
		at, err := s.queries.StartShift(ctx, driverID)
		if err != nil {
			s.respondErr(w, err, "start shift", "could not start the shift")
			return
		}
		writeJSON(w, http.StatusOK, shiftResponse{OnShift: true, Since: &at})
		return
	}
	// Mid-delivery the customer and dispatch are watching the truck.
	busy, err := s.queries.DriverOnTheRoad(ctx, driverID)
	if err != nil {
		s.respondErr(w, err, "driver on the road", "could not end the shift")
		return
	}
	if busy {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "a delivery is on the road: finish it or report a problem before ending the shift"})
		return
	}
	if _, err := s.queries.EndShift(ctx, driverID, by); err != nil {
		s.respondErr(w, err, "end shift", "could not end the shift")
		return
	}
	writeJSON(w, http.StatusOK, shiftResponse{OnShift: false})
}
