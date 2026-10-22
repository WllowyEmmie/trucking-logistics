package api

import (
	"encoding/json"
	"net/http"
)

// writeJSON sends v as JSON. Error bodies ({"error": ...}) always leave with
// a "code" as well: a stable, machine-readable reason the Copilot and other
// clients can act on, where "error" is the sentence for people. Handlers set
// a specific code where one helps (truck_busy, wrong_status); otherwise it
// comes from the status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	if status >= 400 {
		v = withCode(status, v)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// Error codes. Specific ones first; the rest follow the status.
const (
	codeWrongStatus    = "wrong_status"     // the delivery's status doesn't allow it
	codeTruckBusy      = "truck_busy"       // the truck isn't available
	codeTruckNoDriver  = "truck_no_driver"  // the truck has no driver
	codeDriverOffShift = "driver_off_shift" // the driver isn't on shift
	codeOpenStops      = "open_stops"       // stops still to do
	codeAutoModeOff    = "auto_mode_off"    // auto mode is off in Settings
	codeAlreadyDecided = "already_decided"  // the proposal was approved or rejected already
	codeInProgress     = "in_progress"      // the same idempotent request is still running
	codeKeyReused      = "idempotency_key_reused"
)

var statusCodes = map[int]string{
	http.StatusBadRequest:            "bad_request",
	http.StatusUnauthorized:          "unauthenticated",
	http.StatusForbidden:             "forbidden",
	http.StatusNotFound:              "not_found",
	http.StatusMethodNotAllowed:      "method_not_allowed",
	http.StatusConflict:              "conflict",
	http.StatusRequestEntityTooLarge: "too_large",
	http.StatusUnsupportedMediaType:  "unsupported_media_type",
	http.StatusUnprocessableEntity:   "invalid",
	http.StatusTooManyRequests:       "rate_limited",
	http.StatusInternalServerError:   "internal",
	http.StatusBadGateway:            "upstream",
	http.StatusServiceUnavailable:    "unavailable",
	http.StatusGatewayTimeout:        "timeout",
}

func codeForStatus(status int) string {
	if c, ok := statusCodes[status]; ok {
		return c
	}
	if status >= 500 {
		return "internal"
	}
	return "error"
}

// withCode adds "code" to an error body that has none.
func withCode(status int, v any) any {
	switch m := v.(type) {
	case map[string]string:
		if _, ok := m["error"]; ok && m["code"] == "" {
			out := make(map[string]string, len(m)+1)
			for k, val := range m {
				out[k] = val
			}
			out["code"] = codeForStatus(status)
			return out
		}
	case map[string]any:
		if _, ok := m["error"]; ok {
			if _, has := m["code"]; !has {
				out := make(map[string]any, len(m)+1)
				for k, val := range m {
					out[k] = val
				}
				out["code"] = codeForStatus(status)
				return out
			}
		}
	}
	return v
}
