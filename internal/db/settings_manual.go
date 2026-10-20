package db

import (
	"context"
	"encoding/json"
	"time"
)

// SavedSettings is the settings row: the JSON as stored, and who saved it.
type SavedSettings struct {
	Data      json.RawMessage
	UpdatedAt time.Time
	UpdatedBy string
}

func (q *Queries) GetSettings(ctx context.Context) (SavedSettings, error) {
	var s SavedSettings
	err := q.db.QueryRow(ctx, `SELECT data, updated_at, updated_by FROM settings WHERE id = 1`).
		Scan(&s.Data, &s.UpdatedAt, &s.UpdatedBy)
	return s, err
}

func (q *Queries) SaveSettings(ctx context.Context, data json.RawMessage, by string) (time.Time, error) {
	var at time.Time
	err := q.db.QueryRow(ctx, `
		INSERT INTO settings (id, data, updated_at, updated_by) VALUES (1, $1, now(), $2)
		ON CONFLICT (id) DO UPDATE SET data = EXCLUDED.data, updated_at = now(), updated_by = EXCLUDED.updated_by
		RETURNING updated_at`, data, by).Scan(&at)
	return at, err
}

// OutsideArea counts what a new service area would leave out: stops still
// to be done on open deliveries, and trucks whose last position is outside.
func (q *Queries) OutsideArea(ctx context.Context, minLat, maxLat, minLng, maxLng float64) (stops, trucks int64, err error) {
	err = q.db.QueryRow(ctx, `
		SELECT
		  (SELECT COUNT(*) FROM delivery_stops s JOIN deliveries d ON d.id = s.delivery_id
		    WHERE d.status IN ('pending', 'assigned', 'in_transit')
		      AND s.status NOT IN ('completed', 'skipped')
		      AND NOT (ST_Y(s.position::geometry) BETWEEN $1 AND $2 AND ST_X(s.position::geometry) BETWEEN $3 AND $4)),
		  (SELECT COUNT(*) FROM trucks t
		    WHERE t.last_position IS NOT NULL
		      AND NOT (ST_Y(t.last_position::geometry) BETWEEN $1 AND $2 AND ST_X(t.last_position::geometry) BETWEEN $3 AND $4))`,
		minLat, maxLat, minLng, maxLng).Scan(&stops, &trucks)
	return stops, trucks, err
}
