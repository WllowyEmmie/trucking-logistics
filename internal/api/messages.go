package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Messages between dispatch and drivers: one thread per driver.
//
//	GET  /messages                    threads, newest first (dispatchers)
//	GET  /drivers/{id}/messages       a thread, oldest first (dispatchers)
//	POST /drivers/{id}/messages       {"body"} send to a driver (dispatchers)
//	POST /drivers/{id}/messages/read  mark the driver's messages read (dispatchers)
//	GET  /auth/me/messages            the driver's own thread, and how many are unread
//	POST /auth/me/messages            {"body"} the driver writes to dispatch
//	POST /auth/me/messages/read       the driver opened the thread
//
// Every write is announced on /ws as topic "messages" (live.go), so consoles
// refetch at once; drivers' phones get the message itself on /ws/driver
// (ws.go) and poll as a backstop.

const (
	messageMaxRunes  = 2000
	threadMaxMessage = 200
)

type threadResponse struct {
	Messages any `json:"messages"`
	Unread   int `json:"unread"`
}

func readMessageBody(w http.ResponseWriter, r *http.Request) (string, bool) {
	var body struct {
		Body string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `send {"body": "…"}`})
		return "", false
	}
	text := strings.TrimSpace(body.Body)
	if text == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the message is empty"})
		return "", false
	}
	if utf8.RuneCountInString(text) > messageMaxRunes {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the message is longer than 2000 characters"})
		return "", false
	}
	return text, true
}

// driverParam is the {id} driver, or false after answering 400/404.
func (s *Server) driverParam(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid driver id"})
		return uuid.Nil, false
	}
	if _, err := s.queries.GetDriverLogin(r.Context(), id); errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "driver not found"})
		return uuid.Nil, false
	} else if err != nil {
		s.respondErr(w, err, "get driver", "could not load the driver")
		return uuid.Nil, false
	}
	return id, true
}

func (s *Server) listMessageThreads(w http.ResponseWriter, r *http.Request) {
	threads, err := s.queries.MessageThreads(r.Context())
	if err != nil {
		s.respondErr(w, err, "list message threads", "could not load messages")
		return
	}
	writeJSON(w, http.StatusOK, threads)
}

func (s *Server) driverThread(w http.ResponseWriter, r *http.Request) {
	id, ok := s.driverParam(w, r)
	if !ok {
		return
	}
	msgs, err := s.queries.ThreadMessages(r.Context(), id, threadMaxMessage)
	if err != nil {
		s.respondErr(w, err, "load thread", "could not load messages")
		return
	}
	unread := 0
	for _, m := range msgs {
		if m.FromDriver && m.ReadAt == nil {
			unread++
		}
	}
	writeJSON(w, http.StatusOK, threadResponse{Messages: msgs, Unread: unread})
}

func (s *Server) messageDriver(w http.ResponseWriter, r *http.Request) {
	id, ok := s.driverParam(w, r)
	if !ok {
		return
	}
	text, ok := readMessageBody(w, r)
	if !ok {
		return
	}
	sender := "Dispatch"
	if p := principalFrom(r.Context()); p != nil && p.Name != "" {
		sender = p.Name
	}
	m, err := s.queries.AddMessage(r.Context(), id, false, sender, text)
	if err != nil {
		s.respondErr(w, err, "add message", "could not send the message")
		return
	}
	s.hub.toDriver(id, wsMessage{Type: "message", Message: m})
	writeJSON(w, http.StatusCreated, m)
}

func (s *Server) readDriverThread(w http.ResponseWriter, r *http.Request) {
	id, ok := s.driverParam(w, r)
	if !ok {
		return
	}
	n, err := s.queries.MarkThreadRead(r.Context(), id, true)
	if err != nil {
		s.respondErr(w, err, "mark read", "could not mark the messages read")
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"marked": n})
}

// meDriver is the signed-in driver, or false after answering 403.
func meDriver(w http.ResponseWriter, r *http.Request) (*principal, bool) {
	p := principalFrom(r.Context())
	if p == nil || p.Role != roleDriver {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "only a driver has a thread with dispatch"})
		return nil, false
	}
	return p, true
}

func (s *Server) myThread(w http.ResponseWriter, r *http.Request) {
	p, ok := meDriver(w, r)
	if !ok {
		return
	}
	msgs, err := s.queries.ThreadMessages(r.Context(), p.ID, threadMaxMessage)
	if err != nil {
		s.respondErr(w, err, "load thread", "could not load messages")
		return
	}
	unread := 0
	for _, m := range msgs {
		if !m.FromDriver && m.ReadAt == nil {
			unread++
		}
	}
	writeJSON(w, http.StatusOK, threadResponse{Messages: msgs, Unread: unread})
}

func (s *Server) messageDispatch(w http.ResponseWriter, r *http.Request) {
	p, ok := meDriver(w, r)
	if !ok {
		return
	}
	text, ok := readMessageBody(w, r)
	if !ok {
		return
	}
	m, err := s.queries.AddMessage(r.Context(), p.ID, true, p.Name, text)
	if err != nil {
		s.respondErr(w, err, "add message", "could not send the message")
		return
	}
	// The driver's other phones (a tablet in the cab) show it too.
	s.hub.toDriver(p.ID, wsMessage{Type: "message", Message: m})
	writeJSON(w, http.StatusCreated, m)
}

func (s *Server) readMyThread(w http.ResponseWriter, r *http.Request) {
	p, ok := meDriver(w, r)
	if !ok {
		return
	}
	n, err := s.queries.MarkThreadRead(r.Context(), p.ID, false)
	if err != nil {
		s.respondErr(w, err, "mark read", "could not mark the messages read")
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"marked": n})
}
