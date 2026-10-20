// Package eta estimates when a truck reaches each stop it has left.
//
// It's an estimate, not routing: straight-line distance stretched by a road
// factor, at an average speed the operator sets for their traffic, plus a
// fixed time at each stop. Cheap enough to run on every list request and
// needs nothing outside this process. Good to within the nearest quarter of
// an hour in a city, which is what a dispatcher and a customer need.
package eta

import (
	"math"
	"time"
)

// RoadFactor: roads are longer than the straight line between two points.
// 1.3 is the usual figure for city streets.
const RoadFactor = 1.3

type Point struct{ Lat, Lng float64 }

type Stop struct {
	ID        string
	Pos       Point
	Status    string // pending or arrived; finished stops aren't passed in
	ArrivedAt *time.Time
}

type Params struct {
	SpeedKph    float64
	StopMinutes float64
}

type StopETA struct {
	ID string    `json:"stop_id"`
	At time.Time `json:"at"`
}

type Result struct {
	// When the truck reaches the last stop.
	Arrival time.Time `json:"arrival"`
	Stops   []StopETA `json:"stops"`
}

// Estimate walks the stops still to do from the truck's position. Nil when
// there's nothing to estimate: no stops left, or no position to start from.
func Estimate(now time.Time, truck *Point, stops []Stop, p Params) *Result {
	if len(stops) == 0 || truck == nil || p.SpeedKph <= 0 {
		return nil
	}
	dwell := time.Duration(p.StopMinutes * float64(time.Minute))
	t, pos := now, *truck
	res := &Result{Stops: make([]StopETA, 0, len(stops))}
	for _, s := range stops {
		var at time.Time
		if s.Status == "arrived" {
			// Already there: it leaves once the stop's time is up, or now
			// if that has passed and it's still there.
			at = now
			if s.ArrivedAt != nil {
				at = *s.ArrivedAt
			}
			t = maxTime(now, at.Add(dwell))
		} else {
			at = t.Add(travel(pos, s.Pos, p.SpeedKph))
			t = at.Add(dwell)
		}
		res.Stops = append(res.Stops, StopETA{ID: s.ID, At: at.UTC().Truncate(time.Second)})
		pos = s.Pos
	}
	res.Arrival = res.Stops[len(res.Stops)-1].At
	return res
}

func travel(a, b Point, kph float64) time.Duration {
	hours := KM(a, b) * RoadFactor / kph
	return time.Duration(hours * float64(time.Hour))
}

// KM is the great-circle distance.
func KM(a, b Point) float64 {
	rad := func(d float64) float64 { return d * math.Pi / 180 }
	dφ, dλ := rad(b.Lat-a.Lat), rad(b.Lng-a.Lng)
	h := math.Sin(dφ/2)*math.Sin(dφ/2) + math.Cos(rad(a.Lat))*math.Cos(rad(b.Lat))*math.Sin(dλ/2)*math.Sin(dλ/2)
	return 2 * 6371 * math.Asin(math.Sqrt(h))
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
