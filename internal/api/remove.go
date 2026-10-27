package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// Removing trucks and drivers (dispatchers). Refused while they're part of
// work in progress, so nothing is left half done:
//
//	DELETE /trucks/{id}   409 while it's on a delivery, or has an assigned,
//	                      in-transit or failed one (cancel or reassign it first)
//	DELETE /drivers/{id}  409 while they're on shift (end it first); their truck
//	                      is left without a driver and they're signed out
//
// Finished deliveries keep their history: the plate is in their events.

func (s *Server) removeTruck(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid truck id"})
		return
	}
	n, err := s.queries.TruckOpenWork(r.Context(), id)
	if err != nil {
		s.respondErr(w, err, "truck open work", "could not remove the truck")
		return
	}
	if n > 0 {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "the truck still has a delivery (assigned, in transit or failed): reassign or cancel it first"})
		return
	}
	ok, err := s.queries.DeleteTruck(r.Context(), id)
	if err != nil {
		s.respondErr(w, err, "delete truck", "could not remove the truck")
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "truck not found"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) removeDriver(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid driver id"})
		return
	}
	since, err := s.queries.OpenShift(r.Context(), id)
	if err != nil {
		s.respondErr(w, err, "open shift", "could not remove the driver")
		return
	}
	if since != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "the driver is on shift: end their shift first"})
		return
	}
	ok, err := s.queries.DeleteDriver(r.Context(), id)
	if err != nil {
		s.respondErr(w, err, "delete driver", "could not remove the driver")
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "driver not found"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
