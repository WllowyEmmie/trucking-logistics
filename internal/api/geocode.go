package api

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/WllowyEmmie/trucking-logistics/internal/geocode"
)

// geocodeAddress is GET /geocode?q=: places matching an address, inside the
// service area only, so whatever the console picks will pass createDelivery's
// coordinate check.
func (s *Server) geocodeAddress(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if n := utf8.RuneCountInString(q); n < 3 || n > 200 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "q must be 3 to 200 characters"})
		return
	}

	st := s.st()
	places, err := s.geocoder.Search(r.Context(), q, geocode.Bounds{
		MinLat: st.MinLat, MaxLat: st.MaxLat,
		MinLng: st.MinLng, MaxLng: st.MaxLng,
	}, st.Language)
	if err != nil {
		s.logger.Error("geocode", "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "address search is unavailable right now; enter the coordinates instead"})
		return
	}
	writeJSON(w, http.StatusOK, places)
}
