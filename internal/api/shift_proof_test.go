package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/WllowyEmmie/trucking-logistics/internal/client"
)

func ping() []client.Ping {
	return []client.Ping{{Lat: 6.5, Lng: 3.4, RecordedAt: time.Now()}}
}

// GPS is recorded only on shift; starting a delivery starts the shift; a
// shift can't end with a delivery on the road; dispatch can end it after.
func TestShiftsGateGPS(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	dc, truck, me := e.newDriver(t, "Bayo", "+234 803 000 0011", "KF-20")

	// Signing in started the shift; end it to see positions refused.
	var sh shiftResponse
	if err := dc.Get(ctx, "/auth/me", &sh); err != nil || !sh.OnShift {
		t.Fatalf("signed in but not on shift: %+v %v", sh, err)
	}
	if err := dc.Post(ctx, "/auth/me/shift", map[string]bool{"on": false}, nil); err != nil {
		t.Fatal(err)
	}
	if got := status(dc.PostLocations(ctx, truck.ID, ping())); got != 409 {
		t.Fatalf("position off shift: %d, want 409", got)
	}
	if err := dc.Post(ctx, "/auth/me/shift", map[string]bool{"on": true}, &sh); err != nil || !sh.OnShift || sh.Since == nil {
		t.Fatalf("start shift: %+v %v", sh, err)
	}
	if err := dc.PostLocations(ctx, truck.ID, ping()); err != nil {
		t.Fatalf("position on shift: %v", err)
	}
	if err := dc.Post(ctx, "/auth/me/shift", map[string]bool{"on": false}, &sh); err != nil || sh.OnShift {
		t.Fatalf("end shift: %+v %v", sh, err)
	}

	// Setting off on a delivery starts the shift by itself.
	id, _ := e.newDeliveryOn(t, truck.ID)
	if err := dc.StartDelivery(ctx, id); err != nil {
		t.Fatal(err)
	}
	var who struct {
		OnShift *bool `json:"on_shift"`
	}
	if err := dc.Get(ctx, "/auth/me", &who); err != nil || who.OnShift == nil || !*who.OnShift {
		t.Fatalf("after start: %+v %v", who, err)
	}
	if got := status(dc.Post(ctx, "/auth/me/shift", map[string]bool{"on": false}, nil)); got != 409 {
		t.Fatalf("end shift mid-delivery: %d, want 409", got)
	}
	// Dispatch sees it on the driver and the truck.
	var drivers []struct {
		ID      string `json:"id"`
		OnShift bool   `json:"on_shift"`
	}
	if err := e.c.Get(ctx, "/drivers", &drivers); err != nil || len(drivers) != 1 || !drivers[0].OnShift {
		t.Fatalf("drivers: %+v %v", drivers, err)
	}
	var trucks []struct {
		DriverOnShift bool `json:"driver_on_shift"`
	}
	if err := e.c.Get(ctx, "/trucks", &trucks); err != nil || !trucks[0].DriverOnShift {
		t.Fatalf("trucks: %+v %v", trucks, err)
	}
	if err := e.c.CancelDelivery(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := e.c.Post(ctx, "/drivers/"+me.ID+"/shift/end", nil, nil); err != nil {
		t.Fatalf("dispatch ends shift: %v", err)
	}
	if got := status(dc.PostLocations(ctx, truck.ID, ping())); got != 409 {
		t.Fatalf("position after dispatch ended the shift: %d, want 409", got)
	}
	// The simulator (a dispatcher) isn't a driver: never gated.
	if err := e.c.PostLocations(ctx, truck.ID, ping()); err != nil {
		t.Fatalf("dispatcher position: %v", err)
	}
}

// A tiny real JPEG and PNG (headers are what's checked).
var (
	jpegURL = "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, make([]byte, 64)...))
	pngURL  = "data:image/png;base64," + base64.StdEncoding.EncodeToString(append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...))
)

func TestProofOfDelivery(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	dc, truck, _ := e.newDriver(t, "Bayo", "+234 803 000 0012", "KF-21")
	id, d := e.newDeliveryOn(t, truck.ID)
	if err := dc.StartDelivery(ctx, id); err != nil {
		t.Fatal(err)
	}
	pickup, drop := d.Stops[0].ID, d.Stops[1].ID
	for _, step := range []func() error{
		func() error { return dc.ArriveAtStop(ctx, id, pickup) },
		func() error { return dc.CompleteStop(ctx, id, pickup) },
		func() error { return dc.ArriveAtStop(ctx, id, drop) },
	} {
		if err := step(); err != nil {
			t.Fatal(err)
		}
	}
	proofPath := "/deliveries/" + id + "/stops/" + drop + "/proof"

	// No proof: a driver can't complete the drop-off.
	if err := dc.CompleteStop(ctx, id, drop); status(err) != 409 || !strings.Contains(err.Error(), "who received it") {
		t.Fatalf("complete without proof: %v", err)
	}
	// Not on a pickup, not a fake image.
	if got := status(dc.Post(ctx, "/deliveries/"+id+"/stops/"+pickup+"/proof", map[string]any{"receiver_name": "Ada"}, nil)); got != 409 {
		t.Fatalf("proof on a pickup: %d", got)
	}
	if got := status(dc.Post(ctx, proofPath, map[string]any{"receiver_name": "Ada", "photo": "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString([]byte("<svg/>"))}, nil)); got != 400 {
		t.Fatalf("fake image: %d", got)
	}

	// Settings now want a photo too.
	st := *e.s.st()
	st.PodPhotoRequired = true
	e.s.settings.Store(&st)
	if err := dc.Post(ctx, proofPath, map[string]any{"receiver_name": "Ada", "signature": pngURL, "lat": 6.6, "lng": 3.35}, nil); err != nil {
		t.Fatal(err)
	}
	if err := dc.CompleteStop(ctx, id, drop); status(err) != 409 || !strings.Contains(err.Error(), "a photo") {
		t.Fatalf("complete without the required photo: %v", err)
	}
	if err := dc.Post(ctx, proofPath, map[string]any{"receiver_name": "Ada Obi", "photo": jpegURL, "signature": pngURL}, nil); err != nil {
		t.Fatal(err)
	}
	if err := dc.CompleteStop(ctx, id, drop); err != nil {
		t.Fatalf("complete with proof: %v", err)
	}
	// Done: no retake.
	if got := status(dc.Post(ctx, proofPath, map[string]any{"receiver_name": "Someone"}, nil)); got != 409 {
		t.Fatalf("retake after completing: %d", got)
	}

	// It shows on the stop, and the images come back.
	detail, err := e.c.GetDelivery(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Stops []struct {
			Proof *struct {
				ReceiverName string `json:"receiver_name"`
				HasPhoto     bool   `json:"has_photo"`
				HasSignature bool   `json:"has_signature"`
			} `json:"proof"`
		} `json:"stops"`
	}
	if err := e.c.Get(ctx, "/deliveries/"+id, &raw); err != nil || raw.Stops[1].Proof == nil || raw.Stops[1].Proof.ReceiverName != "Ada Obi" || !raw.Stops[1].Proof.HasPhoto {
		t.Fatalf("proof on stop: %+v %v", raw.Stops, err)
	}
	_ = detail
	get := func(path string) (int, string) {
		req, _ := http.NewRequest(http.MethodGet, e.srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+e.c.Token())
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		io.Copy(io.Discard, res.Body)
		return res.StatusCode, res.Header.Get("Content-Type")
	}
	if code, typ := get(proofPath + "/photo"); code != 200 || typ != "image/jpeg" {
		t.Fatalf("photo: %d %s", code, typ)
	}
	if code, typ := get(proofPath + "/signature"); code != 200 || typ != "image/png" {
		t.Fatalf("signature: %d %s", code, typ)
	}
	// The customer's link: by stop number, no sign-in.
	res, err := http.Get(e.srv.URL + "/track/" + d.Delivery.TrackingToken + "/stops/2/photo")
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("tracking photo: %v %v", res, err)
	}
	res.Body.Close()
	var track struct {
		Stops []struct {
			Proof *struct {
				ReceiverName string `json:"receiver_name"`
			} `json:"proof"`
		} `json:"stops"`
	}
	if err := e.c.Get(ctx, "/track/"+d.Delivery.TrackingToken, &track); err != nil || track.Stops[1].Proof == nil {
		t.Fatalf("tracking proof: %+v %v", track, err)
	}
	if res, _ := http.Get(e.srv.URL + "/track/" + strings.Repeat("a", 16) + "/stops/2/photo"); res.StatusCode != 404 {
		t.Fatalf("wrong token: %d", res.StatusCode)
	}
}

// A dispatcher stepping in for a dead phone completes without proof.
func TestDispatcherCompletesWithoutProof(t *testing.T) {
	e := setup(t)
	d, _ := e.newDelivery(t, "KF-22", "in_transit")
	finish(t, e, d)
}

// Every change is announced on the socket.
func TestChangesAnnounced(t *testing.T) {
	e := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(e.srv.URL, "http") + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: http.Header{
		"Origin": {"http://localhost:3000"}, "Authorization": {"Bearer " + e.c.Token()}}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	read := func() wsMessage {
		_, b, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var m wsMessage
		json.Unmarshal(b, &m)
		return m
	}
	read() // snapshot
	d, _ := e.newDelivery(t, "KF-23", "pending")
	for {
		m := read()
		if m.Type == "changed" && m.DeliveryID == "" && strings.Join(m.Topics, ",") == "deliveries,trucks" {
			break // the create
		}
	}
	if err := e.c.CancelDelivery(ctx, d.Delivery.ID); err != nil {
		t.Fatal(err)
	}
	for {
		m := read()
		if m.Type == "changed" && m.DeliveryID == d.Delivery.ID {
			break
		}
	}
	// A refused change says nothing.
	if err := e.c.CancelDelivery(ctx, d.Delivery.ID); status(err) != 409 {
		t.Fatal(err)
	}
	e.c.Post(ctx, "/trucks", map[string]any{"plate_number": "KF-24"}, nil)
	if m := read(); m.Type != "changed" || m.Topics[0] != "trucks" {
		t.Fatalf("after a refused cancel the next frame should be the truck: %+v", m)
	}
}

// One waiting proposal per delivery, enforced by the database.
func TestOneProposalPerDelivery(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	d, truck := e.newDelivery(t, "KF-25", "pending")
	agent := client.New(e.srv.URL).WithToken(e.agentKey)
	if err := agent.Post(ctx, "/proposals", proposalFor(d.Delivery.ID, truck.ID), nil); err != nil {
		t.Fatal(err)
	}
	if err := agent.Post(ctx, "/proposals", proposalFor(d.Delivery.ID, truck.ID), nil); status(err) != 409 {
		t.Fatalf("second proposal: %v", err)
	}
}
