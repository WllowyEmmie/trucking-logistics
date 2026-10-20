package api

// Sign-in and roles. Same TEST_DATABASE_URL setup as api_test.go.

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/WllowyEmmie/trucking-logistics/internal/client"
)

// raw sends a request with an optional bearer token and returns the status.
func (e *env) raw(t *testing.T, method, path, token, contentType, body string) int {
	t.Helper()
	req, _ := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

// newDriver adds a driver with a PIN as the usual driver of a new truck, and
// returns a client signed in as them.
func (e *env) newDriver(t *testing.T, name, phone, plate string) (*client.Client, client.Truck, client.Driver) {
	t.Helper()
	ctx := context.Background()
	d, err := e.c.CreateDriver(ctx, name, phone)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.c.SetDriverPIN(ctx, d.ID, "4821"); err != nil {
		t.Fatal(err)
	}
	truck, err := e.c.CreateTruck(ctx, plate, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.c.UpdateTruck(ctx, truck.ID, &d.ID, nil); err != nil {
		t.Fatal(err)
	}
	dc := client.New(e.srv.URL)
	// Typed differently from how it was stored: sign-in matches digits only.
	if _, err := dc.DriverLogin(ctx, strings.ReplaceAll(phone, " ", ""), "4821"); err != nil {
		t.Fatal(err)
	}
	return dc, truck, d
}

func TestAnonymousIsRefused(t *testing.T) {
	e := setup(t)
	for _, rt := range [][2]string{
		{"GET", "/trucks"}, {"GET", "/drivers"}, {"GET", "/deliveries"}, {"GET", "/proposals"},
		{"GET", "/geocode?q=wharf"}, {"GET", "/ws"},
		{"POST", "/locations"}, {"POST", "/trucks"}, {"POST", "/drivers"}, {"POST", "/deliveries"},
		{"POST", "/proposals"}, {"POST", "/deliveries/00000000-0000-0000-0000-000000000000/assign"},
	} {
		if got := e.raw(t, rt[0], rt[1], "", "application/json", "{}"); got != http.StatusUnauthorized {
			t.Errorf("%s %s without sign-in: %d, want 401", rt[0], rt[1], got)
		}
		if got := e.raw(t, rt[0], rt[1], "made-up-token", "application/json", "{}"); got != http.StatusUnauthorized {
			t.Errorf("%s %s with a made-up token: %d, want 401", rt[0], rt[1], got)
		}
	}
	// Still public.
	for _, p := range []string{"/health", "/config"} {
		if got := e.raw(t, "GET", p, "", "", ""); got != http.StatusOK {
			t.Errorf("GET %s: %d, want 200", p, got)
		}
	}
}

// A driver sees and acts on their own truck and its deliveries only.
func TestDriverScope(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	dc, mine, me := e.newDriver(t, "Bayo", "+234 803 000 0001", "KF-10")
	_, other, _ := e.newDriver(t, "Chika", "+234 803 000 0002", "KF-11")

	myDel, _ := e.newDeliveryOn(t, mine.ID)
	otherDel, _ := e.newDeliveryOn(t, other.ID)

	who, err := dc.DriverLogin(ctx, "+2348030000001", "4821")
	if err != nil || who.Role != "driver" || who.TruckID == nil || *who.TruckID != mine.ID {
		t.Fatalf("me: %+v %v", who, err)
	}

	trucks, _ := dc.ListTrucks(ctx)
	if len(trucks) != 1 || trucks[0].ID != mine.ID {
		t.Fatalf("driver sees trucks %+v", trucks)
	}
	var drivers []client.Driver
	if err := dc.Get(ctx, "/drivers", &drivers); err != nil || len(drivers) != 1 || drivers[0].ID != me.ID {
		t.Fatalf("driver sees drivers %+v %v", drivers, err)
	}
	list, _ := dc.ListDeliveries(ctx, "")
	if len(list) != 1 || list[0].ID != myDel {
		t.Fatalf("driver sees deliveries %+v", list)
	}

	if got := status(dc.StartDelivery(ctx, myDel)); got != 200 {
		t.Fatalf("start own delivery: %d", got)
	}
	if got := status(dc.PostLocations(ctx, mine.ID, []client.Ping{{Lat: 6.5, Lng: 3.4, RecordedAt: time.Now()}})); got != 200 {
		t.Fatalf("own position: %d", got)
	}

	// Someone else's: as if it didn't exist.
	if _, err := dc.GetDelivery(ctx, otherDel); status(err) != 404 {
		t.Errorf("other delivery detail: %d, want 404", status(err))
	}
	if got := status(dc.StartDelivery(ctx, otherDel)); got != 404 {
		t.Errorf("start other delivery: %d, want 404", got)
	}
	if got := status(dc.PostLocations(ctx, other.ID, []client.Ping{{Lat: 6.5, Lng: 3.4, RecordedAt: time.Now()}})); got != 404 {
		t.Errorf("other truck's position: %d, want 404", got)
	}

	// Dispatch work: refused.
	if got := status(dc.AssignTruck(ctx, otherDel, mine.ID)); got != 403 {
		t.Errorf("driver assigning: %d, want 403", got)
	}
	if got := status(dc.CancelDelivery(ctx, myDel)); got != 403 {
		t.Errorf("driver cancelling: %d, want 403", got)
	}
	if _, err := dc.CreateTruck(ctx, "KF-99", nil); status(err) != 403 {
		t.Errorf("driver adding a truck: %d, want 403", status(err))
	}
	if got := e.raw(t, "GET", "/proposals", dc.Token(), "", ""); got != 403 {
		t.Errorf("driver reading proposals: %d, want 403", got)
	}
	ctxWS, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(e.srv.URL, "http") + "/ws"
	if _, res, err := websocket.Dial(ctxWS, wsURL, &websocket.DialOptions{HTTPHeader: http.Header{
		"Origin": {"http://localhost:3000"}, "Authorization": {"Bearer " + dc.Token()}}}); err == nil || res == nil || res.StatusCode != 403 {
		t.Errorf("driver opening the fleet socket: want 403")
	}
}

// newDeliveryOn creates a delivery assigned to a given truck.
func (e *env) newDeliveryOn(t *testing.T, truckID string) (string, client.CreateDeliveryResult) {
	t.Helper()
	ctx := context.Background()
	d, err := e.c.CreateDelivery(ctx, client.CreateDeliveryInput{CustomerName: "Chidi", Stops: twoStops})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.c.AssignTruck(ctx, d.Delivery.ID, truckID); err != nil {
		t.Fatal(err)
	}
	return d.Delivery.ID, d
}

func proposalFor(deliveryID, truckID string) map[string]any {
	return map[string]any{
		"kind": "change", "title": "Assign", "why": "close by",
		"delivery_id": deliveryID, "truck_id": truckID,
		"action": map[string]string{"type": "assign", "delivery_id": deliveryID, "truck_id": truckID},
	}
}

// Only the agent writes proposals; only a dispatcher decides them, and the
// decision records who.
func TestProposalsAgentWritesDispatcherDecides(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	d, truck := e.newDelivery(t, "KF-20", "pending")
	agent := client.New(e.srv.URL).WithToken(e.agentKey)

	if _, err := agent.ListTrucks(ctx); err != nil {
		t.Fatalf("agent reading trucks: %v", err)
	}
	if err := e.c.Post(ctx, "/proposals", proposalFor(d.Delivery.ID, truck.ID), nil); status(err) != 403 {
		t.Errorf("dispatcher writing a proposal: %d, want 403", status(err))
	}
	var p struct {
		ID        string  `json:"id"`
		DecidedBy *string `json:"decided_by"`
		Status    string  `json:"status"`
	}
	if err := agent.Post(ctx, "/proposals", proposalFor(d.Delivery.ID, truck.ID), &p); err != nil {
		t.Fatalf("agent writing a proposal: %v", err)
	}
	if err := agent.Post(ctx, "/proposals/"+p.ID+"/approve", nil, nil); status(err) != 403 {
		t.Errorf("agent approving: %d, want 403", status(err))
	}
	if err := agent.AssignTruck(ctx, d.Delivery.ID, truck.ID); status(err) != 403 {
		t.Errorf("agent assigning directly: %d, want 403", status(err))
	}
	if err := e.c.Post(ctx, "/proposals/"+p.ID+"/approve", nil, &p); err != nil {
		t.Fatal(err)
	}
	if p.Status != "approved" || p.DecidedBy == nil || *p.DecidedBy != testEmail {
		t.Fatalf("decided_by %v, status %s; want %s", p.DecidedBy, p.Status, testEmail)
	}

	wrong := client.New(e.srv.URL).WithToken(strings.Repeat("x", len(e.agentKey)))
	if _, err := wrong.ListTrucks(ctx); status(err) != 401 {
		t.Errorf("wrong agent key: %d, want 401", status(err))
	}
}

func TestLoginLockout(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	c := client.New(e.srv.URL)
	if _, err := c.Login(ctx, "nobody@example.com", "whatever12345"); status(err) != 401 {
		t.Fatalf("unknown email: %d, want 401", status(err))
	}
	for i := 0; i < 5; i++ {
		if _, err := c.Login(ctx, testEmail, "wrong password"); status(err) != 401 {
			t.Fatalf("wrong password %d: %d, want 401", i, status(err))
		}
	}
	if _, err := c.Login(ctx, testEmail, testPassword); status(err) != 429 {
		t.Fatalf("right password after 5 failures: %d, want 429", status(err))
	}
}

func TestLogoutAndPINResetEndSessions(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	c := client.New(e.srv.URL)
	if _, err := c.Login(ctx, testEmail, testPassword); err != nil {
		t.Fatal(err)
	}
	if err := c.Post(ctx, "/auth/logout", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, "/auth/me", nil); status(err) != 401 {
		t.Fatalf("after logout: %d, want 401", status(err))
	}

	dc, _, d := e.newDriver(t, "Dayo", "+234 803 000 0003", "KF-30")
	if err := dc.Get(ctx, "/auth/me", nil); err != nil {
		t.Fatal(err)
	}
	if err := e.c.SetDriverPIN(ctx, d.ID, "9999"); err != nil {
		t.Fatal(err)
	}
	if err := dc.Get(ctx, "/auth/me", nil); status(err) != 401 {
		t.Fatalf("driver still signed in after a PIN reset: %d", status(err))
	}
	if _, err := client.New(e.srv.URL).DriverLogin(ctx, "+2348030000003", "4821"); status(err) != 401 {
		t.Fatalf("old PIN: %d, want 401", status(err))
	}
}

// A browser signed in by cookie must send JSON to change anything.
func TestCookieRequestsMustBeJSON(t *testing.T) {
	e := setup(t)
	post := func(contentType string) int {
		req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/trucks", strings.NewReader(`{"plate_number":"KF-40"}`))
		req.Header.Set("Content-Type", contentType)
		req.AddCookie(&http.Cookie{Name: "session", Value: e.c.Token()})
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if got := post("text/plain"); got != http.StatusUnsupportedMediaType {
		t.Fatalf("cookie + text/plain: %d, want 415", got)
	}
	if got := post("application/json"); got != http.StatusCreated {
		t.Fatalf("cookie + JSON: %d, want 201", got)
	}
}

func TestDriverPhoneDuplicates(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if _, err := e.c.CreateDriver(ctx, "Efe", "+234 803 000 0004"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.c.CreateDriver(ctx, "Efe again", "+2348030000004"); status(err) != 409 {
		t.Fatalf("same number, other spelling: %d, want 409", status(err))
	}
}

// A generated PIN signs the driver in; generating again retires it and the
// sessions made with it. Creating a driver can generate one too.
func TestGeneratedPIN(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	d, err := e.c.CreateDriver(ctx, "Femi", "+234 803 000 0005")
	if err != nil {
		t.Fatal(err)
	}
	first, err := e.c.GenerateDriverPIN(ctx, d.ID)
	if err != nil || len(first) != 6 {
		t.Fatalf("pin %q, err %v", first, err)
	}
	dc := client.New(e.srv.URL)
	if _, err := dc.DriverLogin(ctx, "+2348030000005", first); err != nil {
		t.Fatalf("sign in with the generated PIN: %v", err)
	}
	second, err := e.c.GenerateDriverPIN(ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Log("same PIN twice by chance (1 in a million)")
	}
	if err := dc.Get(ctx, "/auth/me", nil); status(err) != 401 {
		t.Fatalf("session from the old PIN still works: %d", status(err))
	}
	if second != first {
		if _, err := client.New(e.srv.URL).DriverLogin(ctx, "+2348030000005", first); status(err) != 401 {
			t.Fatalf("old PIN still signs in: %d", status(err))
		}
	}
	if _, err := client.New(e.srv.URL).DriverLogin(ctx, "+2348030000005", second); err != nil {
		t.Fatalf("new PIN: %v", err)
	}

	// Drivers can't generate PINs, not even their own.
	if _, err := dc.GenerateDriverPIN(ctx, d.ID); status(err) != 401 && status(err) != 403 {
		t.Fatalf("driver generating a PIN: %d", status(err))
	}
	if _, err := e.c.GenerateDriverPIN(ctx, "00000000-0000-0000-0000-000000000000"); status(err) != 404 {
		t.Fatalf("unknown driver: %d", status(err))
	}

	var created struct {
		ID  string `json:"id"`
		PIN string `json:"pin"`
	}
	if err := e.c.Post(ctx, "/drivers", map[string]any{"name": "Gbenga", "phone": "+2348030000006", "generate_pin": true}, &created); err != nil {
		t.Fatal(err)
	}
	if _, err := client.New(e.srv.URL).DriverLogin(ctx, "+2348030000006", created.PIN); err != nil || len(created.PIN) != 6 {
		t.Fatalf("PIN from create %q: %v", created.PIN, err)
	}
}

func nowUTC() time.Time { return time.Now().UTC() }
