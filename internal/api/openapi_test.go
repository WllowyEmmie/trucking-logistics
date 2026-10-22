package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/WllowyEmmie/trucking-logistics/internal/config"
)

// The OpenAPI description and the router list exactly the same routes.
func TestOpenAPICoversEveryRoute(t *testing.T) {
	var spec struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(openAPISpec, &spec); err != nil {
		t.Fatalf("openapi.json doesn't parse: %v", err)
	}
	documented := map[string]bool{}
	for path, ops := range spec.Paths {
		for method := range ops {
			documented[strings.ToUpper(method)+" "+path] = true
		}
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{AgentAPIKey: "k"}
	s := &Server{logger: logger, cfg: cfg, limits: defaultStaffLimits(), limiter: newLimiter(), tickets: newTicketStore()}
	routes, ok := s.Routes().(chi.Routes)
	if !ok {
		t.Fatal("Routes() isn't a chi router")
	}
	served := map[string]bool{}
	// chi writes "/a/*" for sub-routers' leftovers; none here but the agent proxy.
	trailing := regexp.MustCompile(`/\*$`)
	err := chi.Walk(routes, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if strings.HasPrefix(route, "/agent") { // the agent's own API, proxied
			return nil
		}
		served[method+" "+trailing.ReplaceAllString(route, "")] = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	var missing, stale []string
	for r := range served {
		if !documented[r] {
			missing = append(missing, r)
		}
	}
	for r := range documented {
		if !served[r] {
			stale = append(stale, r)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)
	if len(missing) > 0 {
		t.Errorf("routes not in openapi.json: %v", missing)
	}
	if len(stale) > 0 {
		t.Errorf("openapi.json lists routes the router doesn't have: %v", stale)
	}
}
