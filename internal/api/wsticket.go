package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"sync"
	"time"
)

// Socket passes. When the website and the API are on different hosts (the
// website on Vercel, the API on Render), the browser can't send its session
// cookie to the API's socket: the cookie belongs to the website's address,
// and Vercel can't forward a socket. So the console first asks for a pass
// through the website's /api (cookie and all), then opens the socket on the
// API with ?ticket=. A pass works once, within a minute, for whoever got it.

const ticketTTL = time.Minute

type ticketStore struct {
	mu sync.Mutex
	m  map[string]ticketEntry
}

type ticketEntry struct {
	p   principal
	exp time.Time
}

func newTicketStore() *ticketStore { return &ticketStore{m: map[string]ticketEntry{}} }

func (t *ticketStore) issue(p principal, now time.Time) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(b)
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, e := range t.m {
		if now.After(e.exp) {
			delete(t.m, k)
		}
	}
	t.m[tok] = ticketEntry{p: p, exp: now.Add(ticketTTL)}
	return tok, nil
}

// take returns the pass's holder and forgets it: it works once.
func (t *ticketStore) take(tok string, now time.Time) (*principal, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.m[tok]
	if !ok {
		return nil, false
	}
	delete(t.m, tok)
	if now.After(e.exp) {
		return nil, false
	}
	return &e.p, true
}

// wsTicket is GET /ws/ticket (dispatchers, drivers): a one-minute pass for
// /ws or /ws/driver. The pass carries who asked, so a driver's opens only
// /ws/driver.
func (s *Server) wsTicket(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	tok, err := s.tickets.issue(*p, time.Now())
	if err != nil {
		s.respondErr(w, err, "issue socket pass", "could not open live updates")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"ticket": tok, "expires_in": int(ticketTTL.Seconds())})
}

// ticketAuth signs in a socket request that carries a valid ?ticket=.
func (s *Server) ticketAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tok := r.URL.Query().Get("ticket"); tok != "" && principalFrom(r.Context()) == nil {
			if p, ok := s.tickets.take(tok, time.Now()); ok {
				r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, p))
			}
		}
		next.ServeHTTP(w, r)
	})
}
