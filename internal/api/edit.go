package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/WllowyEmmie/trucking-logistics/internal/config"
	"github.com/WllowyEmmie/trucking-logistics/internal/db"
)

// PATCH /deliveries/{id} (dispatchers, the Copilot): fix a delivery's
// details: the customer's name and phone, the goods, and each stop's
// address, pin and site contact. Only before the truck sets off (pending or
// assigned): once it's in transit the driver is working from what they were
// given, so a wrong delivery is failed and created again instead.
//
// A field left out is kept; "" clears a phone, the goods or a contact. The
// stops are matched by id; their order and types don't change. Every edit is
// an "edited" event saying what changed from what, and an assigned driver
// gets a message about it.

type stopEdit struct {
	ID           string   `json:"id"`
	Address      *string  `json:"address"`
	Lat          *float64 `json:"lat"`
	Lng          *float64 `json:"lng"`
	ContactName  *string  `json:"contact_name"`
	ContactPhone *string  `json:"contact_phone"`
}

type deliveryEdit struct {
	CustomerName  *string    `json:"customer_name"`
	CustomerPhone *string    `json:"customer_phone"`
	GoodsDesc     *string    `json:"goods_desc"`
	Stops         []stopEdit `json:"stops"`
}

// editableStatuses: before the truck sets off.
var editableStatuses = map[string]bool{"pending": true, "assigned": true}

const (
	editMaxName    = 200
	editMaxAddress = 300
	editMaxShort   = 120
)

// check is what's wrong with the edit on its own, or "".
func (e deliveryEdit) check(area *config.Settings) string {
	if e.CustomerName != nil {
		n := strings.TrimSpace(*e.CustomerName)
		if n == "" {
			return "the customer's name can't be empty"
		}
		if utf8.RuneCountInString(n) > editMaxName {
			return "the customer's name is too long"
		}
	}
	for _, f := range []*string{e.CustomerPhone, e.GoodsDesc} {
		if f != nil && utf8.RuneCountInString(*f) > editMaxAddress {
			return "a field is too long"
		}
	}
	seen := map[string]bool{}
	for _, s := range e.Stops {
		if _, err := uuid.Parse(s.ID); err != nil {
			return "every stop needs its id"
		}
		if seen[s.ID] {
			return "a stop is listed twice"
		}
		seen[s.ID] = true
		if (s.Lat == nil) != (s.Lng == nil) {
			return "send lat and lng together"
		}
		if s.Address != nil {
			a := strings.TrimSpace(*s.Address)
			if a == "" {
				return "a stop's address can't be empty"
			}
			if utf8.RuneCountInString(a) > editMaxAddress {
				return "an address is too long"
			}
			// A new address without a new pin would leave the map, the ETA
			// and the driver's directions at the old place.
			if s.Lat == nil {
				return "a new address needs its lat and lng (use Find)"
			}
		}
		if s.Lat != nil && !area.Contains(*s.Lat, *s.Lng) {
			return "coordinates must be within the service area — check that lat and lng aren't swapped"
		}
		for _, f := range []*string{s.ContactName, s.ContactPhone} {
			if f != nil && utf8.RuneCountInString(*f) > editMaxShort {
				return "a contact is too long"
			}
		}
	}
	return ""
}

// optional: "" means none.
func optional(s *string) *string {
	if s == nil {
		return nil
	}
	t := strings.TrimSpace(*s)
	if t == "" {
		return nil
	}
	return &t
}

func orNone(s *string) string {
	if s == nil || *s == "" {
		return "none"
	}
	return *s
}

func sameOpt(a, b *string) bool { return orNone(a) == orNone(b) }

type stopChange struct {
	stop         db.StopRow
	address      string
	lat, lng     float64
	contactName  *string
	contactPhone *string
}

// plan works out the new values and the lines that describe them. A stop
// that has been reached can't change: the driver is already there or past it.
func (e deliveryEdit) plan(cur db.EditableDelivery, stops []db.StopRow) (name string, phone, goods *string, sc []stopChange, changes []string, err error) {
	name, phone, goods = cur.CustomerName, cur.CustomerPhone, cur.GoodsDesc
	if e.CustomerName != nil {
		if n := strings.TrimSpace(*e.CustomerName); n != name {
			changes = append(changes, fmt.Sprintf("customer: %s → %s", name, n))
			name = n
		}
	}
	if e.CustomerPhone != nil {
		if p := optional(e.CustomerPhone); !sameOpt(p, phone) {
			changes = append(changes, fmt.Sprintf("customer phone: %s → %s", orNone(phone), orNone(p)))
			phone = p
		}
	}
	if e.GoodsDesc != nil {
		if g := optional(e.GoodsDesc); !sameOpt(g, goods) {
			changes = append(changes, fmt.Sprintf("goods: %s → %s", orNone(goods), orNone(g)))
			goods = g
		}
	}
	byID := map[string]db.StopRow{}
	for _, s := range stops {
		byID[s.ID.String()] = s
	}
	for _, in := range e.Stops {
		s, ok := byID[in.ID]
		if !ok {
			return "", nil, nil, nil, nil, errNotFound("stop " + in.ID + " isn't on this delivery")
		}
		kind := "drop-off"
		if s.StopType == "pickup" {
			kind = "pickup"
		}
		label := fmt.Sprintf("stop %d (%s)", s.Sequence, kind)
		c := stopChange{stop: s, address: s.Address, contactName: s.ContactName, contactPhone: s.ContactPhone}
		if s.Lat != nil && s.Lng != nil {
			c.lat, c.lng = *s.Lat, *s.Lng
		}
		var lines []string
		if in.Address != nil {
			if a := strings.TrimSpace(*in.Address); a != s.Address {
				lines = append(lines, fmt.Sprintf("%s address: %s → %s", label, s.Address, a))
				c.address = a
			}
		}
		if in.Lat != nil && (*in.Lat != c.lat || *in.Lng != c.lng) {
			if in.Address == nil || strings.TrimSpace(*in.Address) == s.Address {
				lines = append(lines, label+" pin moved")
			}
			c.lat, c.lng = *in.Lat, *in.Lng
		}
		if in.ContactName != nil {
			if v := optional(in.ContactName); !sameOpt(v, s.ContactName) {
				lines = append(lines, fmt.Sprintf("%s contact: %s → %s", label, orNone(s.ContactName), orNone(v)))
				c.contactName = v
			}
		}
		if in.ContactPhone != nil {
			if v := optional(in.ContactPhone); !sameOpt(v, s.ContactPhone) {
				lines = append(lines, fmt.Sprintf("%s contact phone: %s → %s", label, orNone(s.ContactPhone), orNone(v)))
				c.contactPhone = v
			}
		}
		if len(lines) == 0 {
			continue
		}
		if s.Status != "pending" {
			return "", nil, nil, nil, nil, errConflictCode(codeWrongStatus, label+" is already "+s.Status+" and can't change")
		}
		sc = append(sc, c)
		changes = append(changes, lines...)
	}
	return name, phone, goods, sc, changes, nil
}

func (s *Server) editDelivery(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid delivery id"})
		return
	}
	var body deliveryEdit
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "send customer_name, customer_phone, goods_desc and/or stops [{id, address, lat, lng, contact_name, contact_phone}]"})
		return
	}
	if msg := body.check(s.st()); msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}

	ctx := r.Context()
	tx, err := s.beginTx(ctx)
	if err != nil {
		s.respondErr(w, err, "begin tx", "could not edit the delivery")
		return
	}
	defer tx.Rollback(ctx)
	qtx := s.queries.WithTx(tx)

	cur, err := qtx.GetDeliveryForEdit(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "delivery not found"})
		return
	}
	if err != nil {
		s.respondErr(w, err, "get delivery", "could not edit the delivery")
		return
	}
	if !editableStatuses[cur.Status] {
		msg := cur.Reference + " is " + strings.ReplaceAll(cur.Status, "_", " ") + " and can't be edited"
		if cur.Status == "in_transit" {
			msg += ": the truck has set off. Fail it and create a new delivery with the right details"
		}
		writeJSON(w, http.StatusConflict, map[string]string{"error": msg, "code": codeWrongStatus})
		return
	}
	stops, err := qtx.ListStopsForDelivery(ctx, id)
	if err != nil {
		s.respondErr(w, err, "list stops", "could not edit the delivery")
		return
	}
	name, phone, goods, stopChanges, changes, err := body.plan(cur, stops)
	if err != nil {
		s.respondErr(w, err, "plan edit", "could not edit the delivery")
		return
	}
	if len(changes) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"reference": cur.Reference, "changes": []string{}})
		return
	}

	if err := qtx.UpdateDeliveryDetails(ctx, id, name, phone, goods); err != nil {
		s.respondErr(w, err, "update delivery", "could not edit the delivery")
		return
	}
	for _, c := range stopChanges {
		if err := qtx.UpdateStopDetails(ctx, c.stop.ID, c.address, c.lat, c.lng, c.contactName, c.contactPhone); err != nil {
			s.respondErr(w, err, "update stop", "could not edit the delivery")
			return
		}
	}
	note := strings.Join(changes, "; ")
	if err := qtx.CreateDeliveryEvent(ctx, db.CreateDeliveryEventParams{DeliveryID: id, EventType: "edited", Note: &note}); err != nil {
		s.respondErr(w, err, "create event", "could not edit the delivery")
		return
	}

	// The driver it's assigned to hears about it in their thread, so they
	// don't drive to the old address.
	var msg *db.Message
	if cur.Status == "assigned" && cur.DriverID != nil {
		sender := "Dispatch"
		if p := principalFrom(ctx); p != nil && p.Name != "" {
			sender = p.Name
		}
		m, err := qtx.AddMessage(ctx, *cur.DriverID, false, sender, cur.Reference+" was changed: "+note)
		if err != nil {
			s.respondErr(w, err, "message driver", "could not edit the delivery")
			return
		}
		msg = &m
	}

	if err := tx.Commit(ctx); err != nil {
		s.respondErr(w, err, "commit", "could not edit the delivery")
		return
	}
	if msg != nil {
		s.hub.toDriver(*cur.DriverID, wsMessage{Type: "message", Message: msg})
		s.hub.broadcast(wsMessage{Type: "changed", Topics: []string{"messages"}})
	}
	writeJSON(w, http.StatusOK, map[string]any{"reference": cur.Reference, "changes": changes})
}
