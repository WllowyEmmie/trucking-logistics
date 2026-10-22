package api

import (
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Rate limits for signed-in callers, per person (or the agent's key), so
// one runaway client (a stuck retry loop, a misbehaving Copilot turn, a
// leaked token) can't hammer the API. The public routes have their own,
// stricter throttles (booking.go, auth.go). In memory: one API process.
//
// A token bucket per caller and kind: `per` requests a minute, with bursts
// up to `burst`. Over the limit: 429, code rate_limited, and Retry-After.

type limit struct {
	per   float64 // per minute
	burst float64
}

type staffLimits struct {
	read, write map[string]limit // by role
	// POST /locations: position pings, their own bucket. A phone sends one
	// every few seconds; the simulator sends them for every truck as the
	// dispatcher.
	ping map[string]limit
}

func defaultStaffLimits() staffLimits {
	return staffLimits{
		read: map[string]limit{
			roleDispatcher: {per: 600, burst: 120},
			roleDriver:     {per: 240, burst: 60},
			roleAgent:      {per: 1200, burst: 240},
		},
		write: map[string]limit{
			// Generous: the seed script and bulk work in the console come in bursts.
			roleDispatcher: {per: 300, burst: 150},
			// Stop updates and proof photos.
			roleDriver: {per: 120, burst: 30},
			roleAgent:  {per: 300, burst: 60},
		},
		ping: map[string]limit{
			roleDispatcher: {per: 1200, burst: 300},
			roleDriver:     {per: 120, burst: 30},
		},
	}
}

type bucket struct {
	tokens float64
	at     time.Time
}

type limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	calls   int
}

func newLimiter() *limiter { return &limiter{buckets: map[string]*bucket{}} }

// take spends one token from key's bucket. ok false: wait `wait`.
func (l *limiter) take(key string, lim limit, now time.Time) (ok bool, wait time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	if l.calls%1000 == 0 {
		// Forget callers idle long enough to be full again anyway.
		for k, b := range l.buckets {
			if now.Sub(b.at) > 10*time.Minute {
				delete(l.buckets, k)
			}
		}
	}
	b := l.buckets[key]
	if b == nil {
		b = &bucket{tokens: lim.burst, at: now}
		l.buckets[key] = b
	}
	rate := lim.per / 60 // per second
	b.tokens = math.Min(lim.burst, b.tokens+now.Sub(b.at).Seconds()*rate)
	b.at = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, time.Duration((1 - b.tokens) / rate * float64(time.Second))
}

func (s *Server) staffRateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := principalFrom(r.Context())
		if p == nil {
			next.ServeHTTP(w, r)
			return
		}
		kind, table := "read", s.limits.read
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/locations":
			kind, table = "ping", s.limits.ping
		case r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions:
			kind, table = "write", s.limits.write
		}
		lim, ok := table[p.Role]
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		key := p.Role + ":" + p.ID.String() + ":" + kind
		if ok, wait := s.limiter.take(key, lim, time.Now()); !ok {
			secs := int(math.Ceil(wait.Seconds()))
			if secs < 1 {
				secs = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(secs))
			writeJSON(w, http.StatusTooManyRequests, map[string]string{
				"error": "too many requests; try again in " + strconv.Itoa(secs) + " s"})
			return
		}
		next.ServeHTTP(w, r)
	})
}
