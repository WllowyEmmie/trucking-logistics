package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"time"

	"github.com/WllowyEmmie/trucking-logistics/internal/client"
)

// A truck that would start further than this from its first stop (no
// position yet, or one from another city) starts near the pickup instead,
// so nobody waits an hour to see it arrive.
const maxApproachM = 30_000

// What the simulator does with a delivery, tick by tick:
//
//	assigned   → waits startAfter, then starts it (as the driver would)
//	in_transit → drives to the next stop along the road, arrives,
//	             waits dwell, completes the stop; after the last stop,
//	             completes the delivery (or fails it, with failRate)
//	finished   → drives back to the depot and parks there (drive),
//	             unless it's given another delivery on the way
type sim struct {
	c      *client.Client
	routes *router
	now    func() time.Time
	rng    *rand.Rand

	kph        float64 // speed reported with each ping
	metres     float64 // distance covered per tick
	startAfter time.Duration
	dwell      time.Duration
	failRate   float64
	plates     map[string]bool // empty: every truck
	// Put the drivers of the trucks it moves on shift, so auto mode
	// counts them as working (nobody is there to tap Start shift).
	shifts bool

	// Where trucks start when they have no usable position.
	home position
	// Where trucks go back to after a delivery; nil when Settings has no depot.
	depot *position
	// Service area: positions outside it are refused by the API.
	area *client.ServiceArea

	runs    map[string]*run // by delivery id
	at      map[string]position
	returns map[string]*homeward // by truck id: on the way back to the depot
}

// homeward is a truck driving back to the depot after a delivery.
type homeward struct {
	leg   *path
	plate string
}

// run is the simulator's memory of one delivery.
type run struct {
	seen       time.Time // first seen assigned
	stopID     string    // the stop `leg` goes to
	leg        *path
	dwellUntil time.Time
	fail       bool // decided once, when it starts
}

// work is one delivery to move this tick.
type work struct {
	d     client.DeliveryListItem
	plate string
}

// scan lists assigned and in-transit deliveries on trucks this run may move.
func (s *sim) scan(ctx context.Context) ([]work, error) {
	trucks, err := s.c.ListTrucks(ctx)
	if err != nil {
		return nil, err
	}
	plate := map[string]string{}
	for _, t := range trucks {
		plate[t.ID] = t.PlateNumber
		if s.shifts && t.DriverID != nil && !t.DriverOnShift && (len(s.plates) == 0 || s.plates[t.PlateNumber]) {
			if err := s.c.Post(ctx, "/drivers/"+*t.DriverID+"/shift/start", nil, nil); err != nil {
				log.Printf("%s: can't put the driver on shift, so auto mode won't use this truck: %v", t.PlateNumber, err)
				var ae *client.APIError
				if errors.As(err, &ae) && ae.Status == 403 {
					s.shifts = false // the API doesn't allow it; say so once
				}
			} else {
				fmt.Printf("%s: driver on shift\n", t.PlateNumber)
			}
		}
	}
	var out []work
	live := map[string]bool{}
	for _, status := range []string{"assigned", "in_transit"} {
		ds, err := s.c.ListDeliveries(ctx, status)
		if err != nil {
			return nil, err
		}
		for _, d := range ds {
			if d.TruckID == nil {
				continue
			}
			p := plate[*d.TruckID]
			if len(s.plates) > 0 && !s.plates[p] {
				continue
			}
			live[d.ID] = true
			out = append(out, work{d: d, plate: p})
			// A new delivery on the way back: it goes from where it is.
			delete(s.returns, *d.TruckID)
		}
	}
	// Forget deliveries that finished, were cancelled, or moved off our trucks.
	for id := range s.runs {
		if !live[id] {
			delete(s.runs, id)
		}
	}
	return out, nil
}

func (s *sim) step(ctx context.Context, w work) error {
	d := w.d
	detail, err := s.c.GetDelivery(ctx, d.ID)
	if err != nil {
		return err
	}
	if detail.Delivery.TruckID == nil {
		return nil
	}
	truckID := *detail.Delivery.TruckID
	r := s.runs[d.ID]
	if r == nil {
		r = &run{}
		s.runs[d.ID] = r
	}
	now := s.now()

	switch detail.Delivery.Status {
	case "assigned":
		if r.seen.IsZero() {
			r.seen = now
			// Show the truck on the map straight away, where it's starting from.
			pos := s.startFrom(truckID, detail)
			s.at[truckID] = pos
			fmt.Printf("%s on %s: starts in %s\n", d.Reference, w.plate, s.startAfter)
			return s.report(ctx, truckID, pos, 0)
		}
		if now.Sub(r.seen) < s.startAfter {
			return nil
		}
		if err := s.c.StartDelivery(ctx, d.ID); err != nil {
			return fmt.Errorf("start: %w", err)
		}
		r.fail = s.failRate > 0 && s.rng.Float64() < s.failRate
		fmt.Printf("%s on %s: on the way\n", d.Reference, w.plate)
		return nil

	case "in_transit":
		next := nextStop(detail.Stops)
		if next == nil {
			if err := s.c.CompleteDelivery(ctx, d.ID); err != nil {
				return fmt.Errorf("complete delivery: %w", err)
			}
			delete(s.runs, d.ID)
			fmt.Printf("%s on %s: delivered\n", d.Reference, w.plate)
			s.goHome(ctx, truckID, w.plate)
			return nil
		}
		if next.Lat == nil || next.Lng == nil {
			return fmt.Errorf("stop %d has no coordinates", next.Sequence)
		}
		target := position{Lat: *next.Lat, Lng: *next.Lng}

		if next.Status == "arrived" {
			// Loading or unloading. Keep reporting so the truck doesn't go silent.
			if r.dwellUntil.IsZero() {
				r.dwellUntil = now.Add(s.dwell)
			}
			pos := target
			if p, ok := s.at[truckID]; ok {
				pos = p
			}
			if now.Before(r.dwellUntil) {
				return s.report(ctx, truckID, pos, 0)
			}
			r.dwellUntil = time.Time{}
			r.leg, r.stopID = nil, ""
			if r.fail && next.StopType == "dropoff" {
				if err := s.c.FailDelivery(ctx, d.ID, "customer_unavailable"); err != nil {
					return fmt.Errorf("fail: %w", err)
				}
				delete(s.runs, d.ID)
				fmt.Printf("%s on %s: failed at stop %d (customer unavailable)\n", d.Reference, w.plate, next.Sequence)
				s.goHome(ctx, truckID, w.plate)
				return nil
			}
			if err := s.c.CompleteStop(ctx, d.ID, next.ID); err != nil {
				return fmt.Errorf("complete stop %d: %w", next.Sequence, err)
			}
			verb := "collected at"
			if next.StopType == "dropoff" {
				verb = "dropped off at"
			}
			fmt.Printf("%s on %s: %s stop %d (%s)\n", d.Reference, w.plate, verb, next.Sequence, next.Address)
			return nil
		}

		// Driving. A new stop (or a restart of this program) needs a route.
		if r.leg == nil || r.stopID != next.ID {
			from := s.startFrom(truckID, detail)
			if distanceM(from, target) > maxApproachM {
				from = s.near(target)
			}
			leg, err := s.routes.route(ctx, from, target)
			if err != nil {
				log.Printf("%s: %v; driving in a straight line", d.Reference, err)
			}
			r.leg, r.stopID = leg, next.ID
			fmt.Printf("%s on %s: heading to stop %d, %.1f km%s\n",
				d.Reference, w.plate, next.Sequence, leg.length()/1000, map[bool]string{true: " by road", false: " (straight line)"}[leg.byRoad])
		}

		pos := r.leg.advance(s.metres)
		s.at[truckID] = pos
		if r.leg.remaining() > arrivalRadiusM {
			return s.report(ctx, truckID, pos, s.kph)
		}
		s.at[truckID] = target
		if err := s.report(ctx, truckID, target, 0); err != nil {
			return err
		}
		if err := s.c.ArriveAtStop(ctx, d.ID, next.ID); err != nil {
			return fmt.Errorf("arrive at stop %d: %w", next.Sequence, err)
		}
		r.dwellUntil = now.Add(s.dwell)
		fmt.Printf("%s on %s: arrived at stop %d (%s)\n", d.Reference, w.plate, next.Sequence, next.Address)
		return nil
	}
	return nil
}

// goHome sends a truck that has finished back to the depot.
func (s *sim) goHome(ctx context.Context, truckID, plate string) {
	from, ok := s.at[truckID]
	if s.depot == nil || !ok || distanceM(from, *s.depot) <= arrivalRadiusM {
		return
	}
	leg, err := s.routes.route(ctx, from, *s.depot)
	if err != nil {
		log.Printf("%s: %v; driving back in a straight line", plate, err)
	}
	s.returns[truckID] = &homeward{leg: leg, plate: plate}
	fmt.Printf("%s: heading back to the depot, %.1f km\n", plate, leg.length()/1000)
}

// drive moves every truck on its way back one tick, and parks it at the
// depot when it gets there. Its last position is then the depot.
func (s *sim) drive(ctx context.Context) {
	for truckID, h := range s.returns {
		pos := h.leg.advance(s.metres)
		speed := s.kph
		done := h.leg.remaining() <= arrivalRadiusM
		if done {
			pos, speed = *s.depot, 0
			delete(s.returns, truckID)
			fmt.Printf("%s: parked at the depot\n", h.plate)
		}
		s.at[truckID] = pos
		if err := s.report(ctx, truckID, pos, speed); err != nil {
			log.Printf("%s: %v", h.plate, err)
		}
	}
}

// startFrom: where this process last put the truck, else the server's last
// position, else the depot (or map centre).
func (s *sim) startFrom(truckID string, d client.DeliveryDetail) position {
	if p, ok := s.at[truckID]; ok {
		return p
	}
	if d.Delivery.TruckLat != nil && d.Delivery.TruckLng != nil {
		return position{Lat: *d.Delivery.TruckLat, Lng: *d.Delivery.TruckLng}
	}
	if pk := firstStop(d.Stops); pk != nil && distanceM(s.home, *pk) > maxApproachM {
		return s.near(*pk)
	}
	return s.home
}

// near is a point a few km from p, toward home, so a truck has a short,
// visible drive to it. Kept inside the service area.
func (s *sim) near(p position) position {
	toward := s.home
	if distanceM(toward, p) < 100 {
		toward = position{Lat: p.Lat + 0.05, Lng: p.Lng}
	}
	q := moveToward(p, toward, 3000)
	if s.area != nil && !s.area.Contains(q.Lat, q.Lng) {
		return p
	}
	return q
}

func (s *sim) report(ctx context.Context, truckID string, p position, kph float64) error {
	speed := float32(kph)
	acc := float32(8)
	pings := []client.Ping{{Lat: p.Lat, Lng: p.Lng, RecordedAt: s.now(), SpeedKph: &speed, AccuracyM: &acc}}
	if err := s.c.PostLocations(ctx, truckID, pings); err != nil {
		var apiErr *client.APIError
		if errors.As(err, &apiErr) {
			return fmt.Errorf("post position: %s", apiErr.Message)
		}
		return err
	}
	return nil
}

// nextStop is the first stop that isn't finished with.
func nextStop(stops []client.Stop) *client.Stop {
	for i := range stops {
		if stops[i].Status != "completed" && stops[i].Status != "skipped" {
			return &stops[i]
		}
	}
	return nil
}

func firstStop(stops []client.Stop) *position {
	for _, s := range stops {
		if s.Lat != nil && s.Lng != nil {
			return &position{Lat: *s.Lat, Lng: *s.Lng}
		}
	}
	return nil
}
