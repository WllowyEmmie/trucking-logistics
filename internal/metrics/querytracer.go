package metrics

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// QueryTracer times every database query (pgx's tracing hook): a
// histogram of durations, and a log line plus a counter for each query
// slower than Slow. The SQL is logged without its arguments, which can
// hold personal data.
type QueryTracer struct {
	Reg    *Registry
	Logger *slog.Logger
	Slow   time.Duration
}

type queryStartKey struct{}

type queryStart struct {
	at  time.Time
	sql string
}

func (t *QueryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, queryStartKey{}, queryStart{at: time.Now(), sql: data.SQL})
}

func (t *QueryTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	st, ok := ctx.Value(queryStartKey{}).(queryStart)
	if !ok {
		return
	}
	d := time.Since(st.at)
	name := queryName(st.sql)
	t.Reg.Observe("dispatch_db_query_seconds", "Database query durations.", Labels("query", name), d.Seconds(), DefaultBuckets)
	if data.Err != nil {
		t.Reg.Inc("dispatch_db_query_errors_total", "Database queries that returned an error.", Labels("query", name))
	}
	if t.Slow > 0 && d >= t.Slow {
		t.Reg.Inc("dispatch_db_slow_queries_total", "Database queries slower than the slow threshold.", Labels("query", name))
		if t.Logger != nil {
			t.Logger.Warn("slow query", "query", name, "ms", d.Milliseconds(), "sql", oneLine(st.sql, 200))
		}
	}
}

// queryName is sqlc's "-- name: X" when present, else the first word
// (SELECT, UPDATE...), so the label set stays small.
func queryName(sql string) string {
	s := strings.TrimSpace(sql)
	if strings.HasPrefix(s, "-- name: ") {
		rest := strings.TrimPrefix(s, "-- name: ")
		if f := strings.Fields(rest); len(f) > 0 {
			return f[0]
		}
	}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "--") {
			continue
		}
		if f := strings.Fields(line); len(f) > 0 {
			return strings.ToUpper(f[0])
		}
	}
	return "unknown"
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
