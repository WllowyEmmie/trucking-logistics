package api

import (
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/WllowyEmmie/trucking-logistics/internal/db"
	"github.com/WllowyEmmie/trucking-logistics/internal/eta"
)

func (s *Server) etaParams() eta.Params {
	st := s.st()
	return eta.Params{SpeedKph: float64(st.EtaSpeedKph), StopMinutes: float64(st.StopMinutes)}
}

// estimate is the ETA for a delivery on the road, or nil: only assigned and
// in-transit deliveries with a truck position and stops left have one.
func (s *Server) estimate(status string, truckLat, truckLng *float64, stops []db.StopRow) *eta.Result {
	if status != "assigned" && status != "in_transit" || truckLat == nil || truckLng == nil {
		return nil
	}
	left := make([]eta.Stop, 0, len(stops))
	for _, st := range stops {
		if st.Status != "pending" && st.Status != "arrived" || st.Lat == nil || st.Lng == nil {
			continue
		}
		left = append(left, eta.Stop{ID: st.ID.String(), Pos: eta.Point{Lat: *st.Lat, Lng: *st.Lng}, Status: st.Status, ArrivedAt: st.ArrivedAt})
	}
	return eta.Estimate(time.Now(), &eta.Point{Lat: *truckLat, Lng: *truckLng}, left, s.etaParams())
}

// deliveryItem is a GET /deliveries row with its estimated arrival.
type deliveryItem struct {
	db.DeliveryItem
	ETA *time.Time `json:"eta"`
}

func (s *Server) withETAs(r *http.Request, items []db.DeliveryItem) ([]deliveryItem, error) {
	var active []uuid.UUID
	for _, d := range items {
		if d.Status == "assigned" || d.Status == "in_transit" {
			active = append(active, d.ID)
		}
	}
	routes, err := s.queries.RoutesLeft(r.Context(), active)
	if err != nil {
		return nil, err
	}
	out := make([]deliveryItem, len(items))
	for i, d := range items {
		out[i] = deliveryItem{DeliveryItem: d}
		if rt := routes[d.ID]; rt != nil {
			if e := s.estimate(d.Status, rt.TruckLat, rt.TruckLng, rt.Stops); e != nil {
				out[i].ETA = &e.Arrival
			}
		}
	}
	return out, nil
}
