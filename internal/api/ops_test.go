package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/WllowyEmmie/trucking-logistics/internal/client"
)

// ---- Health and metrics ----

func TestHealthReportsDatabaseAndAgent(t *testing.T) {
	e := setup(t)
	res, err := http.Get(e.srv.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var rep healthReport
	json.NewDecoder(res.Body).Decode(&rep)
	if res.StatusCode != 200 || rep.Status != "ok" || rep.DB != "ok" || rep.Agent != "off" {
		t.Fatalf("health: %d %+v", res.StatusCode, rep)
	}
}

func TestMetricsNeedAuthAndCountRequests(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if _, err := e.c.ListTrucks(ctx); err != nil {
		t.Fatal(err)
	}
	get := func(token string) (int, string) {
		req, _ := http.NewRequest(http.MethodGet, e.srv.URL+"/metrics", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	if code, _ := get(""); code != 401 {
		t.Fatalf("anonymous: %d", code)
	}
	code, body := get(e.c.Token())
	if code != 200 {
		t.Fatalf("dispatcher: %d", code)
	}
	for _, want := range []string{
		`dispatch_http_requests_total{method="GET",route="/trucks",status="2xx"}`,
		`dispatch_http_request_seconds_bucket{method="GET",route="/trucks",le="+Inf"}`,
		"dispatch_ws_clients",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing %s", want)
		}
	}
	e.s.cfg.MetricsToken = "scrape-token-123"
	if code, _ := get("scrape-token-123"); code != 200 {
		t.Fatalf("token: %d", code)
	}
	if code, _ := get("wrong"); code != 401 {
		t.Fatalf("wrong token: %d", code)
	}
}

// ---- Races on the paths the agent writes through ----

// Two deliveries assigned to one truck at the same moment, by dispatchers
// (the console and the Copilot use this route): exactly one wins, the other
// gets truck_busy, and the truck and deliveries agree afterwards.
func TestRaceTwoAssignsOneTruck(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	for i := 0; i < 15; i++ {
		truck := e.truckWithDriver(t, fmt.Sprintf("RC-%02d", i))
		var ids [2]string
		for k := range ids {
			d, err := e.c.CreateDelivery(ctx, client.CreateDeliveryInput{CustomerName: "Race", Stops: twoStops})
			if err != nil {
				t.Fatal(err)
			}
			ids[k] = d.Delivery.ID
		}
		errs := race(func(k int) error { return e.c.AssignTruck(ctx, ids[k], truck.ID) })
		e.checkOneWinner(t, i, errs, ids, truck.ID)
	}
}

// The same with auto mode: the supervisor and a dispatcher reach for one
// truck at once.
func TestRaceSupervisorAndDispatcher(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.autoMode(true)
	agent := client.New(e.srv.URL).WithToken(e.agentKey)
	for i := 0; i < 10; i++ {
		_, drv := e.looseDriver(t, fmt.Sprintf("Racer %d", i), fmt.Sprintf("+2348091%06d", i))
		truck, err := e.c.CreateTruck(ctx, fmt.Sprintf("RS-%02d", i), nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := agent.Post(ctx, "/supervisor/pair", map[string]string{
			"driver_id": drv.ID, "truck_id": truck.ID, "reason": "race", "decided_by": "rule"}, nil); err != nil {
			t.Fatal(err)
		}
		var ids [2]string
		for k := range ids {
			d, err := e.c.CreateDelivery(ctx, client.CreateDeliveryInput{CustomerName: "Race", Stops: twoStops})
			if err != nil {
				t.Fatal(err)
			}
			ids[k] = d.Delivery.ID
		}
		errs := race(func(k int) error {
			if k == 0 {
				return agent.Post(ctx, "/supervisor/assign", map[string]string{
					"delivery_id": ids[0], "truck_id": truck.ID, "reason": "race", "decided_by": "rule"}, nil)
			}
			return e.c.AssignTruck(ctx, ids[1], truck.ID)
		})
		e.checkOneWinner(t, i, errs, ids, truck.ID)
	}
}

// One Idempotency-Key sent ten times at once: one delivery, and every
// answer is either the first response or "still running".
func TestRaceSameIdempotencyKey(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	keyed := e.c.WithHeader(headerIdempotencyKey, "race-create-0001")
	var wg sync.WaitGroup
	results := make([]error, 10)
	ids := make([]string, 10)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			d, err := keyed.CreateDelivery(ctx, client.CreateDeliveryInput{CustomerName: "Once", Stops: twoStops})
			results[i], ids[i] = err, d.Delivery.ID
		}(i)
	}
	wg.Wait()
	first := ""
	for i, err := range results {
		if err != nil {
			var ae *client.APIError
			if !errors.As(err, &ae) || ae.Status != 409 || ae.Code != codeInProgress {
				t.Fatalf("try %d: %v", i, err)
			}
			continue
		}
		if first == "" {
			first = ids[i]
		} else if ids[i] != first {
			t.Fatalf("two deliveries from one key: %s and %s", first, ids[i])
		}
	}
	list, err := e.c.ListDeliveries(ctx, "pending")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("%d deliveries, want 1", len(list))
	}
}

// race runs f(0) and f(1) as close together as goroutines allow.
func race(f func(k int) error) [2]error {
	var errs [2]error
	var wg sync.WaitGroup
	start := make(chan struct{})
	for k := 0; k < 2; k++ {
		wg.Add(1)
		go func(k int) {
			defer wg.Done()
			<-start
			errs[k] = f(k)
		}(k)
	}
	close(start)
	wg.Wait()
	return errs
}

func (e *env) checkOneWinner(t *testing.T, run int, errs [2]error, ids [2]string, truckID string) {
	t.Helper()
	ctx := context.Background()
	wins := 0
	for k, err := range errs {
		if err == nil {
			wins++
			continue
		}
		var ae *client.APIError
		if !errors.As(err, &ae) || ae.Status != 409 || ae.Code != codeTruckBusy {
			t.Fatalf("run %d, side %d: %v (want 409 truck_busy)", run, k, err)
		}
	}
	if wins != 1 {
		t.Fatalf("run %d: %d winners, want 1", run, wins)
	}
	assigned := 0
	for _, id := range ids {
		d, err := e.c.GetDelivery(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if d.Delivery.Status == "assigned" {
			assigned++
		}
	}
	trucks, err := e.c.ListTrucks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, tr := range trucks {
		if tr.ID == truckID && tr.Status != "on_delivery" {
			t.Fatalf("run %d: truck is %s", run, tr.Status)
		}
	}
	if assigned != 1 {
		t.Fatalf("run %d: %d deliveries assigned, want 1", run, assigned)
	}
}
