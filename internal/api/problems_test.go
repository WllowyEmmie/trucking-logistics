package api

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/WllowyEmmie/trucking-logistics/internal/client"
	"github.com/WllowyEmmie/trucking-logistics/internal/db"
)

func TestFindProblemsRules(t *testing.T) {
	now := time.Date(2026, 10, 23, 15, 0, 0, 0, time.UTC)
	ago := func(m int) *time.Time { t := now.Add(-time.Duration(m) * time.Minute); return &t }
	in := func(m int) *time.Time { t := now.Add(time.Duration(m) * time.Minute); return &t }
	truckA, truckB, truckC := uuid.New(), uuid.New(), uuid.New()
	musa, ngozi, tunde := uuid.New(), uuid.New(), uuid.New()
	item := func(ref, status string, truck *uuid.UUID, created *time.Time, due, eta, finished *time.Time) deliveryItem {
		return deliveryItem{DeliveryItem: db.DeliveryItem{ID: uuid.New(), Reference: ref, Status: status, TruckID: truck,
			CreatedAt: *created, DueBy: due, FinishedAt: finished}, ETA: eta}
	}
	late := item("D-LATE", "in_transit", &truckA, ago(120), ago(15), in(10), nil)
	risk := item("D-RISK", "assigned", &truckB, ago(10), in(20), in(45), nil)
	fine := item("D-FINE", "in_transit", &truckA, ago(30), in(60), in(20), nil)
	wait := item("D-WAIT", "pending", nil, ago(45), nil, nil, nil)
	fresh := item("D-NEW", "pending", nil, ago(5), nil, nil, nil)
	failed := item("D-FAIL", "failed", nil, ago(300), nil, nil, ago(90))
	out := findProblems(problemInputs{
		Now:        now,
		Deliveries: []deliveryItem{late, risk, fine, wait, fresh, failed},
		AssignedAt: map[uuid.UUID]time.Time{risk.ID: *ago(40)},
		Trucks: []db.TruckWithPosition{
			{ID: truckA, PlateNumber: "KF-01", Status: "on_delivery", DriverID: &musa, LastSeenAt: ago(25), DriverOnShift: true},
			{ID: truckB, PlateNumber: "KF-02", Status: "on_delivery", DriverID: &ngozi, LastSeenAt: ago(2), DriverOnShift: true},
			{ID: truckC, PlateNumber: "KF-03", Status: "maintenance", LastSeenAt: ago(600)},
		},
		Drivers: []db.ListDriversRow{{ID: musa, Name: "Musa"}, {ID: ngozi, Name: "Ngozi"}, {ID: tunde, Name: "Tunde"}},
		OnShift: map[uuid.UUID]time.Time{musa: *ago(300), ngozi: *ago(200), tunde: *ago(50)},
	})
	kinds := map[string]int{}
	for _, p := range out.Problems {
		kinds[p.Kind+":"+p.Reference+p.Plate+p.Driver]++
	}
	want := []string{"late:D-LATEKF-01", "at_risk:D-RISKKF-02", "not_started:D-RISKKF-02", "waiting:D-WAIT",
		"failed:D-FAIL", "silent_truck:KF-01Musa", "driver_without_truck:Tunde"}
	for _, k := range want {
		if kinds[k] != 1 {
			t.Errorf("missing %s in %v", k, kinds)
		}
	}
	if out.Count != len(want) {
		t.Errorf("%d problems, want %d: %v", out.Count, len(want), kinds)
	}
	if out.Problems[0].Kind != "late" || out.Problems[0].Minutes != 15 {
		t.Errorf("worst first: %+v", out.Problems[0])
	}
}

func TestProblemsEndpoint(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	d, err := e.c.CreateDelivery(ctx, client.CreateDeliveryInput{CustomerName: "Late Ltd", Stops: twoStops})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE deliveries SET created_at = now() - interval '2 hours', due_by = now() - interval '10 minutes' WHERE id = $1`, d.Delivery.ID); err != nil {
		t.Fatal(err)
	}
	var out struct {
		Count    int `json:"count"`
		Problems []struct {
			Kind      string `json:"kind"`
			Reference string `json:"reference"`
		} `json:"problems"`
	}
	if err := e.c.Get(ctx, "/problems", &out); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, p := range out.Problems {
		if p.Reference == d.Delivery.Reference {
			got[p.Kind] = true
		}
	}
	if !got["late"] || !got["waiting"] {
		t.Fatalf("problems: %+v", out)
	}
}

func TestReassign(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	d, from := e.newDelivery(t, "RA-01", "assigned")
	to := e.truckWithDriver(t, "RA-02")
	reassign := func(id, truck string) error {
		return e.c.Post(ctx, "/deliveries/"+id+"/reassign", map[string]string{"truck_id": truck}, nil)
	}
	if err := reassign(d.Delivery.ID, to.ID); err != nil {
		t.Fatal(err)
	}
	trucks, _ := e.c.ListTrucks(ctx)
	st := map[string]string{}
	for _, tr := range trucks {
		st[tr.ID] = tr.Status
	}
	if st[from.ID] != "available" || st[to.ID] != "on_delivery" {
		t.Fatalf("trucks after reassign: from %s, to %s", st[from.ID], st[to.ID])
	}
	detail, _ := e.c.GetDelivery(ctx, d.Delivery.ID)
	if detail.Delivery.Status != "assigned" || detail.Delivery.TruckID == nil || *detail.Delivery.TruckID != to.ID {
		t.Fatalf("delivery after reassign: %+v", detail.Delivery)
	}
	evs := e.events(t, d.Delivery.ID)
	if l := evs[len(evs)-1]; l.EventType != "reassigned" || str(l.Note) != "from RA-01; assigned to RA-02" {
		t.Fatalf("event: %+v %q", l, str(l.Note))
	}

	// Refusals: same truck, a busy truck, a pending or started delivery.
	if ae := apiErr(t, reassign(d.Delivery.ID, to.ID)); ae.Code != codeWrongStatus {
		t.Fatalf("same truck: %q", ae.Code)
	}
	other, busy := e.newDelivery(t, "RA-03", "assigned")
	if ae := apiErr(t, reassign(d.Delivery.ID, busy.ID)); ae.Code != codeTruckBusy {
		t.Fatalf("busy truck: %q", ae.Code)
	}
	pending, _ := e.newDelivery(t, "RA-04", "pending")
	if ae := apiErr(t, reassign(pending.Delivery.ID, from.ID)); ae.Code != codeWrongStatus {
		t.Fatalf("pending: %q", ae.Code)
	}
	if err := e.c.StartDelivery(ctx, other.Delivery.ID); err != nil {
		t.Fatal(err)
	}
	if ae := apiErr(t, reassign(other.Delivery.ID, from.ID)); ae.Code != codeWrongStatus {
		t.Fatalf("started: %q", ae.Code)
	}
}
