// Package metrics keeps a few counters and histograms in memory and writes
// them in the Prometheus text format (GET /metrics), so Grafana Agent,
// Prometheus or any compatible scraper can chart them. Hand-rolled to avoid
// a dependency for a dozen series.
package metrics

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"sync"
)

// Durations in seconds: 5 ms … 10 s.
var DefaultBuckets = []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}

type series struct {
	labels string // rendered: method="GET",route="/trucks"
	count  float64
	// Histograms only.
	sum     float64
	buckets []float64 // cumulative counts per bound
}

type family struct {
	name, help, kind string
	bounds           []float64
	series           map[string]*series
}

// Registry is safe for concurrent use.
type Registry struct {
	mu       sync.Mutex
	families map[string]*family
	order    []string
	gauges   []gaugeFunc
}

type gaugeFunc struct {
	name, help string
	fn         func() float64
}

func New() *Registry { return &Registry{families: map[string]*family{}} }

func (r *Registry) family(name, help, kind string, bounds []float64) *family {
	f := r.families[name]
	if f == nil {
		f = &family{name: name, help: help, kind: kind, bounds: bounds, series: map[string]*series{}}
		r.families[name] = f
		r.order = append(r.order, name)
	}
	return f
}

// Labels renders label pairs in a fixed order: Labels("method", "GET", "route", "/x").
func Labels(kv ...string) string {
	var b strings.Builder
	for i := 0; i+1 < len(kv); i += 2 {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%s=%q", kv[i], kv[i+1])
	}
	return b.String()
}

// Inc adds one to a counter.
func (r *Registry) Inc(name, help, labels string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f := r.family(name, help, "counter", nil)
	s := f.series[labels]
	if s == nil {
		s = &series{labels: labels}
		f.series[labels] = s
	}
	s.count++
}

// Observe records one value in a histogram.
func (r *Registry) Observe(name, help, labels string, v float64, bounds []float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f := r.family(name, help, "histogram", bounds)
	s := f.series[labels]
	if s == nil {
		s = &series{labels: labels, buckets: make([]float64, len(f.bounds))}
		f.series[labels] = s
	}
	s.count++
	s.sum += v
	for i, b := range f.bounds {
		if v <= b {
			s.buckets[i]++
		}
	}
}

// Gauge reports fn's value at each scrape.
func (r *Registry) Gauge(name, help string, fn func() float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gauges = append(r.gauges, gaugeFunc{name, help, fn})
}

func fmtFloat(v float64) string {
	if math.IsInf(v, 1) {
		return "+Inf"
	}
	return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.6f", v), "0"), ".")
}

func withLabel(labels, extra string) string {
	if labels == "" {
		return "{" + extra + "}"
	}
	return "{" + labels + "," + extra + "}"
}

func braces(labels string) string {
	if labels == "" {
		return ""
	}
	return "{" + labels + "}"
}

// Write prints everything in the Prometheus text exposition format.
func (r *Registry) Write(w io.Writer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, name := range r.order {
		f := r.families[name]
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", f.name, f.help, f.name, f.kind)
		keys := make([]string, 0, len(f.series))
		for k := range f.series {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			s := f.series[k]
			if f.kind == "counter" {
				fmt.Fprintf(w, "%s%s %s\n", f.name, braces(s.labels), fmtFloat(s.count))
				continue
			}
			for i, b := range f.bounds {
				fmt.Fprintf(w, "%s_bucket%s %s\n", f.name, withLabel(s.labels, fmt.Sprintf("le=%q", fmtFloat(b))), fmtFloat(s.buckets[i]))
			}
			fmt.Fprintf(w, "%s_bucket%s %s\n", f.name, withLabel(s.labels, `le="+Inf"`), fmtFloat(s.count))
			fmt.Fprintf(w, "%s_sum%s %s\n", f.name, braces(s.labels), fmtFloat(s.sum))
			fmt.Fprintf(w, "%s_count%s %s\n", f.name, braces(s.labels), fmtFloat(s.count))
		}
	}
	for _, g := range r.gauges {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s gauge\n%s %s\n", g.name, g.help, g.name, g.name, fmtFloat(g.fn()))
	}
}

// Default is the process's registry: the API's request metrics and the
// database query tracer both write here.
var Default = New()
