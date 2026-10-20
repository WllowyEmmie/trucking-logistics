package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/WllowyEmmie/trucking-logistics/internal/client"
)

// A driver with a PIN and no truck, signed in.
func (e *env) looseDriver(t *testing.T, name, phone string) (*client.Client, client.Driver) {
	t.Helper()
	ctx := context.Background()
	d, err := e.c.CreateDriver(ctx, name, phone)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.c.SetDriverPIN(ctx, d.ID, "4821"); err != nil {
		t.Fatal(err)
	}
	dc := client.New(e.srv.URL)
	if _, err := dc.DriverLogin(ctx, phone, "4821"); err != nil {
		t.Fatal(err)
	}
	return dc, d
}

func (e *env) autoMode(on bool) {
	st := *e.s.st()
	st.AutoAssign = on
	e.s.settings.Store(&st)
}

// Auto mode: the agent gives a driver on shift a free truck, then that
// truck a pending delivery. Everything it must not do is refused, and a
// refusal leaves nothing half done.
func TestSupervisorPairsAndAssigns(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	agent := client.New(e.srv.URL).WithToken(e.agentKey)
	pair := func(driverID, truckID string) error {
		return agent.Post(ctx, "/supervisor/pair", map[string]string{
			"driver_id": driverID, "truck_id": truckID, "reason": "on shift with no truck", "decided_by": "rule"}, nil)
	}
	assign := func(deliveryID, truckID string) error {
		return agent.Post(ctx, "/supervisor/assign", map[string]string{
			"delivery_id": deliveryID, "truck_id": truckID, "reason": "0.4 km from the pickup", "decided_by": "model"}, nil)
	}

	dc, musa := e.looseDriver(t, "Musa", "+2348030000101")
	truck, err := e.c.CreateTruck(ctx, "KF-30", nil)
	if err != nil {
		t.Fatal(err)
	}
	other, err := e.c.CreateTruck(ctx, "KF-31", nil)
	if err != nil {
		t.Fatal(err)
	}
	d, err := e.c.CreateDelivery(ctx, client.CreateDeliveryInput{CustomerName: "Chidi", Stops: twoStops})
	if err != nil {
		t.Fatal(err)
	}

	if got := status(pair(musa.ID, truck.ID)); got != 409 {
		t.Fatalf("auto mode off: %d, want 409", got)
	}
	e.autoMode(true)
	// Signing in started Musa's shift; off shift, he gets nothing.
	if err := dc.Post(ctx, "/auth/me/shift", map[string]bool{"on": false}, nil); err != nil {
		t.Fatal(err)
	}
	if got := status(pair(musa.ID, truck.ID)); got != 409 {
		t.Fatalf("driver off shift: %d, want 409", got)
	}
	if err := dc.Post(ctx, "/auth/me/shift", map[string]bool{"on": true}, nil); err != nil {
		t.Fatal(err)
	}
	if got := status(e.c.Post(ctx, "/supervisor/pair", map[string]string{
		"driver_id": musa.ID, "truck_id": truck.ID, "reason": "x", "decided_by": "rule"}, nil)); got != 403 {
		t.Fatalf("a dispatcher using the agent's endpoint: %d, want 403", got)
	}
	if got := status(agent.Post(ctx, "/supervisor/pair", map[string]string{
		"driver_id": musa.ID, "truck_id": truck.ID, "reason": "", "decided_by": "rule"}, nil)); got != 400 {
		t.Fatalf("no reason: %d, want 400", got)
	}
	if err := pair(musa.ID, truck.ID); err != nil {
		t.Fatalf("pair: %v", err)
	}
	if got := status(pair(musa.ID, other.ID)); got != 409 {
		t.Fatalf("second truck for one driver: %d, want 409", got)
	}
	var me struct {
		TruckID *string `json:"truck_id"`
	}
	if err := dc.Get(ctx, "/auth/me", &me); err != nil || me.TruckID == nil || *me.TruckID != truck.ID {
		t.Fatalf("driver's truck after pairing: %+v %v", me, err)
	}

	if err := assign(d.Delivery.ID, truck.ID); err != nil {
		t.Fatalf("assign: %v", err)
	}
	if got := status(assign(d.Delivery.ID, truck.ID)); got != 409 {
		t.Fatalf("assign twice: %d, want 409", got)
	}
	detail, err := e.c.GetDelivery(ctx, d.Delivery.ID)
	if err != nil || detail.Delivery.Status != "assigned" {
		t.Fatalf("after assign: %+v %v", detail.Delivery, err)
	}

	// A truck whose driver is off shift: refused, and the delivery stays pending.
	_, d2 := e.newDeliveryPending(t)
	lazy := e.truckWithDriver(t, "KF-32")
	if got := status(assign(d2, lazy.ID)); got != 409 {
		t.Fatalf("driver off shift: %d, want 409", got)
	}
	if detail, err := e.c.GetDelivery(ctx, d2); err != nil || detail.Delivery.Status != "pending" {
		t.Fatalf("refused assign left %+v %v", detail.Delivery, err)
	}

	var actions []struct {
		Kind      string `json:"kind"`
		Summary   string `json:"summary"`
		DecidedBy string `json:"decided_by"`
	}
	if err := e.c.Get(ctx, "/supervisor/actions", &actions); err != nil || len(actions) != 2 {
		t.Fatalf("actions: %+v %v", actions, err)
	}
	if actions[0].Kind != "assign" || !strings.Contains(actions[0].Summary, "KF-30 (Musa)") || actions[0].DecidedBy != "model" ||
		actions[1].Kind != "pair" || actions[1].Summary != "Gave KF-30 to Musa" {
		t.Fatalf("actions: %+v", actions)
	}

	// Switching auto mode off stops it at once.
	e.autoMode(false)
	if got := status(assign(d2, other.ID)); got != 409 {
		t.Fatalf("after switching off: %d, want 409", got)
	}
}

func (e *env) newDeliveryPending(t *testing.T) (client.CreateDeliveryResult, string) {
	t.Helper()
	d, err := e.c.CreateDelivery(context.Background(), client.CreateDeliveryInput{CustomerName: "Ngozi", Stops: twoStops})
	if err != nil {
		t.Fatal(err)
	}
	return d, d.Delivery.ID
}

// The agent listens on the socket (no Origin: it isn't a browser) and hears
// what changed, so auto mode reacts within seconds.
func TestAgentHearsChanges(t *testing.T) {
	e := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(e.srv.URL, "http") + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: http.Header{
		"Authorization": {"Bearer " + e.agentKey}}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if _, _, err := conn.Read(ctx); err != nil { // snapshot
		t.Fatal(err)
	}
	if _, err := e.c.CreateDelivery(ctx, client.CreateDeliveryInput{CustomerName: "Chidi", Stops: twoStops}); err != nil {
		t.Fatal(err)
	}
	_, b, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var m wsMessage
	if err := json.Unmarshal(b, &m); err != nil || m.Type != "changed" || len(m.Topics) == 0 || m.Topics[0] != "deliveries" {
		t.Fatalf("frame: %s %v", b, err)
	}
}

// Only the driver starts a shift, unless the API allows it for demos.
func TestDispatchShiftStartIsOptIn(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	_, d := e.looseDriver(t, "Ife", "+2348030000102")
	if got := status(e.c.Post(ctx, "/drivers/"+d.ID+"/shift/start", nil, nil)); got != 403 {
		t.Fatalf("default: %d, want 403", got)
	}
	e.s.cfg.AllowDispatchShiftStart = true
	if err := e.c.Post(ctx, "/drivers/"+d.ID+"/shift/start", nil, nil); err != nil {
		t.Fatalf("allowed: %v", err)
	}
}

// Signing in to the driver app starts the shift, and says so in the answer.
func TestDriverSignInStartsShift(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	d, err := e.c.CreateDriver(ctx, "Tobi", "+2348030000103")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.c.SetDriverPIN(ctx, d.ID, "4821"); err != nil {
		t.Fatal(err)
	}
	var me struct {
		OnShift *bool `json:"on_shift"`
	}
	if err := client.New(e.srv.URL).Post(ctx, "/auth/driver-login", map[string]string{"phone": "+2348030000103", "pin": "4821"}, &me); err != nil {
		t.Fatal(err)
	}
	if me.OnShift == nil || !*me.OnShift {
		t.Fatalf("sign-in answer: %+v, want on shift", me)
	}
}
