package eta

import (
	"testing"
	"time"
)

func TestEstimate(t *testing.T) {
	now := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	p := Params{SpeedKph: 26, StopMinutes: 10}
	truck := Point{6.45, 3.36}
	// ~10 km north: 10 km x 1.3 at 26 km/h = 30 min.
	a := Point{6.45 + 10/111.2, 3.36}
	b := Point{a.Lat + 10/111.2, 3.36}

	r := Estimate(now, &truck, []Stop{{ID: "a", Pos: a, Status: "pending"}, {ID: "b", Pos: b, Status: "pending"}}, p)
	if r == nil || len(r.Stops) != 2 {
		t.Fatal("no estimate")
	}
	near := func(got, want time.Time) bool { d := got.Sub(want); return d > -time.Minute && d < time.Minute }
	if !near(r.Stops[0].At, now.Add(30*time.Minute)) {
		t.Errorf("first stop %s, want about 09:30", r.Stops[0].At)
	}
	// 30 min there, 10 at the stop, 30 more.
	if !near(r.Arrival, now.Add(70*time.Minute)) {
		t.Errorf("arrival %s, want about 10:10", r.Arrival)
	}

	// At the first stop since 08:55: leaves at 09:05, reaches b at 09:35.
	arrived := now.Add(-5 * time.Minute)
	r = Estimate(now, &a, []Stop{{ID: "a", Pos: a, Status: "arrived", ArrivedAt: &arrived}, {ID: "b", Pos: b, Status: "pending"}}, p)
	if !near(r.Stops[0].At, arrived) || !near(r.Arrival, now.Add(35*time.Minute)) {
		t.Errorf("arrived case: %+v", r)
	}
	// Overstayed: still there at 09:00 after arriving 08:30, so it leaves now.
	long := now.Add(-30 * time.Minute)
	r = Estimate(now, &a, []Stop{{ID: "a", Pos: a, Status: "arrived", ArrivedAt: &long}, {ID: "b", Pos: b, Status: "pending"}}, p)
	if !near(r.Arrival, now.Add(30*time.Minute)) {
		t.Errorf("overstayed: %s", r.Arrival)
	}

	if Estimate(now, nil, []Stop{{Pos: a}}, p) != nil || Estimate(now, &truck, nil, p) != nil {
		t.Error("nothing to estimate should be nil")
	}
}
