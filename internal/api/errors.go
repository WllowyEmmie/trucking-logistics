package api

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgconn"
)

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

type apiError struct {
	status  int
	message string
	code    string // see respond.go; empty: from the status
}

func (e apiError) Error() string { return e.message }

func errConflict(msg string) error { return apiError{status: http.StatusConflict, message: msg} }
func errNotFound(msg string) error { return apiError{status: http.StatusNotFound, message: msg} }

// errConflictCode is a conflict with a specific code for clients to act on.
func errConflictCode(code, msg string) error {
	return apiError{status: http.StatusConflict, message: msg, code: code}
}

// body is the error as sent: {"error", "code"}.
func (e apiError) body() map[string]string {
	code := e.code
	if code == "" {
		code = codeForStatus(e.status)
	}
	return map[string]string{"error": e.message, "code": code}
}

// respondErr maps an error from a rule function onto the wire. Anything
// that isn't an apiError is a real failure: log it, tell the client nothing.
func (s *Server) respondErr(w http.ResponseWriter, err error, op, generic string) {
	var ae apiError
	if errors.As(err, &ae) {
		writeJSON(w, ae.status, ae.body())
		return
	}
	s.logger.Error(op, "err", err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": generic})
}
