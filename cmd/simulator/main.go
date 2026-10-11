package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"github.com/WllowyEmmie/trucking-logistics/internal/client"
)

// Where a truck starts when it has never reported a position. Apapa Wharf:
// the freight hub most seeded deliveries collect from.
var depot = position{Lat: 6.4433, Lng: 3.3647}

const (
	// Within this many metres of a stop counts as arrived.
	arrivalRadiusM = 150
	// Earth's mean radius, for the distance and bearing maths.
	earthRadiusM = 6_371_000
)

type position struct {
	Lat float64
	Lng float64
}

func rad(d float64) float64 { return d * math.Pi / 180 }
func deg(r float64) float64 { return r * 180 / math.Pi }

// distanceM is the great-circle distance in metres. Over city distances the
// curvature barely matters, but getting it right costs nothing.
func distanceM(a, b position) float64 {
	φ1, φ2 := rad(a.Lat), rad(b.Lat)
	dφ := rad(b.Lat - a.Lat)
	dλ := rad(b.Lng - a.Lng)

	h := math.Sin(dφ/2)*math.Sin(dφ/2) +
		math.Cos(φ1)*math.Cos(φ2)*math.Sin(dλ/2)*math.Sin(dλ/2)
	return 2 * earthRadiusM * math.Asin(math.Sqrt(h))
}

// bearing is the initial compass heading from a to b, in radians.
func bearing(a, b position) float64 {
	φ1, φ2 := rad(a.Lat), rad(b.Lat)
	dλ := rad(b.Lng - a.Lng)

	y := math.Sin(dλ) * math.Cos(φ2)
	x := math.Cos(φ1)*math.Sin(φ2) - math.Sin(φ1)*math.Cos(φ2)*math.Cos(dλ)
	return math.Atan2(y, x)
}

// moveToward returns the point `metres` along the great circle from a to b.
func moveToward(a, b position, metres float64) position {
	θ := bearing(a, b)
	δ := metres / earthRadiusM
	φ1, λ1 := rad(a.Lat), rad(a.Lng)

	φ2 := math.Asin(math.Sin(φ1)*math.Cos(δ) + math.Cos(φ1)*math.Sin(δ)*math.Cos(θ))
	λ2 := λ1 + math.Atan2(
		math.Sin(θ)*math.Sin(δ)*math.Cos(φ1),
		math.Cos(δ)-math.Sin(φ1)*math.Sin(φ2),
	)
	return position{Lat: deg(φ2), Lng: deg(λ2)}
}

func main() {
	_ = godotenv.Load()

	var (
		baseURL  = flag.String("api", envOr("SEED_API_URL", "http://localhost:8080"), "API base URL")
		speed    = flag.Float64("speed", 10, "speed multiplier; 1 is real time")
		kph      = flag.Float64("kph", 30, "base road speed in km/h before the multiplier")
		tick     = flag.Duration("tick", 3*time.Second, "how often each truck reports")
		rescan   = flag.Duration("rescan", 30*time.Second, "how often to look for new in-transit work")
	)
	flag.Parse()

	c := client.New(*baseURL)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := c.Get(ctx, "/health", nil); err != nil {
		log.Fatalf("API not reachable at %s: %v", *baseURL, err)
	}

	s := &sim{
		c:        c,
		metres:   (*kph * *speed * 1000 / 3600) * tick.Seconds(),
		at:       map[string]position{},
		dwelling: map[string]bool{},
	}

	fmt.Printf("simulator running — %.0f km/h x%.0f, reporting every %s\nctrl-c to stop\n",
		*kph, *speed, *tick)

	ticker := time.NewTicker(*tick)
	defer ticker.Stop()
	rescanner := time.NewTicker(*rescan)
	defer rescanner.Stop()

	var work []client.DeliveryListItem
	work = s.scan(ctx)

	for {
		select {
		case <-ctx.Done():
			fmt.Println("\nstopped")
			return
		case <-rescanner.C:
			work = s.scan(ctx)
		case <-ticker.C:
			for _, d := range work {
				if err := s.step(ctx, d); err != nil {
					// One truck's problem is not the whole run's problem.
					log.Printf("%s: %v", d.Reference, err)
				}
			}
		}
	}
}

type sim struct {
	c *client.Client
	// How far a truck moves per tick, in metres.
	metres float64
	// Last known position per truck, so a truck without a server position
	// still has somewhere to move from.
	at map[string]position
	// Trucks that arrived last tick and complete the stop on this one.
	dwelling map[string]bool
}

func (s *sim) scan(ctx context.Context) []client.DeliveryListItem {
	ds, err := s.c.ListDeliveries(ctx, "in_transit")
	if err != nil {
		log.Printf("scan: %v", err)
		return nil
	}
	out := ds[:0]
	for _, d := range ds {
		if d.TruckID != nil {
			out = append(out, d)
		}
	}
	return out
}

func (s *sim) step(ctx context.Context, d client.DeliveryListItem) error {
	detail, err := s.c.GetDelivery(ctx, d.ID)
	if err != nil {
		return err
	}
	// The scan is up to 30s old; the delivery may have moved on since.
	if detail.Delivery.Status != "in_transit" {
		return nil
	}
	truckID := *d.TruckID

	next := nextStop(detail.Stops)
	if next == nil {
		// Every stop done: the delivery itself can finish.
		if err := s.c.CompleteDelivery(ctx, d.ID); err != nil {
			return fmt.Errorf("complete delivery: %w", err)
		}
		delete(s.at, truckID)
		delete(s.dwelling, truckID)
		fmt.Printf("%s delivered\n", d.Reference)
		return nil
	}
	if next.Lat == nil || next.Lng == nil {
		return fmt.Errorf("stop %d has no coordinates", next.Sequence)
	}
	target := position{Lat: *next.Lat, Lng: *next.Lng}

	// A stop already marked arrived just needs completing. One tick of
	// dwell so loading looks like it takes a moment.
	if next.Status == "arrived" {
		if !s.dwelling[truckID] {
			s.dwelling[truckID] = true
			return nil
		}
		delete(s.dwelling, truckID)
		if err := s.c.CompleteStop(ctx, d.ID, next.ID); err != nil {
			return fmt.Errorf("complete stop %d: %w", next.Sequence, err)
		}
		fmt.Printf("%s left stop %d (%s)\n", d.Reference, next.Sequence, next.Address)
		return nil
	}

	from := s.positionOf(truckID, detail, target)
	dist := distanceM(from, target)

	if dist <= arrivalRadiusM {
		s.at[truckID] = target
		if err := s.report(ctx, truckID, target, 0); err != nil {
			return err
		}
		if err := s.c.ArriveAtStop(ctx, d.ID, next.ID); err != nil {
			return fmt.Errorf("arrive at stop %d: %w", next.Sequence, err)
		}
		fmt.Printf("%s reached stop %d (%s)\n", d.Reference, next.Sequence, next.Address)
		return nil
	}

	moved := moveToward(from, target, math.Min(s.metres, dist))
	s.at[truckID] = moved
	return s.report(ctx, truckID, moved, s.metres/1000*3600/float64(1))
}

// positionOf prefers the server's last known position, falls back to what
// this process last sent, and finally starts the truck at the depot — or at
// the stop itself if the depot is the stop, which happens on a pickup.
func (s *sim) positionOf(truckID string, d client.DeliveryDetail, target position) position {
	if d.Delivery.TruckLat != nil && d.Delivery.TruckLng != nil {
		return position{Lat: *d.Delivery.TruckLat, Lng: *d.Delivery.TruckLng}
	}
	if p, ok := s.at[truckID]; ok {
		return p
	}
	return depot
}

func (s *sim) report(ctx context.Context, truckID string, p position, kph float64) error {
	speed := float32(kph)
	acc := float32(8)
	pings := []client.Ping{{
		Lat:        p.Lat,
		Lng:        p.Lng,
		RecordedAt: time.Now(),
		SpeedKph:   &speed,
		AccuracyM:  &acc,
	}}
	if err := s.c.PostLocations(ctx, truckID, pings); err != nil {
		var apiErr *client.APIError
		if errors.As(err, &apiErr) {
			return fmt.Errorf("post locations: %s", apiErr.Message)
		}
		return err
	}
	return nil
}

// nextStop is the first stop that isn't finished with. Completed and skipped
// stops are behind us; pending and arrived are not.
func nextStop(stops []client.Stop) *client.Stop {
	for i := range stops {
		s := stops[i]
		if s.Status != "completed" && s.Status != "skipped" {
			return &stops[i]
		}
	}
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}