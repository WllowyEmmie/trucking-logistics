package api

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5/middleware"
)

// Live status: after any change goes through, every open console hears what
// changed on the /ws socket ({"type":"changed","topics":[…],"delivery_id":…})
// and refetches only that. The message carries no data: a console can't be
// told something its own sign-in couldn't read.
//
// Done as one middleware rather than a call in each handler, so a new
// endpoint can't forget it.
func (s *Server) announceChanges(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		if ww.Status() < 200 || ww.Status() >= 300 {
			return
		}
		topics, deliveryID := changedBy(r.URL.Path)
		if len(topics) > 0 {
			s.hub.broadcast(wsMessage{Type: "changed", Topics: topics, DeliveryID: deliveryID})
		}
	})
}

// changedBy maps a write's path to what it changed.
func changedBy(path string) (topics []string, deliveryID string) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	switch parts[0] {
	case "deliveries":
		// Any delivery change can move a truck (assign, finish, fail).
		topics = []string{"deliveries", "trucks"}
		if len(parts) > 1 {
			deliveryID = parts[1]
		}
	case "trucks":
		topics = []string{"trucks"}
	case "drivers":
		topics = []string{"drivers", "trucks"}
		if len(parts) > 2 && parts[2] == "messages" {
			topics = []string{"messages"}
		}
	case "proposals":
		topics = []string{"proposals", "deliveries", "trucks"}
	case "config":
		topics = []string{"config"}
	case "supervisor":
		// A pairing changes a truck's driver; an assignment a delivery too.
		topics = []string{"supervisor", "deliveries", "trucks", "drivers", "proposals"}
	case "auth":
		// A shift changes what others see: starting one, or a driver signing
		// in (which starts one). Dispatchers signing in and out don't.
		if (len(parts) > 2 && parts[1] == "me" && parts[2] == "shift") || (len(parts) > 1 && parts[1] == "driver-login") {
			topics = []string{"drivers", "trucks"}
		}
		if len(parts) > 2 && parts[1] == "me" && parts[2] == "pin" {
			topics = []string{"drivers", "trucks"}
		}
		if len(parts) > 2 && parts[1] == "me" && parts[2] == "messages" {
			topics = []string{"messages"}
		}
	}
	// /locations is announced by the position frames themselves.
	return topics, deliveryID
}
