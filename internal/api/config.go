// internal/api/config.go
package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/WllowyEmmie/trucking-logistics/internal/config"
)

// st is the settings in effect now. Read it once per request and use that
// copy: a save swaps the pointer, it never changes a Settings in place.
func (s *Server) st() *config.Settings { return s.settings.Load() }

// loadSettings: the saved row over the environment's defaults. Fields the
// saved JSON doesn't have (added since it was saved) keep their defaults.
func (s *Server) loadSettings(ctx context.Context) {
	def := s.cfg.DefaultSettings()
	saved, err := s.queries.GetSettings(ctx)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		s.logger.Error("load settings; using the environment's", "err", err)
	default:
		merged := def
		if err := json.Unmarshal(saved.Data, &merged); err != nil || merged.Validate() != nil {
			s.logger.Error("saved settings are unreadable; using the environment's", "err", err)
		} else {
			def = merged
			s.settingsAt.Store(&saved.UpdatedAt)
			by := saved.UpdatedBy
			s.settingsBy.Store(&by)
		}
	}
	s.settings.Store(&def)
	if def.Global() {
		s.logger.Warn("no service area set: addresses anywhere are accepted",
			"hint", "set it on the console's Settings page (or SERVICE_MIN_LAT/MAX_LAT/MIN_LNG/MAX_LNG for the default)")
	}
}

type configResponse struct {
	config.Settings
	UpdatedAt *time.Time `json:"updated_at"`
	// Who saved them last: dispatchers only.
	UpdatedBy *string `json:"updated_by,omitempty"`
}

func (s *Server) configResponse(r *http.Request) configResponse {
	out := configResponse{Settings: *s.st(), UpdatedAt: s.settingsAt.Load()}
	if p := principalFrom(r.Context()); p != nil && p.Role == roleDispatcher {
		out.UpdatedBy = s.settingsBy.Load()
	}
	return out
}

// getConfig is public: the tracking page needs the company name, support
// phone and units, and the sign-in screens the country.
func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.configResponse(r))
}

// putConfig is PUT /config (dispatchers): the whole settings object. It's
// checked, saved and in effect for the next request: no restart.
func (s *Server) putConfig(w http.ResponseWriter, r *http.Request) {
	next := *s.st()
	next.Depot = nil // a body without a depot means no depot
	if err := json.NewDecoder(r.Body).Decode(&next); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if err := next.Validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	data, err := json.Marshal(next)
	if err != nil {
		s.respondErr(w, err, "encode settings", "could not save settings")
		return
	}
	by := actorName(r)
	at, err := s.queries.SaveSettings(r.Context(), data, by)
	if err != nil {
		s.respondErr(w, err, "save settings", "could not save settings")
		return
	}
	s.settings.Store(&next)
	s.settingsAt.Store(&at)
	s.settingsBy.Store(&by)
	s.logger.Info("settings saved", "by", by)
	writeJSON(w, http.StatusOK, s.configResponse(r))
}

// checkArea is POST /config/check {min_lat, max_lat, min_lng, max_lng}: what
// a new service area would leave outside, so the page can warn first.
func (s *Server) checkArea(w http.ResponseWriter, r *http.Request) {
	var b struct {
		MinLat float64 `json:"min_lat"`
		MaxLat float64 `json:"max_lat"`
		MinLng float64 `json:"min_lng"`
		MaxLng float64 `json:"max_lng"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	stops, trucks, err := s.queries.OutsideArea(r.Context(), b.MinLat, b.MaxLat, b.MinLng, b.MaxLng)
	if err != nil {
		s.respondErr(w, err, "check area", "could not check the service area")
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"stops_outside": stops, "trucks_outside": trucks})
}
