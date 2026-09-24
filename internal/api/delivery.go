package api

import (
	"encoding/json"
	"net/http"

	"github.com/WllowyEmmie/trucking-logistics/internal/db"
)

const (
	minLat = 4.0
	maxLat = 14.0
	minLng = 2.5
	maxLng = 15.0
)

type stopInput struct {
	StopType     string   `json:"stop_type"`
	Address      string   `json:"address"`
	Lat          *float64 `json:"lat"`
	Lng          *float64 `json:"lng"`
	ContactName  *string  `json:"contact_name"`
	ContactPhone *string  `json:"contact_phone"`
}

type createDeliveryInput struct {
	CustomerName  string      `json:"customer_name"`
	CustomerPhone *string     `json:"customer_phone"`
	GoodsDesc     *string     `json:"goods_desc"`
	Stops         []stopInput `json:"stops"`
}

func (s *Server) createDelivery(w http.ResponseWriter, r *http.Request) {
	var body createDeliveryInput
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}

	if msg := validateDeliveryInput(body); msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}

	token, err := newTrackingToken()
	if err != nil {
		s.logger.Error("generate token", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not create delivery"})
		return
	}

	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.logger.Error("begin tx", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not create delivery"})
		return
	}
	defer tx.Rollback(r.Context())

	qtx := s.queries.WithTx(tx)

	delivery, err := qtx.CreateDelivery(r.Context(), db.CreateDeliveryParams{
		TrackingToken: token,
		CustomerName:  body.CustomerName,
		CustomerPhone: body.CustomerPhone,
		GoodsDesc:     body.GoodsDesc,
	})
	if err != nil {
		s.logger.Error("create delivery", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not create delivery"})
		return
	}

	stops := make([]db.CreateDeliveryStopRow, 0, len(body.Stops))
	for i, in := range body.Stops {
		stop, err := qtx.CreateDeliveryStop(r.Context(), db.CreateDeliveryStopParams{
			DeliveryID:   delivery.ID,
			Sequence:     int32(i + 1),
			StopType:     in.StopType,
			Address:      in.Address,
			Lng:          *in.Lng,
			Lat:          *in.Lat,
			ContactName:  in.ContactName,
			ContactPhone: in.ContactPhone,
		})
		if err != nil {
			s.logger.Error("create stop", "err", err, "sequence", i+1)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not create delivery"})
			return
		}
		stops = append(stops, stop)
	}

	if err := qtx.CreateDeliveryEvent(r.Context(), db.CreateDeliveryEventParams{
		DeliveryID: delivery.ID,
		EventType:  "created",
	}); err != nil {
		s.logger.Error("create event", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not create delivery"})
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		s.logger.Error("commit tx", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not create delivery"})
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"delivery": delivery,
		"stops":    stops,
	})
}
func validateDeliveryInput(body createDeliveryInput) string {
	if body.CustomerName == "" {
		return "customer_name is required"
	}
	if len(body.Stops) < 2 {
		return "at least two stops are required"
	}
	if body.Stops[0].StopType != "pickup" {
		return "the first stop must be a pickup"
	}

	for i, st := range body.Stops {
		if st.StopType != "pickup" && st.StopType != "dropoff" {
			return "stop_type must be pickup or dropoff"
		}
		if st.Address == "" {
			return "every stop needs an address"
		}
		if st.Lat == nil || st.Lng == nil {
			return "every stop needs lat and lng"
		}
		if *st.Lat < minLat || *st.Lat > maxLat || *st.Lng < minLng || *st.Lng > maxLng {
			return "coordinates must be within Nigeria — check that lat and lng aren't swapped"
		}
		if i > 0 && st.StopType == "pickup" {
			return "only the first stop can be a pickup"
		}
	}
	return ""
}
