package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A driver still on the default PIN can only choose a new one (and sign
// out, and read who they are); everything else is refused.
func TestPinGate(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	gate := pinGate(ok)
	run := func(p *principal, path string) int {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		if p != nil {
			r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, p))
		}
		w := httptest.NewRecorder()
		gate.ServeHTTP(w, r)
		return w.Code
	}
	fresh := &principal{Role: roleDriver, MustChangePIN: true}
	for path, want := range map[string]int{
		"/trucks":      http.StatusForbidden,
		"/deliveries":  http.StatusForbidden,
		"/locations":   http.StatusForbidden,
		"/auth/me":     http.StatusOK,
		"/auth/me/pin": http.StatusOK,
		"/auth/logout": http.StatusOK,
		"/config":      http.StatusOK,
	} {
		if got := run(fresh, path); got != want {
			t.Errorf("%s on the default PIN: %d, want %d", path, got, want)
		}
	}
	if got := run(&principal{Role: roleDriver}, "/trucks"); got != http.StatusOK {
		t.Errorf("a driver with their own PIN was refused: %d", got)
	}
	if got := run(nil, "/trucks"); got != http.StatusOK {
		t.Errorf("no sign-in is requireRole's to refuse, not the gate's: %d", got)
	}
}
