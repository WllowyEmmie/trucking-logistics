package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

type healthReport struct {
	Status string `json:"status"` // ok | degraded | down
	DB     string `json:"db"`     // ok | down
	Agent  string `json:"agent"`  // ok | degraded | down | off (not run beside the API)
	Uptime int64  `json:"uptime_s"`
}

var started = time.Now()

// health: 200 while the database answers (degraded if the agent doesn't),
// 503 when it doesn't. Render restarts the service on a failing check, so
// the agent alone never fails it: the agent restarts itself (deploy/start.sh).
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	rep := healthReport{Status: "ok", DB: "ok", Agent: s.agentHealth(r.Context()), Uptime: int64(time.Since(started).Seconds())}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.pool.Ping(ctx); err != nil {
		rep.Status, rep.DB = "down", "down"
		writeJSON(w, http.StatusServiceUnavailable, rep)
		return
	}
	if rep.Agent == "down" || rep.Agent == "degraded" {
		rep.Status = "degraded"
	}
	writeJSON(w, http.StatusOK, rep)
}

// The agent's own /health, cached for 15 s: health checks come every few
// seconds and the agent's check calls back into this API.
var agentHealthCache struct {
	sync.Mutex
	at    time.Time
	value string
}

func (s *Server) agentHealth(ctx context.Context) string {
	if s.cfg.AgentInternalURL == "" {
		return "off"
	}
	agentHealthCache.Lock()
	defer agentHealthCache.Unlock()
	if time.Since(agentHealthCache.at) < 15*time.Second && agentHealthCache.value != "" {
		return agentHealthCache.value
	}
	value := "down"
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(s.cfg.AgentInternalURL, "/")+"/health", nil)
	if err == nil {
		if res, err := http.DefaultClient.Do(req); err == nil {
			var body struct {
				Status string `json:"status"`
			}
			_ = json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&body)
			res.Body.Close()
			switch {
			case res.StatusCode == http.StatusOK && body.Status == "ok":
				value = "ok"
			case res.StatusCode < 500:
				value = "degraded"
			}
		}
	}
	agentHealthCache.at, agentHealthCache.value = time.Now(), value
	return value
}
