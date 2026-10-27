package metrics

import (
	"strings"
	"testing"
)

func TestWriteFormat(t *testing.T) {
	r := New()
	r.Inc("x_total", "Things.", Labels("route", "/a", "code", "2xx"))
	r.Inc("x_total", "Things.", Labels("route", "/a", "code", "2xx"))
	r.Observe("y_seconds", "Time.", Labels("route", "/a"), 0.03, []float64{.01, .05, .1})
	r.Gauge("z", "Now.", func() float64 { return 3 })
	var b strings.Builder
	r.Write(&b)
	out := b.String()
	for _, want := range []string{
		"# TYPE x_total counter",
		`x_total{route="/a",code="2xx"} 2`,
		`y_seconds_bucket{route="/a",le="0.01"} 0`,
		`y_seconds_bucket{route="/a",le="0.05"} 1`,
		`y_seconds_bucket{route="/a",le="+Inf"} 1`,
		`y_seconds_sum{route="/a"} 0.03`,
		`y_seconds_count{route="/a"} 1`,
		"# TYPE z gauge\nz 3",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestQueryName(t *testing.T) {
	cases := map[string]string{
		"-- name: GetDelivery :one\nSELECT 1":  "GetDelivery",
		"\n  update deliveries set x = 1":      "UPDATE",
		"-- a comment\nSELECT set_config('a')": "SELECT",
	}
	for in, want := range cases {
		if got := queryName(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}
