package api

import (
	"crypto/subtle"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/WllowyEmmie/trucking-logistics/internal/metrics"
)

// ---- Metrics (GET /metrics) ----

// observe counts every request by method, route pattern and status class,
// and times it. The route is chi's pattern (/deliveries/{id}), not the raw
// path, so ids don't explode the series. The socket and the agent proxy are
// counted but not timed: they stay open for minutes.
func (s *Server) observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		route := "unmatched"
		if rc := chi.RouteContext(r.Context()); rc != nil && rc.RoutePattern() != "" {
			route = rc.RoutePattern()
		}
		class := strconv.Itoa(sw.status/100) + "xx"
		metrics.Default.Inc("dispatch_http_requests_total", "HTTP requests by route and status class.",
			metrics.Labels("method", r.Method, "route", route, "status", class))
		if route != "/ws" && !strings.HasPrefix(route, "/agent") {
			metrics.Default.Observe("dispatch_http_request_seconds", "HTTP request durations.",
				metrics.Labels("method", r.Method, "route", route), time.Since(start).Seconds(), metrics.DefaultBuckets)
		}
	})
}

type statusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.status, w.wroteHeader = code, true
	}
	w.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController (and the socket upgrade) reach the
// real writer.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// metricsHandler is GET /metrics, Prometheus text format. With
// METRICS_TOKEN set it needs that bearer token; without, a signed-in
// dispatcher or the agent's key.
func (s *Server) metricsHandler(w http.ResponseWriter, r *http.Request) {
	allowed := false
	if s.cfg.MetricsToken != "" {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		allowed = subtle.ConstantTimeCompare([]byte(got), []byte(s.cfg.MetricsToken)) == 1
	}
	if p := principalFrom(r.Context()); p != nil && (p.Role == roleDispatcher || p.Role == roleAgent) {
		allowed = true
	}
	if !allowed {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "metrics need METRICS_TOKEN, a dispatcher or the agent's key"})
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	metrics.Default.Write(w)
}

// registerGauges adds the values read at scrape time. Once per process.
var gaugesOnce sync.Once

func (s *Server) registerGauges() {
	gaugesOnce.Do(func() {
		metrics.Default.Gauge("dispatch_ws_clients", "Open live-update sockets (consoles and the agent).",
			func() float64 { return float64(s.hub.count()) })
		metrics.Default.Gauge("dispatch_db_pool_connections", "Database connections open.",
			func() float64 { return float64(s.pool.Stat().TotalConns()) })
	})
}
