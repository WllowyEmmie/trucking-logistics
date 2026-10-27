package api

import (
	"context"
	"testing"
	"time"

	"github.com/WllowyEmmie/trucking-logistics/internal/client"
	"github.com/WllowyEmmie/trucking-logistics/internal/db"
)

func TestBuildReportAddsUp(t *testing.T) {
	loc, _ := time.LoadLocation("Africa/Lagos")
	day := func(d, h int) time.Time { return time.Date(2026, 10, d, h, 0, 0, 0, loc) }
	ptr := func(t time.Time) *time.Time { return &t }
	plate := func(s string) *string { return &s }
	rows := []db.ReportRow{
		// Created and completed on the 20th, before its due time.
		{Reference: "A", Status: "completed", CreatedAt: day(20, 9), FinishedAt: ptr(day(20, 11)), DueBy: ptr(day(20, 12)), Plate: plate("KF-01")},
		// Completed late, on the 21st.
		{Reference: "B", Status: "completed", CreatedAt: day(20, 10), FinishedAt: ptr(day(21, 9)), DueBy: ptr(day(20, 18)), Plate: plate("KF-01")},
		{Reference: "C", Status: "cancelled", CreatedAt: day(21, 8), FinishedAt: ptr(day(21, 8)), Plate: nil},
		{Reference: "D", Status: "failed", CreatedAt: day(21, 9), FinishedAt: ptr(day(21, 10)), Plate: plate("KF-02")},
		// 23:30 local on the 21st is 22:30 UTC: still the 21st here.
		{Reference: "E", Status: "pending", CreatedAt: time.Date(2026, 10, 21, 22, 30, 0, 0, time.UTC)},
	}
	out := buildReport(rows, day(20, 0), day(22, 0), loc)
	if out.Total != 5 || out.ByStatus["completed"] != 2 || out.ByStatus["cancelled"] != 1 || out.ByStatus["failed"] != 1 || out.ByStatus["pending"] != 1 {
		t.Fatalf("by status: %+v", out.ByStatus)
	}
	if out.OnTime.OnTime != 1 || out.OnTime.Late != 1 || out.OnTime.NoDue != 0 {
		t.Fatalf("on time: %+v", out.OnTime)
	}
	if len(out.ByDay) != 2 || out.ByDay[0].Date != "2026-10-20" || out.ByDay[0].Created != 2 || out.ByDay[0].Completed != 1 ||
		out.ByDay[1].Created != 3 || out.ByDay[1].Completed != 1 || out.ByDay[1].Cancelled != 1 || out.ByDay[1].Failed != 1 {
		t.Fatalf("by day: %+v", out.ByDay)
	}
	if len(out.ByTruck) != 2 || out.ByTruck[0].Plate != "KF-01" || out.ByTruck[0].Completed != 2 || out.ByTruck[1].Failed != 1 {
		t.Fatalf("by truck: %+v", out.ByTruck)
	}
	// (2 h + 23 h) / 2 = 12.5 h
	if out.AvgCompleteMin == nil || *out.AvgCompleteMin != 750 {
		t.Fatalf("avg: %v", out.AvgCompleteMin)
	}
}

func TestDeliveryReportEndpoint(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	d, _ := e.newDelivery(t, "RP-01", "assigned")
	if err := e.c.CancelDelivery(ctx, d.Delivery.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.c.CreateDelivery(ctx, client.CreateDeliveryInput{CustomerName: "Open", Stops: twoStops}); err != nil {
		t.Fatal(err)
	}
	today := time.Now().In(time.UTC).Format("2006-01-02")
	var out struct {
		Total    int            `json:"total"`
		ByStatus map[string]int `json:"by_status"`
		ByTruck  []struct {
			Plate     string `json:"plate"`
			Cancelled int    `json:"cancelled"`
		} `json:"by_truck"`
	}
	// A day either side, so the operator's time zone can't push today out.
	from := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	to := time.Now().AddDate(0, 0, 1).Format("2006-01-02")
	if err := e.c.Get(ctx, "/reports/deliveries?from="+from+"&to="+to, &out); err != nil {
		t.Fatal(err)
	}
	if out.Total != 2 || out.ByStatus["cancelled"] != 1 || out.ByStatus["pending"] != 1 {
		t.Fatalf("report (%s): %+v", today, out)
	}
	if len(out.ByTruck) != 1 || out.ByTruck[0].Plate != "RP-01" || out.ByTruck[0].Cancelled != 1 {
		t.Fatalf("by truck: %+v", out.ByTruck)
	}
	if got := status(e.c.Get(ctx, "/reports/deliveries?from=2026-01-01&to=2026-12-31", nil)); got != 400 {
		t.Fatalf("a year: %d, want 400", got)
	}
	if got := status(e.c.Get(ctx, "/reports/deliveries?from=bad&to=2026-01-01", nil)); got != 400 {
		t.Fatalf("bad date: %d, want 400", got)
	}
}
