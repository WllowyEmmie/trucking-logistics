package api

import (
	"context"
	"testing"
	"time"

	"github.com/WllowyEmmie/trucking-logistics/internal/client"
)

type historyPage struct {
	Items []struct {
		Reference       string  `json:"reference"`
		Status          string  `json:"status"`
		Failures        int     `json:"failures"`
		Reopens         int     `json:"reopens"`
		FirstFailReason *string `json:"first_fail_reason"`
		Plate           *string `json:"plate"`
		Late            bool    `json:"late"`
	} `json:"items"`
	Next *string `json:"next"`
}

func finish(t *testing.T, e *env, d client.CreateDeliveryResult) {
	t.Helper()
	ctx := context.Background()
	for _, s := range d.Stops {
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

// History lists finished deliveries and how they got there; the console
// list (?board=1) keeps open and failed ones and drops nothing recent.
func TestHistoryAndBoard(t *testing.T) {
	e := setup(t)
	ctx := context.Background()

	done, _ := e.newDelivery(t, "KF-90", "in_transit")
	finish(t, e, done)

	// Failed after collecting, handed over, then completed.
	rec, _ := e.failAfterPickup(t, "KF-91", "breakdown")
	other := e.truckWithDriver(t, "KF-92")
	if err := e.reopen(t, rec.Delivery.ID, "handover", other.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.c.StartDelivery(ctx, rec.Delivery.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := e.c.GetDelivery(ctx, rec.Delivery.ID)
	for _, s := range got.Stops[1:] {
		e.c.ArriveAtStop(ctx, rec.Delivery.ID, s.ID)
		e.c.CompleteStop(ctx, rec.Delivery.ID, s.ID)
	}
	if err := e.c.CompleteDelivery(ctx, rec.Delivery.ID); err != nil {
		t.Fatal(err)
	}

	failed, _ := e.failAfterPickup(t, "KF-93", "customer_unavailable")
	gone, _ := e.newDelivery(t, "KF-94", "in_transit")
	if err := e.c.CancelDelivery(ctx, gone.Delivery.ID); err != nil {
		t.Fatalf("cancel in transit: %v", err)
	}
	open, _ := e.newDelivery(t, "KF-95", "pending")

	var all historyPage
	if err := e.c.Get(ctx, "/deliveries/history", &all); err != nil {
		t.Fatal(err)
	}
	if len(all.Items) != 4 {
		t.Fatalf("history has %d rows, want 4 (not the pending one)", len(all.Items))
	}
	var recovered historyPage
	if err := e.c.Get(ctx, "/deliveries/history?outcome=recovered", &recovered); err != nil {
		t.Fatal(err)
	}
	if len(recovered.Items) != 1 || recovered.Items[0].Reference != rec.Delivery.Reference || recovered.Items[0].Failures != 1 ||
		recovered.Items[0].Reopens != 1 || *recovered.Items[0].FirstFailReason != "breakdown" || *recovered.Items[0].Plate != "KF-92" {
		t.Fatalf("recovered: %+v", recovered.Items)
	}

	// Paging: two at a time, no repeats, no gaps.
	seen := map[string]bool{}
	path := "/deliveries/history?limit=2"
	for pages := 0; ; pages++ {
		var p historyPage
		if err := e.c.Get(ctx, path, &p); err != nil {
			t.Fatal(err)
		}
		for _, it := range p.Items {
			if seen[it.Reference] {
				t.Fatalf("%s twice", it.Reference)
			}
			seen[it.Reference] = true
		}
		if p.Next == nil {
			break
		}
		path = "/deliveries/history?limit=2&before=" + *p.Next
		if pages > 3 {
			t.Fatal("too many pages")
		}
	}
	if len(seen) != 4 {
		t.Fatalf("paged %d rows, want 4", len(seen))
	}

	// Search and the date window.
	var q historyPage
	if err := e.c.Get(ctx, "/deliveries/history?q="+failed.Delivery.Reference, &q); err != nil || len(q.Items) != 1 {
		t.Fatalf("search: %v %+v", err, q.Items)
	}
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	if err := e.c.Get(ctx, "/deliveries/history?from="+future, &q); err != nil || len(q.Items) != 0 {
		t.Fatalf("from the future: %v %d", err, len(q.Items))
	}
	if err := e.c.Get(ctx, "/deliveries/history?outcome=sideways", nil); status(err) != 400 {
		t.Fatalf("bad outcome: %v", err)
	}

	// The board: everything here finished just now, so all of it is on it.
	var board []struct {
		ID         string     `json:"id"`
		FinishedAt *time.Time `json:"finished_at"`
	}
	if err := e.c.Get(ctx, "/deliveries?board=1", &board); err != nil || len(board) != 5 {
		t.Fatalf("board: %v, %d rows", err, len(board))
	}
	// Age everything past the window: only the open and the failed stay.
	if _, err := e.pool.Exec(ctx, `UPDATE delivery_events SET created_at = created_at - interval '2 days'`); err != nil {
		t.Fatal(err)
	}
	if err := e.c.Get(ctx, "/deliveries?board=1", &board); err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, b := range board {
		ids[b.ID] = true
	}
	if len(board) != 2 || !ids[open.Delivery.ID] || !ids[failed.Delivery.ID] {
		t.Fatalf("aged board: %+v", board)
	}

	// A failed delivery can be closed (cancelled) without touching its truck.
	if err := e.c.CancelDelivery(ctx, failed.Delivery.ID); err != nil {
		t.Fatalf("cancel failed: %v", err)
	}
}
