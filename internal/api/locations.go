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
	// A driver reports for their own truck only: no placing someone else's
	// truck on the map. 404, not 403, so ids can't be probed.
	if !principalFrom(r.Context()).ownsTruck(truckID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "truck not found"})
		return
	}
	if len(body.Pings) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "at least one ping is required"})
		return
	}
	// A driver's position is recorded only on shift, whatever the phone
	// sends. (A dispatcher, or the simulator, isn't a driver.)
	if p := principalFrom(r.Context()); p != nil && p.Role == roleDriver {
		since, err := s.queries.OpenShift(r.Context(), p.ID)
		if err != nil {
			s.respondErr(w, err, "open shift", "could not record locations")
			return
		}
		if since == nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "you're off shift: positions aren't recorded"})
			return
		}
	}
	if len(body.Pings) > 500 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "too many pings"})
		return
	}
	area := s.st()
	for _, p := range body.Pings {
		if p.Lat == nil || p.Lng == nil || p.RecordedAt == nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "every ping needs lat, lng and recorded_at"})
			return
		}

		if p.RecordedAt.After(now.Add(2 * time.Minute)) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "recorded_at cannot be in the future"})
			return
		}
		if !area.Contains(*p.Lat, *p.Lng) {
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
	// 0 rows means the batch's newest ping is older than the stored position
	// (a phone flushing an offline queue). It's kept in locations, but the
	// truck didn't move, so there's nothing to tell clients.
	moved, err := qtx.UpdateTruckPosition(r.Context(), db.UpdateTruckPositionParams{
		ID:         truckID,
		Lng:        latestLng,
		Lat:        latestLat,
		RecordedAt: latest,
	})
	if err != nil {
		s.logger.Error("update truck position", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not update truck position"})
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		s.logger.Error("commit tx", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not update truck position"})
		return
	}
	if moved > 0 {
		s.hub.broadcast(wsMessage{
			Type: "position",
			Truck: &wsTruck{
				ID:         truckID.String(),
				Lng:        &latestLng,
				Lat:        &latestLat,
				LastSeenAt: latest,
			},
		})
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"accepted": len(body.Pings),
	})
}
