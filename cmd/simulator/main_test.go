package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/WllowyEmmie/trucking-logistics/internal/client"
)

// fakeAPI is just enough of the API for one delivery on one truck, with the
// same status rules: assigned → in_transit → stops arrived/completed → completed.
type fakeAPI struct {
	mu      sync.Mutex
	status  string
	stops   []map[string]any
	pings   []client.Ping
	calls   []string
	truckAt *[2]float64
	// A second truck, parked, its driver on shift.
	parked bool
	// Truck id of each position posted.
	pingTrucks []string
}

func newFakeAPI() *fakeAPI {
	return &fakeAPI{
		status: "assigned",
		stops: []map[string]any{
			{"id": "s1", "sequence": 1, "stop_type": "pickup", "address": "Apapa", "lat": 6.4433, "lng": 3.3647, "status": "pending"},
			{"id": "s2", "sequence": 2, "stop_type": "dropoff", "address": "Ikeja", "lat": 6.4600, "lng": 3.3700, "status": "pending"},
		},
	}
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := r.URL.Path
	if r.Method == http.MethodPost && p != "/locations" {
		f.calls = append(f.calls, p)
	}
	conflict := func(msg string) { w.WriteHeader(http.StatusConflict); fmt.Fprintf(w, `{"error":%q}`, msg) }
	switch {
	case p == "/trucks":
		ts := []map[string]any{{"id": "t1", "plate_number": "KF-01", "status": "on_delivery", "driver_id": "dr1", "driver_on_shift": true}}
		if f.parked {
			ts = append(ts, map[string]any{"id": "t2", "plate_number": "KF-02", "status": "available", "driver_id": "dr2", "driver_on_shift": true, "lat": 6.5, "lng": 3.4})
		}
		json.NewEncoder(w).Encode(ts)
	case p == "/deliveries":
		var out []map[string]any
		if r.URL.Query().Get("status") == f.status {
			out = append(out, map[string]any{"id": "d1", "reference": "DLV-1", "status": f.status, "truck_id": "t1"})
		}
		json.NewEncoder(w).Encode(out)
	case p == "/deliveries/d1":
		d := map[string]any{"id": "d1", "reference": "DLV-1", "status": f.status, "truck_id": "t1"}
		if f.truckAt != nil {
			d["truck_lat"], d["truck_lng"] = f.truckAt[0], f.truckAt[1]
		}
		json.NewEncoder(w).Encode(map[string]any{"delivery": d, "stops": f.stops})
	case p == "/deliveries/d1/start":
		if f.status != "assigned" {
			conflict("not assigned")
			return
		}
		f.status = "in_transit"
	case strings.HasSuffix(p, "/arrive"), strings.HasPrefix(p, "/deliveries/d1/stops/") && strings.HasSuffix(p, "/complete"):
		id := strings.Split(p, "/")[4]
		for _, s := range f.stops {
			if s["id"] == id {
				if strings.HasSuffix(p, "/arrive") {
					s["status"] = "arrived"
				} else {
					s["status"] = "completed"
				}
			}
		}
	case p == "/deliveries/d1/complete":
		for _, s := range f.stops {
			if s["status"] != "completed" {
				conflict("stops left")
				return
			}
		}
		f.status = "completed"
	case p == "/deliveries/d1/fail":
		f.status = "failed"
	case p == "/locations":
		var b struct {
			TruckID string        `json:"truck_id"`
			Pings   []client.Ping `json:"pings"`
		}
		json.NewDecoder(r.Body).Decode(&b)
		if b.TruckID == "t2" {
			f.pingTrucks = append(f.pingTrucks, b.TruckID)
			break
		}
		f.pings = append(f.pings, b.Pings...)
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"accepted":1}`))
		return
	default:
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodPost {
		w.WriteHeader(http.StatusNoContent)
	}
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newSim(t *testing.T, f *fakeAPI, clk *clock, failRate float64) *sim {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	kph, tick := 40.0*10, 3.0
	return &sim{
		c: client.New(srv.URL), routes: newRouter(""), now: clk.now, rng: rand.New(rand.NewPCG(1, 2)),
		kph: kph, metres: kph * 1000 / 3600 * tick,
		startAfter: 10 * time.Second, dwell: 20 * time.Second, failRate: failRate,
		home: position{Lat: 6.43, Lng: 3.36},
		runs: map[string]*run{}, at: map[string]position{}, returns: map[string]*homeward{},
	}
}

// tickUntil runs scan+step every 3 s of fake time until the delivery's
// status is want, or gives up.
func tickUntil(t *testing.T, s *sim, f *fakeAPI, clk *clock, want string) {
	t.Helper()
	for i := 0; i < 500; i++ {
		jobs, err := s.scan(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range jobs {
			if err := s.step(context.Background(), w); err != nil {
				t.Fatal(err)
			}
		}
		s.drive(context.Background())
		f.mu.Lock()
		got := f.status
		f.mu.Unlock()
		if got == want {
			return
		}
		clk.t = clk.t.Add(3 * time.Second)
	}
	t.Fatalf("never reached %s", want)
}

// Assigned → waits → started → drives → each stop arrived, dwelt, completed → delivered.
func TestDeliveryRunsItself(t *testing.T) {
	f, clk := newFakeAPI(), &clock{t: time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)}
	s := newSim(t, f, clk, 0)
	start := clk.t

	tickUntil(t, s, f, clk, "in_transit")
	if waited := clk.t.Sub(start); waited < 10*time.Second || waited > 15*time.Second {
		t.Fatalf("started after %s, want about 10s", waited)
	}
	tickUntil(t, s, f, clk, "completed")

	want := []string{"/deliveries/d1/start",
		"/deliveries/d1/stops/s1/arrive", "/deliveries/d1/stops/s1/complete",
		"/deliveries/d1/stops/s2/arrive", "/deliveries/d1/stops/s2/complete",
		"/deliveries/d1/complete"}
	if strings.Join(f.calls, " ") != strings.Join(want, " ") {
		t.Fatalf("calls:\n got %v\nwant %v", f.calls, want)
	}
	// It moved: positions get closer to the drop-off, and the last is on it.
	last := f.pings[len(f.pings)-1]
	if d := distanceM(position{last.Lat, last.Lng}, position{6.46, 3.37}); d > 1 {
		t.Fatalf("ended %.0f m from the drop-off", d)
	}
	if len(f.pings) < 5 {
		t.Fatalf("only %d positions posted", len(f.pings))
	}
	if r := s.runs["d1"]; r != nil {
		t.Fatal("finished delivery still tracked")
	}
}

func TestFailRateFailsAtDropoff(t *testing.T) {
	f, clk := newFakeAPI(), &clock{t: time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)}
	s := newSim(t, f, clk, 1)
	tickUntil(t, s, f, clk, "failed")
	if got := f.calls[len(f.calls)-1]; got != "/deliveries/d1/fail" {
		t.Fatalf("last call %s", got)
	}
	if f.stops[0]["status"] != "completed" {
		t.Fatal("should fail at the drop-off, after collecting")
	}
}

// A truck last seen in another city starts a short drive from the pickup.
func TestFarAwayTruckStartsNearby(t *testing.T) {
	f, clk := newFakeAPI(), &clock{t: time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)}
	f.truckAt = &[2]float64{53.68, -1.50} // Wakefield
	s := newSim(t, f, clk, 0)
	tickUntil(t, s, f, clk, "in_transit")
	tickUntil(t, s, f, clk, "in_transit") // one more step: the route is planned
	jobs, _ := s.scan(context.Background())
	s.step(context.Background(), jobs[0])
	r := s.runs["d1"]
	if r == nil || r.leg == nil || r.leg.length() > maxApproachM {
		t.Fatalf("leg %+v, want under %d m", r, maxApproachM)
	}
}

func TestRoadRouteAndFallback(t *testing.T) {
	osrm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/route/v1/driving/") {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"code":"Ok","routes":[{"geometry":{"coordinates":[[3.3647,6.4433],[3.3647,6.4500],[3.3700,6.4600]]}}]}`))
	}))
	defer osrm.Close()
	a, b := position{6.4433, 3.3647}, position{6.46, 3.37}

	p, err := newRouter(osrm.URL).route(context.Background(), a, b)
	if err != nil || !p.byRoad || len(p.pts) != 5 {
		t.Fatalf("road route: %v %+v", err, p)
	}
	if p.length() <= distanceM(a, b) {
		t.Fatal("a road route is longer than the straight line")
	}

	p, err = newRouter("http://127.0.0.1:1").route(context.Background(), a, b)
	if err == nil || p.byRoad || len(p.pts) != 2 {
		t.Fatalf("fallback: %v %+v", err, p)
	}
}

func TestPathAdvance(t *testing.T) {
	p := newPath([]position{{6.0, 3.0}, {6.01, 3.0}, {6.01, 3.01}}, true)
	half := p.length() / 2
	got := p.advance(half)
	if d := math.Abs(p.remaining() - half); d > 0.01 {
		t.Fatalf("remaining off by %.2f m", d)
	}
	// Halfway along an L of two equal legs is the corner.
	if distanceM(got, position{6.01, 3.0}) > 20 {
		t.Fatalf("halfway is %v", got)
	}
	if end := p.advance(1e9); end != (position{6.01, 3.01}) || p.remaining() != 0 {
		t.Fatalf("overshoot: %v", end)
	}
}

func TestOnlyLocalAPIUnlessAllowed(t *testing.T) {
	for _, u := range []string{"http://localhost:8080", "http://127.0.0.1:8080", "http://[::1]:8080", "http://api.localhost"} {
		if err := checkLocal(u, false); err != nil {
			t.Errorf("%s: %v", u, err)
		}
	}
	for _, u := range []string{"https://dispatch.example.com", "http://10.0.0.5:8080", "http://192.168.1.10:8080"} {
		if checkLocal(u, false) == nil {
			t.Errorf("%s allowed without -allow-remote", u)
		}
		if err := checkLocal(u, true); err != nil {
			t.Errorf("%s with -allow-remote: %v", u, err)
		}
	}
}

// With a depot, a truck that has finished drives back and parks there.
func TestDrivesBackToDepot(t *testing.T) {
	f, clk := newFakeAPI(), &clock{t: time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)}
	s := newSim(t, f, clk, 0)
	s.depot = &position{Lat: 6.43, Lng: 3.36}
	tickUntil(t, s, f, clk, "completed")
	if s.returns["t1"] == nil {
		t.Fatal("not heading back after delivering")
	}
	for i := 0; i < 200 && s.returns["t1"] != nil; i++ {
		s.drive(context.Background())
	}
	if s.returns["t1"] != nil {
		t.Fatal("never got back")
	}
	last := f.pings[len(f.pings)-1]
	if last.Lat != 6.43 || last.Lng != 3.36 || *last.SpeedKph != 0 {
		t.Fatalf("last position %+v, want parked at the depot", last)
	}
}

// Given a new delivery on the way back, it stops heading home.
func TestNewDeliveryCancelsTheTripBack(t *testing.T) {
	f, clk := newFakeAPI(), &clock{t: time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)}
	s := newSim(t, f, clk, 0)
	s.depot = &position{Lat: 6.43, Lng: 3.36}
	tickUntil(t, s, f, clk, "completed")
	f.mu.Lock()
	f.status = "assigned"
	for _, st := range f.stops {
		st["status"] = "pending"
	}
	f.mu.Unlock()
	if _, err := s.scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.returns["t1"] != nil {
		t.Fatal("still heading back with a new delivery")
	}
}

// A parked truck whose driver is on shift reports now and then, like a
// phone would, and the truck on a delivery isn't sent a parked position.
func TestParkedTrucksReport(t *testing.T) {
	f, clk := newFakeAPI(), &clock{t: time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)}
	f.parked = true
	s := newSim(t, f, clk, 0)
	for i := 0; i < 3; i++ { // 3 scans 10 s apart: one report
		if _, err := s.scan(context.Background()); err != nil {
			t.Fatal(err)
		}
		clk.t = clk.t.Add(10 * time.Second)
	}
	if len(f.pingTrucks) != 1 {
		t.Fatalf("parked reports: %d, want 1", len(f.pingTrucks))
	}
	clk.t = clk.t.Add(parkedEvery)
	s.scan(context.Background())
	if len(f.pingTrucks) != 2 {
		t.Fatalf("after a minute: %d, want 2", len(f.pingTrucks))
	}
}
