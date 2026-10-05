// internal/api/config.go
package api

import "net/http"

type configResponse struct {
	Units       string  `json:"units"`
	Timezone    string  `json:"timezone"`
	Language    string  `json:"language"`
	CenterLat   float64 `json:"center_lat"`
	CenterLng   float64 `json:"center_lng"`
	DefaultZoom int     `json:"default_zoom"`
	MinLat      float64 `json:"min_lat"`
	MaxLat      float64 `json:"max_lat"`
	MinLng      float64 `json:"min_lng"`
	MaxLng      float64 `json:"max_lng"`
}

func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, configResponse{
		Units:       s.cfg.Units,
		Timezone:    s.cfg.Timezone,
		Language:    s.cfg.Language,
		CenterLat:   s.cfg.CenterLat,
		CenterLng:   s.cfg.CenterLng,
		DefaultZoom: s.cfg.DefaultZoom,
		MinLat:      s.cfg.MinLat,
		MaxLat:      s.cfg.MaxLat,
		MinLng:      s.cfg.MinLng,
		MaxLng:      s.cfg.MaxLng,
	})
}
