package api

import (
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/WllowyEmmie/trucking-logistics/internal/db"
)

var validOutcomes = map[string]bool{"": true, "completed": true, "cancelled": true, "failed": true, "recovered": true, "late": true}

type historyItem struct {
	db.HistoryRow
	// Completed after its due time.
	Late bool `json:"late"`
}

// deliveryHistory is GET /deliveries/history (dispatchers): finished
// deliveries, newest first, a page at a time.
//
//	outcome  completed | cancelled | failed | recovered | late (default all)
//	q        part of the reference or customer name
//	from,to  finished in [from, to), RFC 3339
//	limit    1-200 (50)
//	before   the `next` of the previous page
func (s *Server) deliveryHistory(w http.ResponseWriter, r *http.Request) {
	v := r.URL.Query()
	bad := func(msg string) { writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg}) }
	f := db.HistoryFilter{Outcome: v.Get("outcome"), Query: strings.TrimSpace(v.Get("q")), Limit: 50}
	if !validOutcomes[f.Outcome] {
		bad("outcome must be completed, cancelled, failed, recovered or late")
		return
	}
	if len(f.Query) > 100 {
		bad("search is too long")
		return
	}
	for _, p := range []struct {
		key string
		dst **time.Time
	}{{"from", &f.From}, {"to", &f.To}} {
		if raw := v.Get(p.key); raw != "" {
			t, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				bad(p.key + " must be a time like 2026-10-20T00:00:00Z")
				return
			}
			*p.dst = &t
		}
	}
	if raw := v.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 200 {
			bad("limit must be 1 to 200")
			return
		}
		f.Limit = n
	}
	if raw := v.Get("before"); raw != "" {
		at, id, ok := decodeCursor(raw)
		if !ok {
			bad("invalid cursor")
			return
		}
		f.BeforeAt, f.BeforeID = &at, &id
	}

	// One extra row says whether there's another page.
	want := f.Limit
	f.Limit++
	rows, err := s.queries.DeliveryHistory(r.Context(), f)
	if err != nil {
		s.respondErr(w, err, "delivery history", "could not load history")
		return
	}
	var next *string
	if len(rows) > want {
		rows = rows[:want]
		last := rows[len(rows)-1]
		c := encodeCursor(last.FinishedAt, last.ID)
		next = &c
	}
	items := make([]historyItem, len(rows))
	for i, h := range rows {
		items[i] = historyItem{HistoryRow: h, Late: h.Status == "completed" && h.DueBy != nil && h.FinishedAt.After(*h.DueBy)}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next": next})
}

func encodeCursor(at time.Time, id uuid.UUID) string {
	return base64.RawURLEncoding.EncodeToString([]byte(at.UTC().Format(time.RFC3339Nano) + "|" + id.String()))
}

func decodeCursor(s string) (time.Time, uuid.UUID, bool) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return time.Time{}, uuid.UUID{}, false
	}
	at, id, ok := strings.Cut(string(b), "|")
	if !ok {
		return time.Time{}, uuid.UUID{}, false
	}
	t, err1 := time.Parse(time.RFC3339Nano, at)
	u, err2 := uuid.Parse(id)
	return t, u, err1 == nil && err2 == nil
}
