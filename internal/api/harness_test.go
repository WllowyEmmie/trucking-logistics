package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/WllowyEmmie/trucking-logistics/internal/client"
)

func apiErr(t *testing.T, err error) *client.APIError {
	t.Helper()
	var ae *client.APIError
	if !errors.As(err, &ae) {
		t.Fatalf("want an API error, got %v", err)
	}
	return ae
}

// Every error leaves with a stable code; the ones a client acts on are
// specific.
func TestErrorsCarryCode(t *testing.T) {
	e := setup(t)
	ctx := context.Background()

	d, truck := e.newDelivery(t, "KF-40", "assigned")
	other, err := e.c.CreateDelivery(ctx, client.CreateDeliveryInput{CustomerName: "Bisi", Stops: twoStops})
	if err != nil {
		t.Fatal(err)
	}
	ae := apiErr(t, e.c.AssignTruck(ctx, other.Delivery.ID, truck.ID))
	if ae.Status != 409 || ae.Code != codeTruckBusy {
		t.Fatalf("busy truck: %d %q", ae.Status, ae.Code)
	}
	if err := e.c.CancelDelivery(ctx, d.Delivery.ID); err != nil {
		t.Fatal(err)
	}
	ae = apiErr(t, e.c.CancelDelivery(ctx, d.Delivery.ID))
	if ae.Code != codeWrongStatus {
		t.Fatalf("cancel twice: %q", ae.Code)
	}
	ae = apiErr(t, e.c.Get(ctx, "/deliveries/not-a-uuid", nil))
	if ae.Status != 400 || ae.Code != "bad_request" {
		t.Fatalf("bad id: %d %q", ae.Status, ae.Code)
	}
	ae = apiErr(t, client.New(e.srv.URL).Get(ctx, "/deliveries", nil))
	if ae.Status != 401 || ae.Code != "unauthenticated" {
		t.Fatalf("anonymous: %d %q", ae.Status, ae.Code)
	}
}

type eventRow struct {
	EventType string  `json:"event_type"`
	Note      *string `json:"note"`
	Actor     *string `json:"actor"`
	Via       *string `json:"via"`
	ThreadID  *string `json:"thread_id"`
}

func (e *env) events(t *testing.T, id string) []eventRow {
	t.Helper()
	var out struct {
		Events []eventRow `json:"events"`
	}
	if err := e.c.Get(context.Background(), "/deliveries/"+id, &out); err != nil {
		t.Fatal(err)
	}
	return out.Events
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// Delivery events record who made the change and through what.
func TestEventsRecordWhoAndHow(t *testing.T) {
	e := setup(t)
	ctx := context.Background()

	d, _ := e.newDelivery(t, "KF-41", "pending")
	copilot := e.c.WithHeader(headerVia, viaCopilot).WithHeader(headerThread, "thr_7Kq2")
	if err := copilot.Post(ctx, "/deliveries/"+d.Delivery.ID+"/cancel",
		map[string]string{"reason": "Customer called it off"}, nil); err != nil {
		t.Fatal(err)
	}
	evs := e.events(t, d.Delivery.ID)
	if len(evs) < 2 {
		t.Fatalf("events: %+v", evs)
	}
	created, cancelled := evs[0], evs[len(evs)-1]
	if str(created.Via) != viaConsole || str(created.Actor) != testEmail || created.ThreadID != nil {
		t.Errorf("created: via %q actor %q thread %v", str(created.Via), str(created.Actor), created.ThreadID)
	}
	if cancelled.EventType != "cancelled" || str(cancelled.Via) != viaCopilot ||
		str(cancelled.Actor) != testEmail || str(cancelled.ThreadID) != "thr_7Kq2" ||
		str(cancelled.Note) != "Customer called it off" {
		t.Errorf("cancelled: %+v via %q thread %q", cancelled, str(cancelled.Via), str(cancelled.ThreadID))
	}

	// A thread id that isn't one is dropped, not stored.
	d2, _ := e.newDelivery(t, "KF-42", "pending")
	bad := e.c.WithHeader(headerVia, viaCopilot).WithHeader(headerThread, "x'; drop table--")
	if err := bad.CancelDelivery(ctx, d2.Delivery.ID); err != nil {
		t.Fatal(err)
	}
	last := e.events(t, d2.Delivery.ID)
	if l := last[len(last)-1]; str(l.Via) != viaCopilot || l.ThreadID != nil {
		t.Errorf("bad thread: via %q thread %v", str(l.Via), l.ThreadID)
	}
}

// The same change sent twice with one key runs once; the key can't be
// reused for something else.
func TestIdempotencyKey(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	keyed := e.c.WithHeader(headerIdempotencyKey, "create-0001-abc")

	in := client.CreateDeliveryInput{CustomerName: "Tolu", Stops: twoStops}
	first, err := keyed.CreateDelivery(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	again, err := keyed.CreateDelivery(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if first.Delivery.ID != again.Delivery.ID {
		t.Fatalf("second try made a new delivery: %s vs %s", first.Delivery.ID, again.Delivery.ID)
	}
	list, err := e.c.ListDeliveries(ctx, "pending")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("%d deliveries, want 1", len(list))
	}

	// Same key, different body.
	in.CustomerName = "Someone else"
	ae := apiErr(t, func() error { _, err := keyed.CreateDelivery(ctx, in); return err }())
	if ae.Status != 422 || ae.Code != codeKeyReused {
		t.Fatalf("reused key: %d %q", ae.Status, ae.Code)
	}

	// A cancel retried gets the first answer (200), not "already cancelled".
	cancel := e.c.WithHeader(headerIdempotencyKey, "cancel-0001-abc")
	if err := cancel.CancelDelivery(ctx, first.Delivery.ID); err != nil {
		t.Fatal(err)
	}
	if err := cancel.CancelDelivery(ctx, first.Delivery.ID); err != nil {
		t.Fatalf("retried cancel: %v", err)
	}

	// Keys are per sender: another dispatcher's same key is theirs.
	// A malformed key is refused.
	ae = apiErr(t, e.c.WithHeader(headerIdempotencyKey, "short").CancelDelivery(ctx, first.Delivery.ID))
	if ae.Status != 400 {
		t.Fatalf("short key: %d", ae.Status)
	}

	// The replay is marked.
	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/deliveries/"+first.Delivery.ID+"/cancel", strings.NewReader(""))
	req.Header.Set("Authorization", "Bearer "+e.c.Token())
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(headerIdempotencyKey, "cancel-0001-abc")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 || res.Header.Get(headerReplayed) != "true" {
		t.Fatalf("replay: %d %q", res.StatusCode, res.Header.Get(headerReplayed))
	}
}

// A signed-in caller over their limit gets 429 rate_limited with
// Retry-After; others are unaffected.
func TestStaffRateLimit(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.s.limits.read[roleDispatcher] = limit{per: 60, burst: 3}

	for i := 0; i < 3; i++ {
		if _, err := e.c.ListTrucks(ctx); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	ae := apiErr(t, func() error { _, err := e.c.ListTrucks(ctx); return err }())
	if ae.Status != 429 || ae.Code != "rate_limited" {
		t.Fatalf("over the limit: %d %q", ae.Status, ae.Code)
	}
	// Writes have their own bucket; the agent its own.
	if _, err := e.c.CreateTruck(ctx, "KF-50", nil); err != nil {
		t.Fatalf("write after reads ran out: %v", err)
	}
	agent := client.New(e.srv.URL).WithToken(e.agentKey)
	if err := agent.Get(ctx, "/trucks", nil); err != nil {
		t.Fatalf("agent: %v", err)
	}
}

func TestLimiterRefills(t *testing.T) {
	l := newLimiter()
	lim := limit{per: 60, burst: 1} // one a second
	now := time.Now()
	if ok, _ := l.take("k", lim, now); !ok {
		t.Fatal("first")
	}
	ok, wait := l.take("k", lim, now)
	if ok || wait <= 0 || wait > time.Second {
		t.Fatalf("second: ok=%v wait=%v", ok, wait)
	}
	if ok, _ := l.take("k", lim, now.Add(1100*time.Millisecond)); !ok {
		t.Fatal("after a second")
	}
}
