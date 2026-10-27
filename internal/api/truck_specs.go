package api

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/WllowyEmmie/trucking-logistics/internal/db"
)

// What each truck is, and its photo (dispatchers):
//
//	GET    /trucks/specs       every truck's specs and whether it has a photo
//	PUT    /trucks/{id}/specs  replace them: {body_type, capacity_kg, body_length_m, pallets, year, fuel}, each null to clear
//	PUT    /trucks/{id}/photo  {"photo": "data:image/jpeg;base64,…"} (JPEG, PNG or WebP, up to 3 MB)
//	GET    /trucks/{id}/photo  the image
//	DELETE /trucks/{id}/photo  back to the stock photo for its body type

const truckPhotoMax = 3 << 20

var (
	bodyTypes = map[string]bool{"box": true, "dropside": true, "flatbed": true, "tipper": true, "tanker": true, "refrigerated": true, "curtainsider": true, "tractor": true}
	fuels     = map[string]bool{"diesel": true, "petrol": true, "cng": true, "lng": true, "electric": true, "hybrid": true}
)

func (s *Server) truckParam(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid truck id"})
		return uuid.Nil, false
	}
	ok, err := s.queries.TruckExists(r.Context(), id)
	if err != nil {
		s.respondErr(w, err, "truck exists", "could not load the truck")
		return uuid.Nil, false
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "truck not found"})
		return uuid.Nil, false
	}
	return id, true
}

func (s *Server) listTruckSpecs(w http.ResponseWriter, r *http.Request) {
	specs, err := s.queries.ListTruckSpecs(r.Context())
	if err != nil {
		s.respondErr(w, err, "list truck specs", "could not load truck details")
		return
	}
	writeJSON(w, http.StatusOK, specs)
}

func (s *Server) putTruckSpecs(w http.ResponseWriter, r *http.Request) {
	id, ok := s.truckParam(w, r)
	if !ok {
		return
	}
	var body struct {
		BodyType    *string  `json:"body_type"`
		CapacityKg  *int32   `json:"capacity_kg"`
		BodyLengthM *float64 `json:"body_length_m"`
		Pallets     *int32   `json:"pallets"`
		Year        *int32   `json:"year"`
		Fuel        *string  `json:"fuel"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	bad := func(msg string) { writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg}) }
	switch {
	case body.BodyType != nil && !bodyTypes[*body.BodyType]:
		bad("body_type must be one of: box, dropside, flatbed, tipper, tanker, refrigerated, curtainsider, tractor")
	case body.CapacityKg != nil && (*body.CapacityKg < 100 || *body.CapacityKg > 60000):
		bad("capacity must be between 0.1 and 60 tonnes")
	case body.BodyLengthM != nil && (*body.BodyLengthM < 1 || *body.BodyLengthM > 20):
		bad("body length must be between 1 and 20 metres")
	case body.Pallets != nil && (*body.Pallets < 0 || *body.Pallets > 40):
		bad("pallets must be between 0 and 40")
	case body.Year != nil && (*body.Year < 1960 || *body.Year > int32(time.Now().Year()+1)):
		bad("that year doesn't look right")
	case body.Fuel != nil && !fuels[*body.Fuel]:
		bad("fuel must be one of: diesel, petrol, cng, lng, electric, hybrid")
	default:
		if body.BodyLengthM != nil {
			v := math.Round(*body.BodyLengthM*10) / 10
			body.BodyLengthM = &v
		}
		spec := db.TruckSpecs{TruckID: id, BodyType: body.BodyType, CapacityKg: body.CapacityKg, BodyLengthM: body.BodyLengthM, Pallets: body.Pallets, Year: body.Year, Fuel: body.Fuel}
		if err := s.queries.PutTruckSpecs(r.Context(), spec); err != nil {
			s.respondErr(w, err, "put truck specs", "could not save the truck's details")
			return
		}
		writeJSON(w, http.StatusOK, spec)
	}
}

func (s *Server) putTruckPhoto(w http.ResponseWriter, r *http.Request) {
	id, ok := s.truckParam(w, r)
	if !ok {
		return
	}
	var body struct {
		Photo *string `json:"photo"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json, or the photo is over 3 MB"})
		return
	}
	data, mime, err := decodeImage(body.Photo, truckPhotoMax)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if data == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "send a photo"})
		return
	}
	if err := s.queries.PutTruckPhoto(r.Context(), id, mime, data); err != nil {
		s.respondErr(w, err, "put truck photo", "could not save the photo")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"has_photo": true})
}

func (s *Server) getTruckPhoto(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid truck id"})
		return
	}
	mime, data, at, err := s.queries.TruckPhoto(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no photo"})
		return
	}
	if err != nil {
		s.respondErr(w, err, "truck photo", "could not load the photo")
		return
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Cache-Control", "private, max-age=300")
	w.Header().Set("Last-Modified", at.UTC().Format(http.TimeFormat))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
}

func (s *Server) deleteTruckPhoto(w http.ResponseWriter, r *http.Request) {
	id, ok := s.truckParam(w, r)
	if !ok {
		return
	}
	if err := s.queries.DeleteTruckPhoto(r.Context(), id); err != nil {
		s.respondErr(w, err, "delete truck photo", "could not remove the photo")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
