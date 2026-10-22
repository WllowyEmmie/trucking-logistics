package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/WllowyEmmie/trucking-logistics/internal/db"
)

const (
	maxPhotoBytes     = 3 << 20
	maxSignatureBytes = 300 << 10
	// The request carries both images as base64 (4/3 larger) plus a little JSON.
	proofBodyLimit = 5 << 20
)

// saveProof is POST /deliveries/{id}/stops/{stopId}/proof (the driver, or a
// dispatcher for them):
//
//	{"receiver_name": "Ada", "photo": "data:image/jpeg;base64,…" | null,
//	 "signature": "data:image/png;base64,…" | null,
//	 "lat": 6.45, "lng": 3.39, "captured_at": "2026-10-20T13:05:00Z"}
//
// At a drop-off still to complete. A second send replaces the first (a retake).
func (s *Server) saveProof(w http.ResponseWriter, r *http.Request) {
	deliveryID, err1 := uuid.Parse(chi.URLParam(r, "id"))
	stopID, err2 := uuid.Parse(chi.URLParam(r, "stopId"))
	if err1 != nil || err2 != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid delivery or stop id"})
		return
	}
	var body struct {
		ReceiverName string     `json:"receiver_name"`
		Photo        *string    `json:"photo"`
		Signature    *string    `json:"signature"`
		Lat          *float64   `json:"lat"`
		Lng          *float64   `json:"lng"`
		CapturedAt   *time.Time `json:"captured_at"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json, or the photo is too large"})
		return
	}
	bad := func(msg string) { writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg}) }
	name := strings.TrimSpace(body.ReceiverName)
	if name == "" || len(name) > 100 {
		bad("enter who received it (up to 100 characters)")
		return
	}
	photo, photoType, err := decodeImage(body.Photo, maxPhotoBytes)
	if err != nil {
		bad("photo: " + err.Error())
		return
	}
	sig, sigType, err := decodeImage(body.Signature, maxSignatureBytes)
	if err != nil {
		bad("signature: " + err.Error())
		return
	}
	if (body.Lat == nil) != (body.Lng == nil) {
		bad("send both lat and lng, or neither")
		return
	}
	captured := time.Now()
	if body.CapturedAt != nil {
		if body.CapturedAt.After(captured.Add(2 * time.Minute)) {
			bad("captured_at cannot be in the future")
			return
		}
		captured = *body.CapturedAt
	}

	tx, err := s.beginTx(r.Context())
	if err != nil {
		s.respondErr(w, err, "begin tx", "could not save the proof")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := s.queries.WithTx(tx)

	d, err := qtx.GetDeliveryForUpdate(r.Context(), deliveryID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "delivery not found"})
		return
	}
	if err != nil {
		s.respondErr(w, err, "get delivery", "could not save the proof")
		return
	}
	if d.Status != "in_transit" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "delivery is " + d.Status + "; proof is taken while it's in transit"})
		return
	}
	stop, err := qtx.GetStopForUpdate(r.Context(), db.GetStopForUpdateParams{ID: stopID, DeliveryID: deliveryID})
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "stop not found on this delivery"})
		return
	}
	if err != nil {
		s.respondErr(w, err, "get stop", "could not save the proof")
		return
	}
	if stop.StopType != "dropoff" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "proof of delivery is for drop-offs"})
		return
	}
	if stop.Status == "completed" || stop.Status == "skipped" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "stop is already " + stop.Status})
		return
	}
	if err := qtx.SaveProof(r.Context(), db.SaveProofParams{
		StopID: stopID, DeliveryID: deliveryID, ReceiverName: name,
		Photo: photo, PhotoType: photoType, Signature: sig, SignatureType: sigType,
		Lat: body.Lat, Lng: body.Lng, CapturedAt: captured, SavedBy: actorName(r),
	}); err != nil {
		s.respondErr(w, err, "save proof", "could not save the proof")
		return
	}
	note := "received by " + name
	if err := qtx.CreateDeliveryEvent(r.Context(), db.CreateDeliveryEventParams{DeliveryID: deliveryID, StopID: &stopID, EventType: "proof", Note: &note}); err != nil {
		s.respondErr(w, err, "create event", "could not save the proof")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		s.respondErr(w, err, "commit", "could not save the proof")
		return
	}
	proof, err := s.queries.GetProof(r.Context(), stopID)
	if err != nil {
		s.respondErr(w, err, "get proof", "could not save the proof")
		return
	}
	writeJSON(w, http.StatusOK, proof)
}

// proofMissing says what a driver still owes before completing a drop-off,
// or "" when nothing.
func (s *Server) proofMissing(p *db.ProofSummary) string {
	st := s.st()
	var need []string
	if p == nil {
		need = append(need, "who received it")
	}
	if st.PodPhotoRequired && (p == nil || !p.HasPhoto) {
		need = append(need, "a photo")
	}
	if st.PodSignatureRequired && (p == nil || !p.HasSignature) {
		need = append(need, "a signature")
	}
	if len(need) == 0 {
		return ""
	}
	return "proof of delivery needed: " + strings.Join(need, ", ")
}

// proofImage is GET /deliveries/{id}/stops/{stopId}/proof/{kind} (photo or
// signature), for the console and the driver.
func (s *Server) proofImage(w http.ResponseWriter, r *http.Request) {
	deliveryID, err1 := uuid.Parse(chi.URLParam(r, "id"))
	stopID, err2 := uuid.Parse(chi.URLParam(r, "stopId"))
	if err1 != nil || err2 != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid delivery or stop id"})
		return
	}
	s.writeImage(w, r, deliveryID, stopID, chi.URLParam(r, "kind"))
}

// trackProofImage is GET /track/{token}/stops/{seq}/{kind}: the customer's
// link shows its own proofs.
func (s *Server) trackProofImage(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	seq, err := strconv.Atoi(chi.URLParam(r, "seq"))
	if len(token) != 16 || err != nil || seq < 1 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	deliveryID, err := s.queries.DeliveryIDByToken(r.Context(), token)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	stopID, err := s.queries.StopIDBySequence(r.Context(), deliveryID, int32(seq))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	s.writeImage(w, r, deliveryID, stopID, chi.URLParam(r, "kind"))
}

func (s *Server) writeImage(w http.ResponseWriter, r *http.Request, deliveryID, stopID uuid.UUID, kind string) {
	if kind != "photo" && kind != "signature" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	data, typ, err := s.queries.ProofImage(r.Context(), deliveryID, stopID, kind)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no " + kind + " for this stop"})
		return
	}
	if err != nil {
		s.respondErr(w, err, "proof image", "could not load the image")
		return
	}
	w.Header().Set("Content-Type", typ)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	// Private: it's behind a sign-in or a secret link. A retake changes it,
	// so browsers check back rather than keep it.
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// decodeImage takes a data URL (data:image/jpeg;base64,…). Nil or empty
// means no image. Only JPEG, PNG and WebP, checked by their first bytes,
// not just the label.
func decodeImage(dataURL *string, max int) ([]byte, string, error) {
	if dataURL == nil || *dataURL == "" {
		return nil, "", nil
	}
	head, b64, ok := strings.Cut(*dataURL, ",")
	if !ok || !strings.HasPrefix(head, "data:") || !strings.HasSuffix(head, ";base64") {
		return nil, "", errors.New("send it as a data URL (data:image/jpeg;base64,…)")
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, "", errors.New("the image data isn't valid base64")
	}
	if len(raw) > max {
		return nil, "", fmt.Errorf("the image is too large (over %d KB)", max>>10)
	}
	switch {
	case bytes.HasPrefix(raw, []byte{0xFF, 0xD8, 0xFF}):
		return raw, "image/jpeg", nil
	case bytes.HasPrefix(raw, []byte("\x89PNG\r\n\x1a\n")):
		return raw, "image/png", nil
	case len(raw) > 12 && string(raw[0:4]) == "RIFF" && string(raw[8:12]) == "WEBP":
		return raw, "image/webp", nil
	}
	return nil, "", errors.New("only JPEG, PNG or WebP images")
}
