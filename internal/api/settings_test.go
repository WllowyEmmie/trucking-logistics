package api

// Settings saved from the page: who may, what's checked, and that they
// apply at once and survive a restart. Same TEST_DATABASE_URL setup.

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"

	"github.com/WllowyEmmie/trucking-logistics/internal/client"
	"github.com/WllowyEmmie/trucking-logistics/internal/config"
)

func lagos() map[string]any {
	return map[string]any{
		"country": "NG", "units": "metric", "timezone": "Africa/Lagos", "language": "en-NG",
		"center_lat": 6.5244, "center_lng": 3.3792, "default_zoom": 11,
		"min_lat": 6.3, "max_lat": 6.7, "min_lng": 3.0, "max_lng": 3.7,
		"operator_name": "Kestrel Freight", "support_phone": "+2348031234567", "dispatch_phone": "",
		"depot":         map[string]any{"address": "Apapa Wharf", "lat": 6.4433, "lng": 3.3647},
		"agent_enabled": true, "stale_minutes": 30, "pending_minutes": 90, "failed_window_minutes": 60, "ping_seconds": 15,
	}
}

func TestSettingsSaveApplyAndPersist(t *testing.T) {
	e := setup(t)
	ctx := context.Background()

	var got struct {
		config.Settings
		UpdatedBy *string `json:"updated_by"`
	}
	if err := client.New(e.srv.URL).Get(ctx, "/config", &got); err != nil {
		t.Fatal(err)
	}
	if got.OperatorName != "Dispatch" || got.MinLat != 6 || got.UpdatedBy != nil {
		t.Fatalf("defaults: %+v", got)
	}

	// Who may save.
	if err := client.New(e.srv.URL).Put(ctx, "/config", lagos(), nil); status(err) != 401 {
		t.Fatalf("anonymous save: %d", status(err))
	}
	dc, _, _ := e.newDriver(t, "Ife", "+234 803 000 0009", "KF-50")
	if err := dc.Put(ctx, "/config", lagos(), nil); status(err) != 403 {
		t.Fatalf("driver save: %d", status(err))
	}

	// Checked before saving.
	bad := lagos()
	bad["center_lat"] = 9.0 // outside the new area
	if err := e.c.Put(ctx, "/config", bad, nil); status(err) != 400 {
		t.Fatalf("centre outside the area: %d", status(err))
	}

	if err := e.c.Put(ctx, "/config", lagos(), &got); err != nil {
		t.Fatal(err)
	}
	if got.OperatorName != "Kestrel Freight" || got.StaleMinutes != 30 || got.UpdatedBy == nil || *got.UpdatedBy != testEmail {
		t.Fatalf("after save: %+v by %v", got.Settings, got.UpdatedBy)
	}

	// In effect for the next request: a stop outside Lagos is refused now.
	far := []client.StopInput{
		{StopType: "pickup", Address: "Apapa", Lat: 6.44, Lng: 3.36},
		{StopType: "dropoff", Address: "Ibadan", Lat: 6.9, Lng: 3.9},
	}
	if _, err := e.c.CreateDelivery(ctx, client.CreateDeliveryInput{CustomerName: "X", Stops: far}); status(err) != 400 {
		t.Fatalf("stop outside the new area: %d", status(err))
	}

	// A fresh server (a restart) reads the saved row.
	s2 := NewServer(e.pool, slog.New(slog.NewTextHandler(io.Discard, nil)), e.s.cfg)
	srv2 := httptest.NewServer(s2.Routes())
	defer srv2.Close()
	got.UpdatedBy = nil // decoding into the same struct keeps fields the answer leaves out
	if err := client.New(srv2.URL).Get(ctx, "/config", &got); err != nil {
		t.Fatal(err)
	}
	if got.OperatorName != "Kestrel Freight" || got.MaxLat != 6.7 || got.Depot == nil || got.UpdatedBy != nil {
		t.Fatalf("after restart: %+v", got.Settings)
	}

	// Saving without a depot removes it.
	noDepot := lagos()
	delete(noDepot, "depot")
	if err := e.c.Put(ctx, "/config", noDepot, &got); err != nil || got.Depot != nil {
		t.Fatalf("depot not removed: %+v %v", got.Depot, err)
	}
}

func TestCheckAreaCountsWhatWouldBeLeftOut(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	_, truck := e.newDelivery(t, "KF-60", "in_transit")
	if err := e.c.PostLocations(ctx, truck.ID, []client.Ping{{Lat: 6.5, Lng: 3.4, RecordedAt: nowUTC()}}); err != nil {
		t.Fatal(err)
	}
	var out struct {
		Stops  int `json:"stops_outside"`
		Trucks int `json:"trucks_outside"`
	}
	// A box around Apapa only: the Ikeja drop-off and the truck at 6.5,3.4 fall outside.
	if err := e.c.Post(ctx, "/config/check", map[string]float64{"min_lat": 6.40, "max_lat": 6.46, "min_lng": 3.3, "max_lng": 3.38}, &out); err != nil {
		t.Fatal(err)
	}
	if out.Stops != 1 || out.Trucks != 1 {
		t.Fatalf("outside: %+v, want 1 stop and 1 truck", out)
	}
	if err := e.c.Post(ctx, "/config/check", map[string]float64{"min_lat": 6, "max_lat": 7, "min_lng": 3, "max_lng": 4}, &out); err != nil || out.Stops != 0 || out.Trucks != 0 {
		t.Fatalf("whole area: %+v %v", out, err)
	}
}
