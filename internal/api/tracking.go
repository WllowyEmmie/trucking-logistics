package api

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/WllowyEmmie/trucking-logistics/internal/db"
)

func (s *Server) trackDelivery(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	if len(token) != 16 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "delivery not found"})
		return
	}

	delivery, err := s.queries.GetDeliveryByToken(r.Context(), token)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "delivery not found"})
			return
		}
		s.logger.Error("track delivery", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not load delivery"})
		return
	}

	// The customer gets one time: arrival at the last stop.
	if delivery.TruckLat != nil {
		stops := make([]db.StopRow, 0, len(delivery.Stops))
		for _, st := range delivery.Stops {
			lat, lng := st.Lat, st.Lng
			stops = append(stops, db.StopRow{Status: st.Status, Lat: &lat, Lng: &lng, ArrivedAt: st.ArrivedAt})
		}
		if e := s.estimate(delivery.Status, delivery.TruckLat, delivery.TruckLng, stops); e != nil {
			delivery.ETA = &e.Arrival
		}
	}
	writeJSON(w, http.StatusOK, delivery)
}