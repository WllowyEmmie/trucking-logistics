package api

import (
	"context"
	"testing"
	"time"

	"github.com/WllowyEmmie/trucking-logistics/internal/client"
)

// A truck needs a driver to be given a delivery, and keeps it while on one.
func TestAssignNeedsADriver(t *testing.T) {
	e := setup(t)
	ctx := context.Background()

	bare, err := e.c.CreateTruck(ctx, "KF-50", nil)
	if err != nil {
		t.Fatal(err)
	}
	d, err := e.c.CreateDelivery(ctx, client.CreateDeliveryInput{CustomerName: "Chidi", Stops: twoStops})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.c.AssignTruck(ctx, d.Delivery.ID, bare.ID); status(err) != 409 {
		t.Fatalf("assign to a truck with no driver: want 409, got %v", err)
	}

	// Giving it a driver makes it assignable.
	drv, err := e.c.CreateDriver(ctx, "Tunde", "+2348090000001")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.c.UpdateTruck(ctx, bare.ID, &drv.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err := e.c.AssignTruck(ctx, d.Delivery.ID, bare.ID); err != nil {
		t.Fatalf("assign after adding a driver: %v", err)
	}

	// On a delivery: the driver can be swapped but not removed.
	other, err := e.c.CreateDriver(ctx, "Bola", "+2348090000002")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.c.UpdateTruck(ctx, bare.ID, &other.ID, nil); err != nil {
		t.Fatalf("swap driver while on delivery: %v", err)
	}
	none := ""
	if err := e.c.UpdateTruck(ctx, bare.ID, &none, nil); status(err) != 409 {
		t.Fatalf("remove driver while on delivery: want 409, got %v", err)
	}

	// Cancelling frees the truck.
	if err := e.c.CancelDelivery(ctx, d.Delivery.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.c.UpdateTruck(ctx, bare.ID, &none, nil); err != nil {
		t.Fatalf("remove driver after cancel: %v", err)
	}
}

func (e *env) reopen(t *testing.T, id, mode, truckID string) error {
	t.Helper()
	return e.c.Post(context.Background(), "/deliveries/"+id+"/reopen", map[string]string{"mode": mode, "truck_id": truckID}, nil)
}

// failAfterPickup: a delivery collected at stop 1, then failed for reason.
func (e *env) failAfterPickup(t *testing.T, plate, reason string) (client.CreateDeliveryResult, client.Truck) {
	t.Helper()
	ctx := context.Background()
	d, truck := e.newDelivery(t, plate, "in_transit")
	lat, lng := 6.5, 3.36
	if err := e.c.PostLocations(ctx, truck.ID, []client.Ping{{Lat: lat, Lng: lng, RecordedAt: nowUTC()}}); err != nil {
		t.Fatal(err)
	}
	if err := e.c.ArriveAtStop(ctx, d.Delivery.ID, d.Stops[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := e.c.CompleteStop(ctx, d.Delivery.ID, d.Stops[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := e.c.FailDelivery(ctx, d.Delivery.ID, reason); err != nil {
		t.Fatal(err)
	}
	return d, truck
}

func TestHandoverAfterBreakdown(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	d, broken := e.failAfterPickup(t, "KF-60", "breakdown")
	other := e.truckWithDriver(t, "KF-61")

	// The goods are on KF-60: plain reassigning to another truck is refused,
	// and so is sending KF-60 (in the workshop). Neither changes anything.
	if err := e.reopen(t, d.Delivery.ID, "resume", other.ID); status(err) != 409 {
		t.Fatalf("resume on another truck: want 409, got %v", err)
	}
	if err := e.reopen(t, d.Delivery.ID, "resume", broken.ID); status(err) != 409 {
		t.Fatalf("resume on the broken truck: want 409, got %v", err)
	}
	if got, _ := e.c.GetDelivery(ctx, d.Delivery.ID); got.Delivery.Status != "failed" || len(got.Stops) != 2 {
		t.Fatalf("a refused reopen changed it: %s, %d stops", got.Delivery.Status, len(got.Stops))
	}

	if err := e.reopen(t, d.Delivery.ID, "handover", other.ID); err != nil {
		t.Fatal(err)
	}
	got, err := e.c.GetDelivery(ctx, d.Delivery.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Delivery.Status != "assigned" || *got.Delivery.TruckID != other.ID {
		t.Fatalf("after handover: %s on %v", got.Delivery.Status, got.Delivery.TruckID)
	}
	// Collected pickup kept, handover pickup at KF-60's position, then the drop-off.
	if len(got.Stops) != 3 {
		t.Fatalf("want 3 stops, got %+v", got.Stops)
	}
	st := got.Stops
	if st[0].Status != "completed" || st[1].StopType != "pickup" || st[1].Status != "pending" ||
		st[1].Address != "Handover from KF-60" || *st[1].Lat != 6.5 || st[2].StopType != "dropoff" || st[2].Status != "pending" {
		t.Fatalf("stops after handover: %+v %+v %+v", st[0], st[1], st[2])
	}

	// And it can be finished: start, both open stops, complete.
	if err := e.c.StartDelivery(ctx, d.Delivery.ID); err != nil {
		t.Fatal(err)
	}
	for _, s := range st[1:] {
		if err := e.c.ArriveAtStop(ctx, d.Delivery.ID, s.ID); err != nil {
			t.Fatal(err)
		}
		if err := e.c.CompleteStop(ctx, d.Delivery.ID, s.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.c.CompleteDelivery(ctx, d.Delivery.ID); err != nil {
		t.Fatal(err)
	}
}

func TestTryAgainSameTruck(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	d, truck := e.failAfterPickup(t, "KF-62", "customer_unavailable")
	if err := e.reopen(t, d.Delivery.ID, "handover", truck.ID); status(err) != 409 {
		t.Fatalf("handover to the same truck: want 409, got %v", err)
	}
	if err := e.reopen(t, d.Delivery.ID, "resume", truck.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := e.c.GetDelivery(ctx, d.Delivery.ID)
	if got.Delivery.Status != "assigned" || got.Stops[0].Status != "completed" || got.Stops[1].Status != "pending" {
		t.Fatalf("after try again: %s, stops %s/%s", got.Delivery.Status, got.Stops[0].Status, got.Stops[1].Status)
	}
}

func TestRestartAndReassign(t *testing.T) {
	e := setup(t)
	ctx := context.Background()

	// Damaged goods: start over from the pickup on any truck, every stop again.
	d, _ := e.failAfterPickup(t, "KF-63", "goods_damaged")
	other := e.truckWithDriver(t, "KF-64")
	if err := e.reopen(t, d.Delivery.ID, "restart", other.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := e.c.GetDelivery(ctx, d.Delivery.ID)
	for _, s := range got.Stops {
		if s.Status != "pending" || s.ArrivedAt != nil || s.CompletedAt != nil {
			t.Fatalf("restart left stop %d %s", s.Sequence, s.Status)
		}
	}

	// Failed before collecting: nothing to hand over; any truck can take it.
	d2, _ := e.newDelivery(t, "KF-65", "in_transit")
	if err := e.c.FailDelivery(ctx, d2.Delivery.ID, "breakdown"); err != nil {
		t.Fatal(err)
	}
	third := e.truckWithDriver(t, "KF-66")
	if err := e.reopen(t, d2.Delivery.ID, "handover", third.ID); status(err) != 409 {
		t.Fatalf("handover with nothing collected: want 409, got %v", err)
	}
	if err := e.reopen(t, d2.Delivery.ID, "resume", third.ID); err != nil {
		t.Fatal(err)
	}
	// Only failed deliveries.
	if err := e.reopen(t, d2.Delivery.ID, "resume", third.ID); status(err) != 409 {
		t.Fatalf("reopen an assigned delivery: want 409, got %v", err)
	}
	if err := e.reopen(t, d2.Delivery.ID, "sideways", third.ID); status(err) != 400 {
		t.Fatalf("bad mode: want 400, got %v", err)
	}
}

// The agent can suggest how a failed delivery goes out again; approving runs
// the same rules as the console's buttons.
func TestAgentProposesReopen(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	d, _ := e.failAfterPickup(t, "KF-70", "breakdown")
	other := e.truckWithDriver(t, "KF-71")
	agent := client.New(e.srv.URL).WithToken(e.agentKey)

	reopenProposal := func(mode string) map[string]any {
		return map[string]any{
			"kind": "change", "title": "Hand over", "why": "KF-70 broke down after collecting",
			"delivery_id": d.Delivery.ID, "truck_id": other.ID,
			"action": map[string]string{"type": "reopen", "mode": mode, "delivery_id": d.Delivery.ID, "truck_id": other.ID},
		}
	}
	if err := agent.Post(ctx, "/proposals", reopenProposal("teleport"), nil); status(err) != 400 {
		t.Fatalf("bad mode: want 400, got %v", err)
	}

	// Resume onto another truck breaks the rules: approving it expires it.
	var bad struct{ ID string }
	if err := agent.Post(ctx, "/proposals", reopenProposal("resume"), &bad); err != nil {
		t.Fatal(err)
	}
	if err := e.c.Post(ctx, "/proposals/"+bad.ID+"/approve", nil, nil); status(err) != 409 {
		t.Fatalf("approving a resume onto another truck: want 409, got %v", err)
	}

	var p struct{ ID, Status string }
	if err := agent.Post(ctx, "/proposals", reopenProposal("handover"), &p); err != nil {
		t.Fatal(err)
	}
	if err := e.c.Post(ctx, "/proposals/"+p.ID+"/approve", nil, &p); err != nil {
		t.Fatal(err)
	}
	got, _ := e.c.GetDelivery(ctx, d.Delivery.ID)
	if p.Status != "approved" || got.Delivery.Status != "assigned" || len(got.Stops) != 3 || got.Stops[1].Address != "Handover from KF-70" {
		t.Fatalf("after approving: proposal %s, delivery %s, %d stops", p.Status, got.Delivery.Status, len(got.Stops))
	}
}

// Due times are kept and changed with an event; an assigned delivery whose
// truck has a position gets an estimated arrival in the list, the detail
// and on the customer's page.
func TestDueAndETA(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	due := time.Now().Add(3 * time.Hour).UTC().Truncate(time.Second)

	var created client.CreateDeliveryResult
	if err := e.c.Post(ctx, "/deliveries", map[string]any{"customer_name": "Ada", "stops": twoStops, "due_by": due}, &created); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-2 * time.Hour)
	if err := e.c.Post(ctx, "/deliveries", map[string]any{"customer_name": "Ada", "stops": twoStops, "due_by": past}, nil); status(err) != 400 {
		t.Fatalf("due in the past: want 400, got %v", err)
	}

	truck := e.truckWithDriver(t, "KF-80")
	if err := e.c.PostLocations(ctx, truck.ID, []client.Ping{{Lat: 6.40, Lng: 3.36, RecordedAt: nowUTC()}}); err != nil {
		t.Fatal(err)
	}
	if err := e.c.AssignTruck(ctx, created.Delivery.ID, truck.ID); err != nil {
		t.Fatal(err)
	}

	var list []struct {
		ID    string     `json:"id"`
		DueBy *time.Time `json:"due_by"`
		ETA   *time.Time `json:"eta"`
	}
	if err := e.c.Get(ctx, "/deliveries", &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].DueBy == nil || !list[0].DueBy.Equal(due) || list[0].ETA == nil {
		t.Fatalf("list row: %+v", list)
	}
	// Apapa from 6.40: ~5 km, then ~18 km to Ikeja; at 25 km/h x1.3 plus a
	// 10 min stop that's around an hour and a quarter.
	if mins := time.Until(*list[0].ETA).Minutes(); mins < 50 || mins > 110 {
		t.Fatalf("ETA in %.0f min, want about 75", mins)
	}

	var detail struct {
		Delivery struct {
			DueBy *time.Time `json:"due_by"`
		} `json:"delivery"`
		ETA *struct {
			Arrival time.Time `json:"arrival"`
			Stops   []struct {
				StopID string    `json:"stop_id"`
				At     time.Time `json:"at"`
			} `json:"stops"`
		} `json:"eta"`
	}
	if err := e.c.Get(ctx, "/deliveries/"+created.Delivery.ID, &detail); err != nil {
		t.Fatal(err)
	}
	if detail.ETA == nil || len(detail.ETA.Stops) != 2 || !detail.ETA.Arrival.Equal(*list[0].ETA) || detail.Delivery.DueBy == nil {
		t.Fatalf("detail eta: %+v", detail.ETA)
	}

	var track struct {
		DueBy *time.Time `json:"due_by"`
		ETA   *time.Time `json:"eta"`
	}
	if err := e.c.Get(ctx, "/track/"+created.Delivery.TrackingToken, &track); err != nil {
		t.Fatal(err)
	}
	if track.ETA == nil || track.DueBy == nil {
		t.Fatalf("tracking: %+v", track)
	}

	// Change it, clear it; not on a finished delivery.
	later := due.Add(time.Hour)
	if err := e.c.Post(ctx, "/deliveries/"+created.Delivery.ID+"/due", map[string]any{"due_by": later}, nil); err != nil {
		t.Fatal(err)
	}
	if err := e.c.Post(ctx, "/deliveries/"+created.Delivery.ID+"/due", map[string]any{"due_by": nil}, nil); err != nil {
		t.Fatal(err)
	}
	if err := e.c.Get(ctx, "/deliveries/"+created.Delivery.ID, &detail); err != nil || detail.Delivery.DueBy != nil {
		t.Fatalf("after clearing: %v %v", err, detail.Delivery.DueBy)
	}
	if err := e.c.CancelDelivery(ctx, created.Delivery.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.c.Post(ctx, "/deliveries/"+created.Delivery.ID+"/due", map[string]any{"due_by": later}, nil); status(err) != 409 {
		t.Fatalf("due on a cancelled delivery: want 409, got %v", err)
	}
}
