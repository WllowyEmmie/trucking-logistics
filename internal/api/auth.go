package api

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/WllowyEmmie/trucking-logistics/internal/auth"
)

// Roles. A request is one of these, or anonymous.
const (
	roleDispatcher = "dispatcher"
	roleDriver     = "driver"
	roleAgent      = "agent"
)

const sessionCookie = "session"

// principal is who a request is from.
type principal struct {
	Role  string
	ID    uuid.UUID // dispatcher or driver id; zero for the agent
	Name  string
	Email string // dispatchers only
	// The trucks a driver may act for: theirs as usual driver.
	Trucks []uuid.UUID
	// Signed in with the cookie (a browser) rather than a bearer token.
	viaCookie bool
}

type ctxKey struct{}

func principalFrom(ctx context.Context) *principal {
	p, _ := ctx.Value(ctxKey{}).(*principal)
	return p
}

// ownsTruck: a dispatcher may act for any truck, a driver only for theirs.
func (p *principal) ownsTruck(id uuid.UUID) bool {
	if p == nil {
		return false
	}
	if p.Role == roleDispatcher {
		return true
	}
	return p.Role == roleDriver && slices.Contains(p.Trucks, id)
}

// presentedToken is the session token or service key on a request: the
// Authorization bearer header first (the agent, scripts), then the cookie.
func presentedToken(r *http.Request) (token string, viaCookie bool) {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer ")), false
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		return c.Value, true
	}
	return "", false
}

// authenticate works out who the request is from and stores it in the
// context. It never refuses anything itself: requireRole does that, so
// public routes keep working with a stale cookie.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, viaCookie := presentedToken(r)
		if token == "" {
			next.ServeHTTP(w, r)
			return
		}
		if !viaCookie && auth.SameKey(token, s.cfg.AgentAPIKey) {
			p := &principal{Role: roleAgent, Name: "agent"}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, p)))
			return
		}
		p, err := s.loadSession(r.Context(), token)
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				s.logger.Error("load session", "err", err)
			}
			next.ServeHTTP(w, r)
			return
		}
		p.viaCookie = viaCookie
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, p)))
	})
}

func (s *Server) loadSession(ctx context.Context, token string) (*principal, error) {
	sess, err := s.queries.GetSession(ctx, auth.HashToken(token))
	if err != nil {
		return nil, err
	}
	switch sess.Role {
	case roleDispatcher:
		d, err := s.queries.GetDispatcher(ctx, sess.SubjectID)
		if err != nil {
			return nil, err
		}
		return &principal{Role: roleDispatcher, ID: d.ID, Name: d.Name, Email: d.Email}, nil
	case roleDriver:
		d, err := s.queries.GetDriverLogin(ctx, sess.SubjectID)
		if err != nil {
			return nil, err
		}
		trucks, err := s.queries.TrucksForDriver(ctx, d.ID)
		if err != nil {
			return nil, err
		}
		return &principal{Role: roleDriver, ID: d.ID, Name: d.Name, Trucks: trucks}, nil
	}
	return nil, pgx.ErrNoRows
}

// requireRole lets through only the listed roles: 401 for nobody, 403 for
// someone signed in as the wrong kind of user.
func requireRole(roles ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := principalFrom(r.Context())
			if p == nil {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "sign in first"})
				return
			}
			if !slices.Contains(roles, p.Role) {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "not allowed for a " + p.Role})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// csrfGuard: a browser signed in by cookie must send JSON on anything that
// changes data. Another site can make a browser POST a form or plain text
// with the cookie attached, but not application/json without a CORS
// preflight this API refuses. SameSite=Lax on the cookie covers most of
// this already; this is the second lock.
func csrfGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := principalFrom(r.Context())
		if p != nil && p.viaCookie && r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if mt != "application/json" {
				writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "send JSON (Content-Type: application/json)"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// ---- Login throttling ----

// throttle counts failed logins per account and per client address, so a
// four-digit PIN can't be guessed by brute force. In memory: one API
// process. Behind several instances, move this to Postgres or Redis.
type throttle struct {
	mu    sync.Mutex
	fails map[string][]time.Time
	max   int
	win   time.Duration
}

func newThrottle(max int, win time.Duration) *throttle {
	return &throttle{fails: map[string][]time.Time{}, max: max, win: win}
}

func (t *throttle) recent(key string, now time.Time) []time.Time {
	keep := t.fails[key][:0]
	for _, at := range t.fails[key] {
		if now.Sub(at) < t.win {
			keep = append(keep, at)
		}
	}
	if len(keep) == 0 {
		delete(t.fails, key)
		return nil
	}
	t.fails[key] = keep
	return keep
}

func (t *throttle) blocked(keys ...string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	for _, k := range keys {
		if len(t.recent(k, now)) >= t.max {
			return true
		}
	}
	return false
}

func (t *throttle) fail(keys ...string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	for _, k := range keys {
		t.fails[k] = append(t.recent(k, now), now)
	}
}

func (t *throttle) clear(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.fails, key)
}

// Five failures in 15 minutes, or twenty in a day, lock an account's sign-in.
// The daily cap is what makes a 4-digit PIN slow to guess: at most twenty
// tries a day.
func (s *Server) loginsBlocked(acct string) bool {
	return s.logins.blocked(acct) || s.loginsDay.blocked(acct)
}

func (s *Server) loginFailed(acct string) {
	s.logins.fail(acct)
	s.loginsDay.fail(acct)
}

// ---- Handlers ----

type meResponse struct {
	Role  string     `json:"role"`
	ID    *uuid.UUID `json:"id,omitempty"`
	Name  string     `json:"name"`
	Email string     `json:"email,omitempty"`
	// Drivers: the truck this phone reports for (the first, if several).
	TruckID *uuid.UUID `json:"truck_id,omitempty"`
	// Drivers: whether they're on shift, and since when.
	OnShift    *bool      `json:"on_shift,omitempty"`
	ShiftSince *time.Time `json:"shift_since,omitempty"`
}

func meOf(p *principal) meResponse {
	m := meResponse{Role: p.Role, Name: p.Name, Email: p.Email}
	if p.ID != uuid.Nil {
		id := p.ID
		m.ID = &id
	}
	if len(p.Trucks) > 0 {
		t := p.Trucks[0]
		m.TruckID = &t
	}
	return m
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	if p == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "not signed in"})
		return
	}
	m := meOf(p)
	if p.Role == roleDriver {
		since, err := s.queries.OpenShift(r.Context(), p.ID)
		if err != nil {
			s.respondErr(w, err, "open shift", "could not load your account")
			return
		}
		on := since != nil
		m.OnShift, m.ShiftSince = &on, since
	}
	writeJSON(w, http.StatusOK, m)
}

const badLogin = "wrong email or password"

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	email := strings.ToLower(strings.TrimSpace(body.Email))
	// Counted per account, not per address: behind Next's rewrite every
	// request comes from the Next server, so an address limit would let one
	// person lock everyone out.
	acct := "d:" + email
	if s.loginsBlocked(acct) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many failed sign-ins; try again in 15 minutes"})
		return
	}
	d, err := s.queries.GetDispatcherByEmail(r.Context(), email)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			s.respondErr(w, err, "find dispatcher", "could not sign in")
			return
		}
		auth.SpendTime(body.Password)
		s.loginFailed(acct)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": badLogin})
		return
	}
	if !auth.CheckSecret(d.PasswordHash, body.Password) {
		s.loginFailed(acct)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": badLogin})
		return
	}
	s.logins.clear(acct)
	s.loginsDay.clear(acct)
	s.startSession(w, r, roleDispatcher, d.ID, s.cfg.DispatcherSessionTTL,
		meOf(&principal{Role: roleDispatcher, ID: d.ID, Name: d.Name, Email: d.Email}))
}

const badDriverLogin = "wrong phone number or PIN"

func (s *Server) driverLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Phone string `json:"phone"`
		PIN   string `json:"pin"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	phone := auth.NormPhone(body.Phone)
	acct := "p:" + phone
	if s.loginsBlocked(acct) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many failed sign-ins; try again in 15 minutes or call dispatch"})
		return
	}
	d, err := s.queries.GetDriverByPhone(r.Context(), phone)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		s.respondErr(w, err, "find driver", "could not sign in")
		return
	}
	if err != nil || d.PinHash == nil || !auth.CheckSecret(*d.PinHash, body.PIN) {
		if err != nil || d.PinHash == nil {
			auth.SpendTime(body.PIN)
		}
		s.loginFailed(acct)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": badDriverLogin})
		return
	}
	s.logins.clear(acct)
	s.loginsDay.clear(acct)
	trucks, err := s.queries.TrucksForDriver(r.Context(), d.ID)
	if err != nil {
		s.respondErr(w, err, "driver trucks", "could not sign in")
		return
	}
	// Signing in to the app starts the shift: a driver opens it to go to
	// work. They end it in the app (or dispatch does) when they're done.
	since, err := s.queries.StartShift(r.Context(), d.ID)
	if err != nil {
		s.respondErr(w, err, "start shift", "could not sign in")
		return
	}
	// The app takes this as who's signed in, so it says they're on shift.
	m := meOf(&principal{Role: roleDriver, ID: d.ID, Name: d.Name, Trucks: trucks})
	on := true
	m.OnShift, m.ShiftSince = &on, &since
	s.startSession(w, r, roleDriver, d.ID, s.cfg.DriverSessionTTL, m)
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, role string, subject uuid.UUID, ttl time.Duration, me meResponse) {
	token, hash, err := auth.NewToken()
	if err != nil {
		s.respondErr(w, err, "new token", "could not sign in")
		return
	}
	expires := time.Now().Add(ttl)
	if err := s.queries.CreateSession(r.Context(), hash, role, subject, expires); err != nil {
		s.respondErr(w, err, "create session", "could not sign in")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true, // page scripts can't read it, so XSS can't steal it
		Secure:   s.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, http.StatusOK, me)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if token, _ := presentedToken(r); token != "" && !auth.SameKey(token, s.cfg.AgentAPIKey) {
		if err := s.queries.DeleteSession(r.Context(), auth.HashToken(token)); err != nil {
			s.logger.Error("delete session", "err", err)
		}
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteLaxMode})
	w.WriteHeader(http.StatusNoContent)
}

// setDriverPIN is POST /drivers/{id}/pin {pin}: a dispatcher sets or resets a
// driver's PIN. The driver's phones are signed out.
func (s *Server) setDriverPIN(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid driver id"})
		return
	}
	var body struct {
		PIN string `json:"pin"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if err := auth.ValidPIN(body.PIN); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	hash, err := auth.HashSecret(body.PIN)
	if err != nil {
		s.respondErr(w, err, "hash pin", "could not set the PIN")
		return
	}
	ok, err := s.queries.SetDriverPIN(r.Context(), id, hash)
	if err != nil {
		s.respondErr(w, err, "set pin", "could not set the PIN")
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "driver not found"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// generateDriverPIN is POST /drivers/{id}/pin/generate: a dispatcher gets a
// fresh random PIN to pass on to the driver. It's shown this once and only
// its hash is kept. The old PIN stops working and the driver's phones are
// signed out, so regenerating is also how to lock out a lost phone.
func (s *Server) generateDriverPIN(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid driver id"})
		return
	}
	pin, ok := s.newPINFor(w, r, id)
	if !ok {
		return
	}
	// Never cached anywhere on the way back.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"pin": pin})
}

// newPINFor generates, stores and returns a PIN for a driver. On failure it
// has already answered the request.
func (s *Server) newPINFor(w http.ResponseWriter, r *http.Request, id uuid.UUID) (string, bool) {
	pin, err := auth.GeneratePIN()
	if err == nil {
		var hash string
		if hash, err = auth.HashSecret(pin); err == nil {
			var found bool
			found, err = s.queries.SetDriverPIN(r.Context(), id, hash)
			if err == nil && !found {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "driver not found"})
				return "", false
			}
		}
	}
	if err != nil {
		s.respondErr(w, err, "generate pin", "could not make a PIN")
		return "", false
	}
	return pin, true
}

// ---- Driver scoping ----

// deliveryForPrincipal checks a driver may act on a delivery: it must be on
// one of their trucks. Dispatchers may act on any. Answers 404 rather than
// 403 for someone else's delivery, so drivers can't probe which ids exist.
func (s *Server) deliveryForPrincipal(w http.ResponseWriter, r *http.Request, id uuid.UUID) bool {
	p := principalFrom(r.Context())
	if p != nil && p.Role != roleDriver {
		return true
	}
	d, err := s.queries.GetDelivery(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "delivery not found"})
			return false
		}
		s.respondErr(w, err, "get delivery", "could not load delivery")
		return false
	}
	if d.TruckID == nil || !p.ownsTruck(*d.TruckID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "delivery not found"})
		return false
	}
	return true
}

// ownDeliveryOnly wraps a /deliveries/{id}/... handler with that check.
func (s *Server) ownDeliveryOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid delivery id"})
			return
		}
		if s.deliveryForPrincipal(w, r, id) {
			next(w, r)
		}
	}
}

// actorName is who to record as having made a decision.
func actorName(r *http.Request) string {
	p := principalFrom(r.Context())
	switch {
	case p == nil:
		return "unknown"
	case p.Email != "":
		return p.Email
	default:
		return p.Role + ":" + p.Name
	}
}

// onlyMine filters a list for a driver; everyone else gets it whole.
func onlyMine[T any](r *http.Request, rows []T, keep func(p *principal, row T) bool) []T {
	p := principalFrom(r.Context())
	if p == nil || p.Role != roleDriver {
		return rows
	}
	out := make([]T, 0, len(rows))
	for _, row := range rows {
		if keep(p, row) {
			out = append(out, row)
		}
	}
	return out
}

func onTruckOf(p *principal, truckID *uuid.UUID) bool {
	return truckID != nil && p.ownsTruck(*truckID)
}
