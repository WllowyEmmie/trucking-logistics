package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"regexp"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
)

// Idempotency keys: a client that may send a change twice (a retry after a
// timeout, a double click, the Copilot resuming an approval) puts the same
// Idempotency-Key header on each try. The first try runs; later ones get its
// response back, marked Idempotent-Replayed: true, without running again.
//
// Keys belong to whoever sent them, last a day, and are optional: requests
// without one behave as before. A key sent again with a different method,
// path or body is refused (422), and one whose first request is still
// running gets 409 in_progress.
const (
	headerIdempotencyKey = "Idempotency-Key"
	headerReplayed       = "Idempotent-Replayed"
	idempotencyTTL       = 24 * time.Hour
	// Bodies above this aren't hashed for a key (proof photos): the request
	// runs as if it had no key.
	idempotencyMaxBody = 1 << 20
)

var idempotencyKeyFormat = regexp.MustCompile(`^[A-Za-z0-9_.:-]{8,128}$`)

// Last sweep of expired keys, as unix seconds.
var lastIdempotencySweep atomic.Int64

func (s *Server) idempotent(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get(headerIdempotencyKey)
		p := principalFrom(r.Context())
		if key == "" || p == nil || r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		if !idempotencyKeyFormat.MatchString(key) {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "Idempotency-Key must be 8-128 letters, digits or _ . : -"})
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, idempotencyMaxBody+1))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "could not read the request"})
			return
		}
		if len(body) > idempotencyMaxBody {
			r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), r.Body))
			next.ServeHTTP(w, r)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		sum := sha256.Sum256(body)
		hash := hex.EncodeToString(sum[:])
		scope := p.Role + ":" + p.ID.String()
		ctx := r.Context()
		s.sweepIdempotencyKeys(ctx)

		tag, err := s.pool.Exec(ctx, `
			INSERT INTO idempotency_keys (scope, key, method, path, body_hash)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (scope, key) DO NOTHING`, scope, key, r.Method, r.URL.Path, hash)
		if err != nil {
			s.logger.Error("idempotency claim", "err", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not process the request"})
			return
		}
		if tag.RowsAffected() == 0 {
			s.replayIdempotent(w, r, scope, key, hash)
			return
		}

		rec := &recordingWriter{ResponseWriter: w, status: http.StatusOK}
		completed := false
		defer func() {
			// A 5xx (or a panic) leaves nothing behind, so the client can try
			// again with the same key. Use a fresh context: the request's may
			// be cancelled by now.
			bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if completed && rec.status < 500 {
				if _, err := s.pool.Exec(bg, `
					UPDATE idempotency_keys SET status = $3, content_type = $4, response = $5
					WHERE scope = $1 AND key = $2`,
					scope, key, rec.status, rec.Header().Get("Content-Type"), rec.body.Bytes()); err != nil {
					s.logger.Error("idempotency save", "err", err)
				}
				return
			}
			if _, err := s.pool.Exec(bg, `DELETE FROM idempotency_keys WHERE scope = $1 AND key = $2`, scope, key); err != nil {
				s.logger.Error("idempotency release", "err", err)
			}
		}()
		next.ServeHTTP(rec, r)
		completed = true
	})
}

func (s *Server) replayIdempotent(w http.ResponseWriter, r *http.Request, scope, key, hash string) {
	var (
		method, path, bodyHash string
		status                 *int32
		contentType            *string
		response               []byte
	)
	err := s.pool.QueryRow(r.Context(), `
		SELECT method, path, body_hash, status, content_type, response
		FROM idempotency_keys WHERE scope = $1 AND key = $2`, scope, key).
		Scan(&method, &path, &bodyHash, &status, &contentType, &response)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// The first try failed and let the key go between our insert and
		// this read: ask the client to send it again.
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "the first request with this key just failed; send it again", "code": codeInProgress})
		return
	case err != nil:
		s.logger.Error("idempotency read", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not process the request"})
		return
	}
	if method != r.Method || path != r.URL.Path || bodyHash != hash {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error": "this Idempotency-Key was used for a different request", "code": codeKeyReused})
		return
	}
	if status == nil {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "the first request with this key is still running", "code": codeInProgress})
		return
	}
	if contentType != nil && *contentType != "" {
		w.Header().Set("Content-Type", *contentType)
	}
	w.Header().Set(headerReplayed, "true")
	w.WriteHeader(int(*status))
	w.Write(response)
}

// sweepIdempotencyKeys deletes expired keys, at most once an hour.
func (s *Server) sweepIdempotencyKeys(ctx context.Context) {
	now := time.Now().Unix()
	last := lastIdempotencySweep.Load()
	if now-last < 3600 || !lastIdempotencySweep.CompareAndSwap(last, now) {
		return
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM idempotency_keys WHERE created_at < $1`,
		time.Now().Add(-idempotencyTTL)); err != nil {
		s.logger.Error("idempotency sweep", "err", err)
	}
}

// recordingWriter passes a response through and keeps a copy of it.
type recordingWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
	body        bytes.Buffer
}

func (rw *recordingWriter) WriteHeader(status int) {
	if !rw.wroteHeader {
		rw.status = status
		rw.wroteHeader = true
	}
	rw.ResponseWriter.WriteHeader(status)
}

func (rw *recordingWriter) Write(b []byte) (int, error) {
	if !rw.wroteHeader {
		rw.WriteHeader(http.StatusOK)
	}
	rw.body.Write(b)
	return rw.ResponseWriter.Write(b)
}

func (rw *recordingWriter) Unwrap() http.ResponseWriter { return rw.ResponseWriter }
