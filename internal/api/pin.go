package api

import (
	"encoding/json"
	"net/http"

	"github.com/WllowyEmmie/trucking-logistics/internal/auth"
)

// The default PIN. A driver added without one gets it, marked must-change:
// the first time they sign in, the app asks for their own PIN, and until
// they've chosen one the API refuses everything else (pinGate).
const defaultDriverPIN = "1234"

const codePINChange = "pin_change_required"

// What a driver on the default PIN may still call.
var pinGateOpen = map[string]bool{
	"/auth/me":     true,
	"/auth/me/pin": true,
	"/auth/logout": true,
	"/config":      true,
	"/health":      true,
}

func pinGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := principalFrom(r.Context())
		if p != nil && p.Role == roleDriver && p.MustChangePIN && !pinGateOpen[r.URL.Path] {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "choose your own PIN first", "code": codePINChange})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// changeMyPIN is POST /auth/me/pin {"pin"} (drivers): choose a new PIN. From
// the default PIN it also starts the shift that signing in would have.
func (s *Server) changeMyPIN(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	if p == nil || p.Role != roleDriver {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "only a driver has a PIN"})
		return
	}
	var body struct {
		PIN string `json:"pin"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `send {"pin": "…"}`})
		return
	}
	if err := auth.ValidPIN(body.PIN); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if body.PIN == defaultDriverPIN {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "choose a PIN other than " + defaultDriverPIN})
		return
	}
	hash, err := auth.HashSecret(body.PIN)
	if err != nil {
		s.respondErr(w, err, "hash pin", "could not change the PIN")
		return
	}
	if err := s.queries.ChangeOwnPIN(r.Context(), p.ID, hash); err != nil {
		s.respondErr(w, err, "change pin", "could not change the PIN")
		return
	}
	was := p.MustChangePIN
	p.MustChangePIN = false
	m := meOf(p)
	since, err := s.queries.OpenShift(r.Context(), p.ID)
	if err != nil {
		s.respondErr(w, err, "open shift", "the PIN changed, but your shift couldn't be loaded")
		return
	}
	// Coming off the default PIN: start the shift signing in would have.
	if was && since == nil {
		at, err := s.queries.StartShift(r.Context(), p.ID)
		if err != nil {
			s.respondErr(w, err, "start shift", "the PIN changed, but your shift didn't start; start it in the app")
			return
		}
		since = &at
	}
	on := since != nil
	m.OnShift, m.ShiftSince = &on, since
	writeJSON(w, http.StatusOK, m)
}
