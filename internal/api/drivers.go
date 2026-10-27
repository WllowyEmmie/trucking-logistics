package api

import (
	"time"
	"encoding/json"
	"net/http"

	"github.com/WllowyEmmie/trucking-logistics/internal/auth"
	"github.com/WllowyEmmie/trucking-logistics/internal/db"
)

func (s *Server) listDriver(w http.ResponseWriter, r *http.Request) {
	drivers, err := s.queries.ListDrivers(r.Context())
	if err != nil {
		s.logger.Error("list drivers", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not list drivers"})
		return
	}
	drivers = onlyMine(r, drivers, func(p *principal, d db.ListDriversRow) bool { return d.ID == p.ID })
	withPIN, err := s.queries.DriversWithPIN(r.Context())
	if err != nil {
		s.respondErr(w, err, "drivers with pin", "could not list drivers")
		return
	}
	shifts, err := s.queries.OpenShifts(r.Context())
	if err != nil {
		s.respondErr(w, err, "open shifts", "could not list drivers")
		return
	}
	type row struct {
		db.ListDriversRow
		// Whether the driver can sign in to the app yet. Never the PIN itself.
		HasPIN bool `json:"has_pin"`
		// On shift now, and since when.
		OnShift    bool       `json:"on_shift"`
		ShiftSince *time.Time `json:"shift_since"`
	}
	out := make([]row, 0, len(drivers))
	for _, d := range drivers {
		rw := row{ListDriversRow: d, HasPIN: withPIN[d.ID]}
		if at, ok := shifts[d.ID]; ok {
			rw.OnShift, rw.ShiftSince = true, &at
		}
		out = append(out, rw)
	}
	writeJSON(w, http.StatusOK, out)
}
func (s *Server) createDriver(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name  string `json:"name"`
		Phone string `json:"phone"`
		// Optional: lets the driver sign in to the app straight away. Either
		// a PIN of the dispatcher's choosing, or generate_pin for a random one
		// returned once in the response.
		PIN         string `json:"pin"`
		GeneratePIN bool   `json:"generate_pin"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if body.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}
	// Stored as digits with a leading +, the form sign-in matches on.
	body.Phone = auth.NormPhone(body.Phone)
	if body.Phone == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "phone number is required"})
		return
	}
	if body.PIN != "" {
		if err := auth.ValidPIN(body.PIN); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}
	// The unique index is on the phone as stored; older rows may have
	// spaces in them, so check the normalised form too.
	if _, err := s.queries.GetDriverByPhone(r.Context(), body.Phone); err == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "a driver with that phone number already exists"})
		return
	}

	driver, err := s.queries.CreateDriver(r.Context(), db.CreateDriverParams{
		Name:  body.Name,
		Phone: body.Phone,
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "a driver with that phone number already exists"})
			return
		}

		s.logger.Error("create driver", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not create driver"})
		return
	}
	if body.GeneratePIN {
		pin, ok := s.newPINFor(w, r, driver.ID)
		if !ok {
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusCreated, struct {
			db.Driver
			PIN string `json:"pin"`
		}{driver, pin})
		return
	}
	// No PIN given: the default, which they must change at first sign-in (pin.go).
	pin, mustChange := body.PIN, false
	if pin == "" {
		pin, mustChange = defaultDriverPIN, true
	}
	hash, err := auth.HashSecret(pin)
	if err == nil {
		if mustChange {
			_, err = s.queries.SetDefaultDriverPIN(r.Context(), driver.ID, hash)
		} else {
			_, err = s.queries.SetDriverPIN(r.Context(), driver.ID, hash)
		}
	}
	if err != nil {
		s.respondErr(w, err, "set pin", "driver added, but the PIN wasn't set")
		return
	}
	writeJSON(w, http.StatusCreated, struct {
		db.Driver
		// Signs in with 1234 the first time, then chooses their own.
		DefaultPIN bool `json:"default_pin"`
	}{driver, mustChange})
}
