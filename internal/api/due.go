package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/WllowyEmmie/trucking-logistics/internal/db"
)

// checkDue: a due time a few minutes in the past is a slow form, not a
// mistake; an hour ago is a mistake.
func checkDue(due *time.Time) string {
	if due == nil {
		return ""
	}
	if due.Before(time.Now().Add(-15 * time.Minute)) {
		return "the due time has already passed"
	}
	if due.After(time.Now().AddDate(1, 0, 0)) {
		return "the due time is more than a year away"
	}
	return ""
}

// setDue is POST /deliveries/{id}/due {"due_by": time or null} (dispatchers):
// promise a time, change it, or clear it. Not on a finished delivery.
func (s *Server) setDue(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid delivery id"})
		return
	}
	var body struct {
		DueBy *time.Time `json:"due_by"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json: due_by is a time like 2026-10-20T14:30:00Z, or null"})
		return
	}
	if msg := checkDue(body.DueBy); msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}

	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.respondErr(w, err, "begin tx", "could not set the due time")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := s.queries.WithTx(tx)

	status, err := qtx.SetDueBy(r.Context(), id, body.DueBy)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "delivery not found"})
		return
	}
	if err != nil {
		s.respondErr(w, err, "set due", "could not set the due time")
		return
	}
	if status == "completed" || status == "cancelled" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "delivery is " + status + "; its due time can't change"})
		return
	}
	// The note is the time itself (or empty): clients format it in the
	// operator's zone.
	note := ""
	if body.DueBy != nil {
		note = body.DueBy.UTC().Format(time.RFC3339)
	}
	if err := qtx.CreateDeliveryEvent(r.Context(), db.CreateDeliveryEventParams{DeliveryID: id, EventType: "due_changed", Note: &note}); err != nil {
		s.respondErr(w, err, "create event", "could not set the due time")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		s.respondErr(w, err, "commit", "could not set the due time")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "due_by": body.DueBy})
}
