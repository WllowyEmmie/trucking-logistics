package api

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// agentProxy serves /agent/* by forwarding to the agent running beside the
// API (AGENT_INTERNAL_URL, e.g. http://127.0.0.1:8200), so one host serves
// both: the website forwards /agent there, cookie included, and the agent
// checks the sign-in itself. Nil when AGENT_INTERNAL_URL isn't set.
func (s *Server) agentProxy() http.Handler {
	if s.cfg.AgentInternalURL == "" {
		return nil
	}
	target, err := url.Parse(s.cfg.AgentInternalURL)
	if err != nil {
		s.logger.Error("AGENT_INTERNAL_URL is not a URL; /agent is off", "err", err)
		return nil
	}
	p := httputil.NewSingleHostReverseProxy(target)
	base := p.Director
	p.Director = func(r *http.Request) {
		base(r)
		r.URL.Path = "/" + strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/agent"), "/")
		r.URL.RawPath = ""
		r.Host = target.Host
	}
	p.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		s.logger.Warn("agent unreachable", "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "the agent isn't running right now"})
	}
	return p
}
