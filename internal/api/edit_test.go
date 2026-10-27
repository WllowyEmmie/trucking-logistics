package api

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/WllowyEmmie/trucking-logistics/internal/config"
	"github.com/WllowyEmmie/trucking-logistics/internal/db"
)

func strp(s string) *string   { return &s }
func f64p(f float64) *float64 { return &f }

var editArea = &config.Settings{MinLat: 6.3, MaxLat: 6.8, MinLng: 3.0, MaxLng: 3.7}

func TestDeliveryEditCheck(t *testing.T) {
	stop := uuid.NewString()
	for _, tc := range []struct {
		name string
		e    deliveryEdit
		want string // a word the refusal contains; "" for none
	}{
		{"name only", deliveryEdit{CustomerName: strp("Awele Bello")}, ""},
		{"empty name", deliveryEdit{CustomerName: strp("  ")}, "empty"},
		{"address without pin", deliveryEdit{Stops: []stopEdit{{ID: stop, Address: strp("Maryland Mall")}}}, "lat and lng"},
		{"address with pin", deliveryEdit{Stops: []stopEdit{{ID: stop, Address: strp("Maryland Mall"), Lat: f64p(6.57), Lng: f64p(3.36)}}}, ""},
		{"half a pin", deliveryEdit{Stops: []stopEdit{{ID: stop, Lat: f64p(6.57)}}}, "together"},
		{"outside the area", deliveryEdit{Stops: []stopEdit{{ID: stop, Lat: f64p(9.0), Lng: f64p(7.4)}}}, "service area"},
		{"bad stop id", deliveryEdit{Stops: []stopEdit{{ID: "2", ContactName: strp("Ada")}}}, "id"},
		{"stop twice", deliveryEdit{Stops: []stopEdit{{ID: stop}, {ID: stop}}}, "twice"},
	} {
		got := tc.e.check(editArea)
		if (tc.want == "") != (got == "") || (tc.want != "" && !strings.Contains(got, tc.want)) {
			t.Errorf("%s: got %q, want something with %q", tc.name, got, tc.want)
		}
	}
}

func TestDeliveryEditPlan(t *testing.T) {
	pickup := db.StopRow{ID: uuid.New(), Sequence: 1, StopType: "pickup", Address: "3 Animat Street", Lat: f64p(6.66), Lng: f64p(3.33), Status: "completed"}
	drop := db.StopRow{ID: uuid.New(), Sequence: 2, StopType: "dropoff", Address: "Maryland Mall", Lat: f64p(6.57), Lng: f64p(3.36), Status: "pending"}
	cur := db.EditableDelivery{Reference: "DEL-2026-10024", Status: "assigned", CustomerName: "Curry Barker", GoodsDesc: strp("Montessori tools")}
	stops := []db.StopRow{pickup, drop}

	e := deliveryEdit{
		CustomerName:  strp("Awele Bello"),
		CustomerPhone: strp("+2348012345678"),
		GoodsDesc:     strp("Montessori tools"), // unchanged
		Stops:         []stopEdit{{ID: drop.ID.String(), Address: strp("Ikeja City Mall"), Lat: f64p(6.61), Lng: f64p(3.35), ContactName: strp("Ada")}},
	}
	name, phone, goods, sc, changes, err := e.plan(cur, stops)
	if err != nil {
		t.Fatal(err)
	}
	if name != "Awele Bello" || orNone(phone) != "+2348012345678" || orNone(goods) != "Montessori tools" {
		t.Errorf("new values: %q %q %q", name, orNone(phone), orNone(goods))
	}
	if len(sc) != 1 || sc[0].address != "Ikeja City Mall" || sc[0].lat != 6.61 || orNone(sc[0].contactName) != "Ada" {
		t.Errorf("stop change: %+v", sc)
	}
	joined := strings.Join(changes, "; ")
	for _, want := range []string{"customer: Curry Barker → Awele Bello", "customer phone: none → +2348012345678", "stop 2 (drop-off) address: Maryland Mall → Ikeja City Mall", "stop 2 (drop-off) contact: none → Ada"} {
		if !strings.Contains(joined, want) {
			t.Errorf("changes %q miss %q", joined, want)
		}
	}
	if strings.Contains(joined, "goods") || strings.Contains(joined, "pin moved") {
		t.Errorf("unchanged fields reported: %q", joined)
	}

	// The pickup has been done: it can't move.
	_, _, _, _, _, err = deliveryEdit{Stops: []stopEdit{{ID: pickup.ID.String(), ContactName: strp("Bayo")}}}.plan(cur, stops)
	var ae apiError
	if !errors.As(err, &ae) || ae.status != 409 {
		t.Errorf("editing a completed stop: %v, want a 409", err)
	}

	// Nothing different: nothing to record.
	_, _, _, _, changes, _ = deliveryEdit{CustomerName: strp("Curry Barker"), GoodsDesc: strp("Montessori tools")}.plan(cur, stops)
	if len(changes) != 0 {
		t.Errorf("no-op edit reported %v", changes)
	}
}
