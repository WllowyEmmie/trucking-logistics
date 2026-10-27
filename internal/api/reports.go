package api

import (
	"net/http"
	"sort"
	"time"

	"github.com/WllowyEmmie/trucking-logistics/internal/db"
)

// GET /reports/deliveries?from=2026-10-01&to=2026-10-22
//
// What happened to the deliveries created between two dates (inclusive,
// in the operator's time zone): counts by status, a series per day, per
// truck, on time vs late, and the deliveries themselves. The Copilot's
// stats and chart tools read this, so the numbers come from the database,
// never from the model's arithmetic. At most 92 days.

const (
	reportMaxDays = 92
	reportMaxRows = 2000
)

type reportDay struct {
	Date      string `json:"date"`
	Created   int    `json:"created"`
	Completed int    `json:"completed"`
	Cancelled int    `json:"cancelled"`
	Failed    int    `json:"failed"`
}

type reportTruck struct {
	Plate     string `json:"plate"`
	Completed int    `json:"completed"`
	Failed    int    `json:"failed"`
	Cancelled int    `json:"cancelled"`
	Open      int    `json:"open"`
}

type reportOut struct {
	From     string         `json:"from"`
	To       string         `json:"to"`
	Timezone string         `json:"timezone"`
	Total    int            `json:"total"`
	ByStatus map[string]int `json:"by_status"`
	// Completed deliveries that had a due time: done by it, or after.
	OnTime struct {
		OnTime int `json:"on_time"`
		Late   int `json:"late"`
		NoDue  int `json:"no_due"`
	} `json:"on_time"`
	// Minutes from created to completed, for completed deliveries.
	AvgCompleteMin *float64       `json:"avg_complete_min"`
	ByDay          []reportDay    `json:"by_day"`
	ByTruck        []reportTruck  `json:"by_truck"`
	Deliveries     []db.ReportRow `json:"deliveries"`
	// More deliveries than were listed.
	Truncated bool `json:"truncated"`
}

func (s *Server) deliveryReport(w http.ResponseWriter, r *http.Request) {
	bad := func(msg string) { writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg}) }
	tzName := s.st().Timezone
	loc, err := time.LoadLocation(tzName)
	if err != nil {
		loc, tzName = time.UTC, "UTC"
	}
	from, err1 := time.ParseInLocation("2006-01-02", r.URL.Query().Get("from"), loc)
	to, err2 := time.ParseInLocation("2006-01-02", r.URL.Query().Get("to"), loc)
	if err1 != nil || err2 != nil {
		bad("from and to must be dates like 2026-10-22")
		return
	}
	if to.Before(from) {
		bad("to is before from")
		return
	}
	end := to.AddDate(0, 0, 1) // inclusive: up to the end of `to`
	if end.Sub(from) > reportMaxDays*24*time.Hour+time.Hour {
		bad("at most 92 days at a time")
		return
	}

	rows, err := s.queries.ReportDeliveries(r.Context(), from, end, reportMaxRows+1)
	if err != nil {
		s.respondErr(w, err, "report deliveries", "could not build the report")
		return
	}
	out := buildReport(rows, from, end, loc)
	out.Timezone = tzName
	writeJSON(w, http.StatusOK, out)
}

// buildReport adds the rows up. Separate from the handler so it's testable.
func buildReport(rows []db.ReportRow, from, end time.Time, loc *time.Location) reportOut {
	var out reportOut
	out.From = from.Format("2006-01-02")
	out.To = end.AddDate(0, 0, -1).Format("2006-01-02")
	out.ByStatus = map[string]int{"pending": 0, "assigned": 0, "in_transit": 0, "completed": 0, "cancelled": 0, "failed": 0}
	if len(rows) > reportMaxRows {
		rows, out.Truncated = rows[:reportMaxRows], true
	}

	days := map[string]*reportDay{}
	for d := from; d.Before(end); d = d.AddDate(0, 0, 1) {
		key := d.Format("2006-01-02")
		days[key] = &reportDay{Date: key}
	}
	trucks := map[string]*reportTruck{}
	var completeMin float64
	var completed int

	for _, row := range rows {
		out.Total++
		out.ByStatus[row.Status]++
		if d := days[row.CreatedAt.In(loc).Format("2006-01-02")]; d != nil {
			d.Created++
		}
		if row.FinishedAt != nil {
			if d := days[row.FinishedAt.In(loc).Format("2006-01-02")]; d != nil {
				switch row.Status {
				case "completed":
					d.Completed++
				case "cancelled":
					d.Cancelled++
				case "failed":
					d.Failed++
				}
			}
		}
		if row.Status == "completed" {
			switch {
			case row.DueBy == nil:
				out.OnTime.NoDue++
			case row.FinishedAt != nil && !row.FinishedAt.After(*row.DueBy):
				out.OnTime.OnTime++
			default:
				out.OnTime.Late++
			}
			if row.FinishedAt != nil {
				completeMin += row.FinishedAt.Sub(row.CreatedAt).Minutes()
				completed++
			}
		}
		if row.Plate != nil && *row.Plate != "" {
			t := trucks[*row.Plate]
			if t == nil {
				t = &reportTruck{Plate: *row.Plate}
				trucks[*row.Plate] = t
			}
			switch row.Status {
			case "completed":
				t.Completed++
			case "failed":
				t.Failed++
			case "cancelled":
				t.Cancelled++
			default:
				t.Open++
			}
		}
	}
	if completed > 0 {
		avg := completeMin / float64(completed)
		out.AvgCompleteMin = &avg
	}
	for d := from; d.Before(end); d = d.AddDate(0, 0, 1) {
		out.ByDay = append(out.ByDay, *days[d.Format("2006-01-02")])
	}
	for _, t := range trucks {
		out.ByTruck = append(out.ByTruck, *t)
	}
	// Busiest first, then by plate.
	sort.Slice(out.ByTruck, func(i, j int) bool {
		a, b := out.ByTruck[i], out.ByTruck[j]
		ta, tb := a.Completed+a.Failed+a.Cancelled+a.Open, b.Completed+b.Failed+b.Cancelled+b.Open
		if ta != tb {
			return ta > tb
		}
		return a.Plate < b.Plate
	})
	out.Deliveries = rows
	if out.ByTruck == nil {
		out.ByTruck = []reportTruck{}
	}
	return out
}
