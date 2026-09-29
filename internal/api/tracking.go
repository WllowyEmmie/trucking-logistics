package api

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
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

	writeJSON(w, http.StatusOK, delivery)
}