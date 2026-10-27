package api

import (
	"net/http"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/WllowyEmmie/trucking-logistics/internal/db"
)

// GET /problems: what needs a dispatcher's attention right now, worst
// first. One sweep over the board, the trucks and the drivers, so the
// Copilot's find_problems (and, later, the console) ask once instead of
// stitching lists together. The rules are here, not in a prompt.

const (
	silentAfter     = 20 * time.Minute // a working truck with no position for this long
	notStartedAfter = 30 * time.Minute // assigned, driver hasn't pressed Start
	waitingAfter    = 30 * time.Minute // pending with no truck
)

// Severity, worst first (the status palette's names).
const (
	sevCritical = "critical"
	sevSerious  = "serious"
	sevWarning  = "warning"
)

var sevRank = map[string]int{sevCritical: 0, sevSerious: 1, sevWarning: 2}

type problem struct {
	Kind       string     `json:"kind"`
	Severity   string     `json:"severity"`
	Summary    string     `json:"summary"`
	DeliveryID *uuid.UUID `json:"delivery_id,omitempty"`
	Reference  string     `json:"reference,omitempty"`
	Plate      string     `json:"plate,omitempty"`
	Driver     string     `json:"driver,omitempty"`
	// How long it's been the case, in minutes (how late, how silent...).
	Minutes int `json:"minutes"`
}

type problemsOut struct {
	At       time.Time `json:"at"`
	Count    int       `json:"count"`
	Problems []problem `json:"problems"`
	// Context for "pending with no truck": how many trucks could take one.
	FreeTrucks int `json:"free_trucks"`
}

type problemInputs struct {
	Now        time.Time
	Deliveries []deliveryItem
	AssignedAt map[uuid.UUID]time.Time
	Trucks     []db.TruckWithPosition
	Drivers    []db.ListDriversRow
	OnShift    map[uuid.UUID]time.Time
}

func (s *Server) problems(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	since := time.Now().Add(-time.Duration(s.st().BoardHours) * time.Hour)
	items, err := s.queries.ListDeliveryItems(ctx, "", &since)
	if err != nil {
		s.respondErr(w, err, "problems: deliveries", "could not check for problems")
		return
	}
	withETA, err := s.withETAs(r, items)
	if err != nil {
		s.respondErr(w, err, "problems: estimates", "could not check for problems")
		return
	}
	var assigned []uuid.UUID
	for _, d := range items {
		if d.Status == "assigned" {
			assigned = append(assigned, d.ID)
		}
	}
	at, err := s.queries.LastAssignedAt(ctx, assigned)
	if err != nil {
		s.respondErr(w, err, "problems: assigned at", "could not check for problems")
		return
	}
	trucks, err := s.queries.ListTrucks(ctx)
	if err != nil {
		s.respondErr(w, err, "problems: trucks", "could not check for problems")
		return
	}
	drivers, err := s.queries.ListDrivers(ctx)
	if err != nil {
		s.respondErr(w, err, "problems: drivers", "could not check for problems")
		return
	}
	shifts, err := s.queries.OpenShifts(ctx)
	if err != nil {
		s.respondErr(w, err, "problems: shifts", "could not check for problems")
		return
	}
	writeJSON(w, http.StatusOK, findProblems(problemInputs{
		Now: time.Now(), Deliveries: withETA, AssignedAt: at, Trucks: trucks, Drivers: drivers, OnShift: shifts,
	}))
}

func mins(d time.Duration) int { return int(d / time.Minute) }

// findProblems applies the rules. Separate from the handler so it's testable.
func findProblems(in problemInputs) problemsOut {
	out := problemsOut{At: in.Now, Problems: []problem{}}
	plate := map[uuid.UUID]string{}
	driverOf := map[uuid.UUID]string{}
	names := map[uuid.UUID]string{}
	for _, d := range in.Drivers {
		names[d.ID] = d.Name
	}
	hasTruck := map[uuid.UUID]bool{}
	for _, t := range in.Trucks {
		plate[t.ID] = t.PlateNumber
		if t.DriverID != nil {
			hasTruck[*t.DriverID] = true
			driverOf[t.ID] = names[*t.DriverID]
		}
		if t.Status == "available" && t.DriverID != nil && t.DriverOnShift {
			out.FreeTrucks++
		}
	}
	add := func(p problem) { out.Problems = append(out.Problems, p) }

	for _, d := range in.Deliveries {
		id := d.ID
		truck := ""
		if d.TruckID != nil {
			truck = plate[*d.TruckID]
		}
		open := d.Status == "pending" || d.Status == "assigned" || d.Status == "in_transit"
		switch {
		case open && d.DueBy != nil && in.Now.After(*d.DueBy):
			add(problem{Kind: "late", Severity: sevCritical, DeliveryID: &id, Reference: d.Reference, Plate: truck,
				Minutes: mins(in.Now.Sub(*d.DueBy)), Summary: d.Reference + " is past its due time and is still " + d.Status})
		case open && d.DueBy != nil && d.ETA != nil && d.ETA.After(*d.DueBy):
			add(problem{Kind: "at_risk", Severity: sevSerious, DeliveryID: &id, Reference: d.Reference, Plate: truck,
				Minutes: mins(d.ETA.Sub(*d.DueBy)), Summary: d.Reference + "'s estimated arrival is after its due time"})
		}
		switch d.Status {
		case "assigned":
			if a, ok := in.AssignedAt[d.ID]; ok && in.Now.Sub(a) >= notStartedAfter {
				add(problem{Kind: "not_started", Severity: sevWarning, DeliveryID: &id, Reference: d.Reference, Plate: truck,
					Minutes: mins(in.Now.Sub(a)), Summary: d.Reference + " was assigned to " + truck + " but hasn't started"})
			}
		case "pending":
			if in.Now.Sub(d.CreatedAt) >= waitingAfter {
				add(problem{Kind: "waiting", Severity: sevWarning, DeliveryID: &id, Reference: d.Reference,
					Minutes: mins(in.Now.Sub(d.CreatedAt)), Summary: d.Reference + " is waiting for a truck"})
			}
		case "failed":
			p := problem{Kind: "failed", Severity: sevSerious, DeliveryID: &id, Reference: d.Reference, Plate: truck,
				Summary: d.Reference + " failed and hasn't been sent out again or cancelled"}
			if d.FinishedAt != nil {
				p.Minutes = mins(in.Now.Sub(*d.FinishedAt))
			}
			add(p)
		}
	}

	for _, t := range in.Trucks {
		working := t.Status == "on_delivery" || (t.Status == "available" && t.DriverOnShift)
		if !working {
			continue
		}
		sev := sevWarning
		if t.Status == "on_delivery" {
			sev = sevSerious // goods on board and nobody can see where
		}
		switch {
		case t.LastSeenAt == nil:
			add(problem{Kind: "silent_truck", Severity: sev, Plate: t.PlateNumber, Driver: driverOf[t.ID],
				Summary: t.PlateNumber + " has never reported a position"})
		case in.Now.Sub(*t.LastSeenAt) >= silentAfter:
			add(problem{Kind: "silent_truck", Severity: sev, Plate: t.PlateNumber, Driver: driverOf[t.ID],
				Minutes: mins(in.Now.Sub(*t.LastSeenAt)), Summary: t.PlateNumber + " hasn't reported its position"})
		}
	}

	for _, d := range in.Drivers {
		if since, on := in.OnShift[d.ID]; on && !hasTruck[d.ID] {
			add(problem{Kind: "driver_without_truck", Severity: sevWarning, Driver: d.Name,
				Minutes: mins(in.Now.Sub(since)), Summary: d.Name + " is on shift without a truck"})
		}
	}

	sort.SliceStable(out.Problems, func(i, j int) bool {
		a, b := out.Problems[i], out.Problems[j]
		if sevRank[a.Severity] != sevRank[b.Severity] {
			return sevRank[a.Severity] < sevRank[b.Severity]
		}
		return a.Minutes > b.Minutes
	})
	out.Count = len(out.Problems)
	return out
}
