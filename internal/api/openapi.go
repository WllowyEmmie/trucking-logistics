package api

import (
	_ "embed"
	"net/http"
)

// openapi.json describes every route, its sign-in, body and errors.
// TestOpenAPICoversEveryRoute fails when a route is added or removed
// without it, so the description (and anything generated from it: Copilot
// tool definitions, frontend types) can't drift from the router.
//
//go:embed openapi.json
var openAPISpec []byte

func (s *Server) openAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Write(openAPISpec)
}
