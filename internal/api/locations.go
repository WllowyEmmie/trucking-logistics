package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/WllowyEmmie/trucking-logistics/internal/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *Server) ingestLocations(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	var body struct {
		TruckID string `json:"truck_id"`
		Pings   []struct {
			Lat        *float64   `json:"lat"`
			Lng        *float64   `json:"lng"`
			RecordedAt *time.Time `json:"recorded_at"`
			SpeedKph   *float32   `json:"speed_kph"`
			AccuracyM  *float32   `json:"accuracy_m"`
			Heading    *float32   `json:"heading"`
		} `json:"pings"`
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
	if len(body.Pings) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "at least one ping is required"})
		return
	}
	if len(body.Pings) > 500 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "too many pings"})
		return
	}
	for _, p := range body.Pings {
		if p.Lat == nil || p.Lng == nil || p.RecordedAt == nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "every ping needs lat, lng and recorded_at"})
			return
		}

		if p.RecordedAt.After(now.Add(2 * time.Minute)) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "recorded_at cannot be in the future"})
			return
		}
		if *p.Lat < s.cfg.MinLat || *p.Lat > s.cfg.MaxLat ||
			*p.Lng < s.cfg.MinLng || *p.Lng > s.cfg.MaxLng {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "coordinates must be within the service area"})
			return
		}
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.logger.Error("begin tx", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not record locations"})
		return
	}
	defer tx.Rollback(r.Context())
	qtx := s.queries.WithTx(tx)

	if _, err := qtx.GetTruckForUpdate(r.Context(), truckID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "truck not found"})
			return
		}
		s.logger.Error("get truck", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not get truck"})
		return
	}
	var latest *time.Time
	var latestLat, latestLng float64
	for _, p := range body.Pings {
		if err := qtx.InsertLocation(r.Context(), db.InsertLocationParams{
			TruckID:    truckID,
			Lng:        *p.Lng,
			Lat:        *p.Lat,
			AccuracyM:  p.AccuracyM,
			SpeedKph:   p.SpeedKph,
			Heading:    p.Heading,
			RecordedAt: *p.RecordedAt,
		}); err != nil {
			s.logger.Error("insert location", "err", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not insert location"})
			return

		}
		if latest == nil || p.RecordedAt.After(*latest) {
			latest = p.RecordedAt
			latestLat = *p.Lat
			latestLng = *p.Lng
		}

	}
	if err := qtx.UpdateTruckPosition(r.Context(), db.UpdateTruckPositionParams{
		ID:         truckID,
		Lng:        latestLng,
		Lat:        latestLat,
		RecordedAt: latest,
	}); err != nil {
		s.logger.Error("update truck position", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not update truck position"})
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		s.logger.Error("commit tx", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not update truck position"})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"accepted": len(body.Pings),
	})
}
