package api

// Integration tests: real handlers against a real Postgres with PostGIS and
// the migrations applied. They need TEST_DATABASE_URL, and the database name
// must contain "test" because every test empties the tables first.
//
//	docker compose up -d db
//	createdb -h 127.0.0.1 -p 5434 -U fleet fleet_test
//	goose -dir migrations postgres "postgres://fleet:...@127.0.0.1:5434/fleet_test?sslmode=disable" up
//	TEST_DATABASE_URL="postgres://fleet:...@127.0.0.1:5434/fleet_test?sslmode=disable" go test ./internal/api/
//
// Without TEST_DATABASE_URL they skip, so `go test ./...` stays green.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/WllowyEmmie/trucking-logistics/internal/auth"
	"github.com/WllowyEmmie/trucking-logistics/internal/client"
	"github.com/WllowyEmmie/trucking-logistics/internal/config"
)

type env struct {
	srv *httptest.Server
	// c is signed in as a dispatcher.
	c    *client.Client
	s    *Server
	pool *pgxpool.Pool
	// The agent's service key, as configured for this server.
	agentKey string
}

const (
	testEmail    = "ada@example.com"
	testPassword = "correct horse battery"
	testAgentKey = "test-agent-key-0123456789abcdef0123456789"
)

func setup(t *testing.T) *env {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	if !strings.Contains(dsn, "test") {
		t.Fatal("TEST_DATABASE_URL must name a test database: these tests empty every table")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, `TRUNCATE locations, delivery_events, delivery_stops, deliveries,
		trucks, drivers, proposals, sessions, dispatchers, settings, idempotency_keys RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	t.Setenv("SERVICE_MIN_LAT", "6")
	t.Setenv("SERVICE_MAX_LAT", "7")
	t.Setenv("SERVICE_MIN_LNG", "3")
	t.Setenv("SERVICE_MAX_LNG", "4")
	t.Setenv("AGENT_API_KEY", testAgentKey)
	cfg, err := config.Load(logger)
	if err != nil {
		t.Fatal(err)
	}
	s := NewServer(pool, logger, cfg)
	// Tests fire hundreds of requests a second; TestStaffRateLimit sets its own.
	s.limits = staffLimits{read: map[string]limit{}, write: map[string]limit{}, ping: map[string]limit{}}
	srv := httptest.NewServer(s.Routes())
	t.Cleanup(srv.Close)

	hash, err := auth.HashSecret(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.queries.UpsertDispatcher(ctx, testEmail, "Ada Obi", hash); err != nil {
		t.Fatal(err)
	}
	c := client.New(srv.URL)
	if _, err := c.Login(ctx, testEmail, testPassword); err != nil {
		t.Fatal(err)
	}
	return &env{srv: srv, c: c, s: s, pool: pool, agentKey: testAgentKey}
}

var twoStops = []client.StopInput{
	{StopType: "pickup", Address: "Apapa Wharf", Lat: 6.4433, Lng: 3.3647},
	{StopType: "dropoff", Address: "Ikeja", Lat: 6.6018, Lng: 3.3515},
}

// newDelivery creates a delivery and a truck, and walks it to `to`
// ("pending", "assigned" or "in_transit").
func (e *env) newDelivery(t *testing.T, plate, to string) (client.CreateDeliveryResult, client.Truck) {
	t.Helper()
	ctx := context.Background()
	truck := e.truckWithDriver(t, plate)
	d, err := e.c.CreateDelivery(ctx, client.CreateDeliveryInput{CustomerName: "Chidi", Stops: twoStops})
	if err != nil {
		t.Fatal(err)
	}
	if to == "pending" {
		return d, truck
	}
	if err := e.c.AssignTruck(ctx, d.Delivery.ID, truck.ID); err != nil {
		t.Fatal(err)
	}
	if to == "in_transit" {
		if err := e.c.StartDelivery(ctx, d.Delivery.ID); err != nil {
			t.Fatal(err)
		}
	}
	return d, truck
}

var phoneSeq atomic.Int64

// truckWithDriver: a new truck with a new driver on it, ready to assign.
func (e *env) truckWithDriver(t *testing.T, plate string) client.Truck {
	t.Helper()
	ctx := context.Background()
	truck, err := e.c.CreateTruck(ctx, plate, nil)
	if err != nil {
		t.Fatal(err)
	}
	d, err := e.c.CreateDriver(ctx, "Driver "+plate, fmt.Sprintf("+23480%08d", phoneSeq.Add(1)))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.c.UpdateTruck(ctx, truck.ID, &d.ID, nil); err != nil {
		t.Fatal(err)
	}
	return truck
}

func status(err error) int {
	var ae *client.APIError
	if errors.As(err, &ae) {
		return ae.Status
	}
	if err == nil {
		return 200
	}
	return -1
}

// Start and cancel racing on one assigned delivery. Both may succeed now
// (cancelling in transit is allowed), but only in that order, and the truck
// must always match: before the row lock a delivery could end cancelled with
// its truck still on_delivery.
func TestStatusChangesAreSerialised(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	for i := 0; i < 40; i++ {
		d, truck := e.newDelivery(t, "RACE-"+string(rune('A'+i%26))+string(rune('A'+i/26)), "assigned")
		var wg sync.WaitGroup
		var startErr, cancelErr error
		wg.Add(2)
		go func() { defer wg.Done(); startErr = e.c.StartDelivery(ctx, d.Delivery.ID) }()
		go func() { defer wg.Done(); cancelErr = e.c.CancelDelivery(ctx, d.Delivery.ID) }()
		wg.Wait()
		got, err := e.c.GetDelivery(ctx, d.Delivery.ID)
		if err != nil {
			t.Fatal(err)
		}
		if startErr == nil && cancelErr == nil && got.Delivery.Status != "cancelled" {
			t.Fatalf("run %d: both succeeded but it's %s", i, got.Delivery.Status)
		}
		trucks, _ := e.c.ListTrucks(ctx)
		var ts string
		for _, x := range trucks {
			if x.ID == truck.ID {
				ts = x.Status
			}
		}
		switch got.Delivery.Status {
		case "in_transit":
			if ts != "on_delivery" {
				t.Fatalf("run %d: in transit but truck is %s", i, ts)
			}
		case "cancelled":
			if ts != "available" {
				t.Fatalf("run %d: cancelled but truck is %s", i, ts)
			}
		default:
			t.Fatalf("run %d: unexpected status %s", i, got.Delivery.Status)
		}
	}
}

func TestCompleteNeedsEveryStopDone(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	d, _ := e.newDelivery(t, "KF-01", "in_transit")
	if got := status(e.c.CompleteDelivery(ctx, d.Delivery.ID)); got != http.StatusConflict {
		t.Fatalf("complete with open stops: got %d, want 409", got)
	}
	detail, _ := e.c.GetDelivery(ctx, d.Delivery.ID)
	for _, s := range detail.Stops {
		if err := e.c.ArriveAtStop(ctx, d.Delivery.ID, s.ID); err != nil {
			t.Fatal(err)
		}
		if err := e.c.CompleteStop(ctx, d.Delivery.ID, s.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.c.CompleteDelivery(ctx, d.Delivery.ID); err != nil {
		t.Fatalf("complete after every stop: %v", err)
	}
}

// A finished delivery's link must not keep following its truck.
func TestTrackingHidesTruckWhenFinished(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	d, truck := e.newDelivery(t, "KF-02", "in_transit")
	if err := e.c.PostLocations(ctx, truck.ID, []client.Ping{{Lat: 6.5, Lng: 3.36, RecordedAt: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	track := func() map[string]any {
		var out map[string]any
		if err := e.c.Get(ctx, "/track/"+d.Delivery.TrackingToken, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if track()["truck_lat"] == nil {
		t.Fatal("in transit: truck position should be shown")
	}
	if err := e.c.FailDelivery(ctx, d.Delivery.ID, "customer_unavailable"); err != nil {
		t.Fatal(err)
	}
	if got := track(); got["truck_lat"] != nil || got["truck_lng"] != nil || got["truck_seen_at"] != nil {
		t.Fatalf("failed delivery still shows the truck: %v %v", got["truck_lat"], got["truck_lng"])
	}
}

func TestOversizedBodyRefused(t *testing.T) {
	e := setup(t)
	// Valid in every other way, so only the size can be the reason for a 400.
	stops, _ := json.Marshal(twoStops)
	big := `{"customer_name":"Chidi","goods_desc":"` + strings.Repeat("x", 2<<20) + `","stops":` + string(stops) + `}`
	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/deliveries", strings.NewReader(big))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.c.Token())
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("2 MB body: got %d, want 400", res.StatusCode)
	}
}

func TestCORSUsesConfiguredOrigins(t *testing.T) {
	t.Setenv("CORS_ORIGINS", "https://dispatch.example.com")
	e := setup(t)
	for origin, want := range map[string]string{
		"https://dispatch.example.com": "https://dispatch.example.com",
		"http://localhost:3000":        "",
	} {
		req, _ := http.NewRequest(http.MethodGet, e.srv.URL+"/health", nil)
		req.Header.Set("Origin", origin)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if got := res.Header.Get("Access-Control-Allow-Origin"); got != want {
			t.Errorf("origin %s: allow-origin %q, want %q", origin, got, want)
		}
	}
}

// The socket sends a snapshot, then every position after it, and lets go of
// the client as soon as it disconnects.
func TestPositionsSocket(t *testing.T) {
	e := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	truck, err := e.c.CreateTruck(ctx, "KF-03", nil)
	if err != nil {
		t.Fatal(err)
	}
	wsURL := "ws" + strings.TrimPrefix(e.srv.URL, "http") + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: http.Header{
		"Origin": {"http://localhost:3000"}, "Authorization": {"Bearer " + e.c.Token()}}})
	if err != nil {
		t.Fatal(err)
	}
	read := func() wsMessage {
		_, b, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var m wsMessage
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	if m := read(); m.Type != "snapshot" || len(m.Trucks) != 1 {
		t.Fatalf("first frame: %+v", m)
	}
	if err := e.c.PostLocations(ctx, truck.ID, []client.Ping{{Lat: 6.5, Lng: 3.4, RecordedAt: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	if m := read(); m.Type != "position" || m.Truck == nil || m.Truck.ID != truck.ID {
		t.Fatalf("second frame: %+v", m)
	}

	conn.Close(websocket.StatusNormalClosure, "")
	deadline := time.Now().Add(3 * time.Second)
	for {
		e.s.hub.mu.RLock()
		n := len(e.s.hub.clients)
		e.s.hub.mu.RUnlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("client still registered after closing: %d", n)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A socket from an origin not in CORS_ORIGINS is refused.
func TestSocketRefusesUnknownOrigin(t *testing.T) {
	e := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(e.srv.URL, "http") + "/ws"
	_, res, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: http.Header{
		"Origin": {"https://evil.example"}, "Authorization": {"Bearer " + e.c.Token()}}})
	if err == nil {
		t.Fatal("socket from an unknown origin was accepted")
	}
	if res != nil && res.StatusCode != http.StatusForbidden {
		t.Fatalf("got %d, want 403", res.StatusCode)
	}
}
