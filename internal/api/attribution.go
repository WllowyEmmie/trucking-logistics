package api

import (
	"context"
	"net/http"
	"regexp"

	"github.com/jackc/pgx/v5"
)

// Where a change came from. Recorded on every delivery event (see the
// attribution migration) and in logs.
const (
	viaConsole    = "console"    // a dispatcher clicking in the console
	viaCopilot    = "copilot"    // a dispatcher, through the Copilot chat
	viaSupervisor = "supervisor" // auto mode / the agent's service key
	viaDriver     = "driver"     // the driver app
	viaBooking    = "booking"    // the public booking page and Paystack
	viaSystem     = "system"     // nobody signed in (simulator, jobs)
)

// Headers a dispatcher's client sends to say the change went through the
// Copilot, and in which conversation. They only label the change: the
// permissions are the signed-in person's either way.
const (
	headerVia    = "X-Via"
	headerThread = "X-Copilot-Thread"
)

type attribution struct {
	Actor  string
	Via    string
	Thread string
}

type attributionKey struct{}

var threadID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// attributionOf works out who and what to record for a request.
func attributionOf(r *http.Request) attribution {
	a := attribution{Actor: actorName(r), Via: viaSystem}
	p := principalFrom(r.Context())
	switch {
	case p == nil:
		if isBookingPath(r.URL.Path) {
			a.Via = viaBooking
			a.Actor = "customer"
		}
	case p.Role == roleAgent:
		a.Via = viaSupervisor
	case p.Role == roleDriver:
		a.Via = viaDriver
	case p.Role == roleDispatcher:
		a.Via = viaConsole
		if r.Header.Get(headerVia) == viaCopilot {
			a.Via = viaCopilot
			if t := r.Header.Get(headerThread); threadID.MatchString(t) {
				a.Thread = t
			}
		}
	}
	return a
}

func isBookingPath(path string) bool {
	return path == "/book" || path == "/paystack/webhook" ||
		len(path) > len("/book/") && path[:len("/book/")] == "/book/"
}

// attribute stores the request's attribution in its context. It runs after
// authenticate.
func (s *Server) attribute(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := attributionOf(r)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), attributionKey{}, a)))
	})
}

func attributionFrom(ctx context.Context) attribution {
	if a, ok := ctx.Value(attributionKey{}).(attribution); ok {
		return a
	}
	return attribution{Actor: "unknown", Via: viaSystem}
}

// beginTx opens a transaction labelled with the request's attribution, so
// the delivery events it writes record who and how. Use it instead of
// s.pool.Begin for anything that changes deliveries.
func (s *Server) beginTx(ctx context.Context) (pgx.Tx, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	a := attributionFrom(ctx)
	if _, err := tx.Exec(ctx,
		`SELECT set_config('app.actor', $1, true), set_config('app.via', $2, true), set_config('app.thread', $3, true)`,
		a.Actor, a.Via, a.Thread); err != nil {
		tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}
