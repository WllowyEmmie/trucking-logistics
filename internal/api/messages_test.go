package api

import (
	"io"
	"log/slog"
	"reflect"
	"testing"

	"github.com/google/uuid"
)

// Sending or reading a message only refreshes the consoles' messages, not
// their trucks and drivers.
func TestMessagesAnnounceOnlyMessages(t *testing.T) {
	for _, path := range []string{
		"/drivers/0b7d1c1e-6a51-4c55-9a0e-4c1f2e8d9a10/messages",
		"/drivers/0b7d1c1e-6a51-4c55-9a0e-4c1f2e8d9a10/messages/read",
		"/auth/me/messages",
		"/auth/me/messages/read",
	} {
		topics, _ := changedBy(path)
		if !reflect.DeepEqual(topics, []string{"messages"}) {
			t.Errorf("%s announces %v, want [messages]", path, topics)
		}
	}
	if topics, _ := changedBy("/drivers/x/pin"); !reflect.DeepEqual(topics, []string{"drivers", "trucks"}) {
		t.Errorf("a PIN change announces %v", topics)
	}
}

// A message reaches only its driver's phones, and consoles never get
// driver frames.
func TestHubToDriverReachesOnlyThatDriver(t *testing.T) {
	h := newHub(slog.New(slog.NewTextHandler(io.Discard, nil)))
	alice, bob := uuid.New(), uuid.New()
	a1 := &wsClient{send: make(chan wsMessage, 1)}
	a2 := &wsClient{send: make(chan wsMessage, 1)}
	b := &wsClient{send: make(chan wsMessage, 1)}
	console := &wsClient{send: make(chan wsMessage, 1)}
	h.addDriver(alice, a1)
	h.addDriver(alice, a2)
	h.addDriver(bob, b)
	h.add(console)

	h.toDriver(alice, wsMessage{Type: "message", Message: "hi"})
	for _, c := range []*wsClient{a1, a2} {
		select {
		case m := <-c.send:
			if m.Type != "message" || m.Message != "hi" {
				t.Errorf("got %+v", m)
			}
		default:
			t.Error("one of alice's phones got nothing")
		}
	}
	if len(b.send) != 0 || len(console.send) != 0 {
		t.Error("the message went beyond alice's phones")
	}

	h.broadcast(wsMessage{Type: "changed", Topics: []string{"trucks"}})
	if len(a1.send) != 0 || len(b.send) != 0 {
		t.Error("a driver's phone got a console broadcast")
	}

	h.removeDriver(alice, a1)
	if _, open := <-a1.send; open {
		t.Error("removing a phone should close its channel")
	}
	h.removeDriver(alice, a2)
	if _, ok := h.drivers[alice]; ok {
		t.Error("a driver with no phones left should be forgotten")
	}
}
