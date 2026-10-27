package db

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Message is one line in a driver's thread with dispatch.
type Message struct {
	ID         uuid.UUID  `json:"id"`
	DriverID   uuid.UUID  `json:"driver_id"`
	FromDriver bool       `json:"from_driver"`
	Sender     string     `json:"sender"`
	Body       string     `json:"body"`
	CreatedAt  time.Time  `json:"created_at"`
	ReadAt     *time.Time `json:"read_at"`
}

// MessageThread is a driver's conversation as the console lists it: the
// latest message and how many of the driver's are unread.
type MessageThread struct {
	DriverID   uuid.UUID `json:"driver_id"`
	DriverName string    `json:"driver_name"`
	Last       Message   `json:"last"`
	Unread     int       `json:"unread"`
}

const messageCols = `id, driver_id, from_driver, sender, body, created_at, read_at`

// AddMessage writes a message into a driver's thread.
func (q *Queries) AddMessage(ctx context.Context, driverID uuid.UUID, fromDriver bool, sender, body string) (Message, error) {
	var m Message
	err := q.db.QueryRow(ctx, `
		INSERT INTO messages (driver_id, from_driver, sender, body) VALUES ($1, $2, $3, $4)
		RETURNING `+messageCols, driverID, fromDriver, sender, body).
		Scan(&m.ID, &m.DriverID, &m.FromDriver, &m.Sender, &m.Body, &m.CreatedAt, &m.ReadAt)
	return m, err
}

// ThreadMessages is a driver's thread, oldest first: the latest `limit`.
func (q *Queries) ThreadMessages(ctx context.Context, driverID uuid.UUID, limit int) ([]Message, error) {
	rows, err := q.db.Query(ctx, `
		SELECT `+messageCols+` FROM (
			SELECT `+messageCols+` FROM messages WHERE driver_id = $1 ORDER BY created_at DESC, id DESC LIMIT $2
		) recent ORDER BY created_at, id`, driverID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.DriverID, &m.FromDriver, &m.Sender, &m.Body, &m.CreatedAt, &m.ReadAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MarkThreadRead marks the other side's messages read: the driver's when a
// dispatcher opens the thread (fromDriver true), dispatch's when the driver does.
func (q *Queries) MarkThreadRead(ctx context.Context, driverID uuid.UUID, fromDriver bool) (int64, error) {
	tag, err := q.db.Exec(ctx, `
		UPDATE messages SET read_at = now()
		WHERE driver_id = $1 AND from_driver = $2 AND read_at IS NULL`, driverID, fromDriver)
	return tag.RowsAffected(), err
}

// MessageThreads lists every driver who has a thread, latest message first.
func (q *Queries) MessageThreads(ctx context.Context) ([]MessageThread, error) {
	rows, err := q.db.Query(ctx, `
		SELECT DISTINCT ON (m.driver_id)
			m.driver_id, d.name,
			m.id, m.driver_id, m.from_driver, m.sender, m.body, m.created_at, m.read_at,
			(SELECT count(*) FROM messages u WHERE u.driver_id = m.driver_id AND u.from_driver AND u.read_at IS NULL)
		FROM messages m JOIN drivers d ON d.id = m.driver_id
		ORDER BY m.driver_id, m.created_at DESC, m.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MessageThread{}
	for rows.Next() {
		var t MessageThread
		m := &t.Last
		if err := rows.Scan(&t.DriverID, &t.DriverName, &m.ID, &m.DriverID, &m.FromDriver, &m.Sender, &m.Body, &m.CreatedAt, &m.ReadAt, &t.Unread); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Newest conversation first.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Last.CreatedAt.After(out[j-1].Last.CreatedAt); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out, nil
}

// UnreadForDriver counts dispatch's messages the driver hasn't opened.
func (q *Queries) UnreadForDriver(ctx context.Context, driverID uuid.UUID) (int, error) {
	var n int
	err := q.db.QueryRow(ctx, `SELECT count(*) FROM messages WHERE driver_id = $1 AND NOT from_driver AND read_at IS NULL`, driverID).Scan(&n)
	return n, err
}
