package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
)

func (s *Server) Routes() http.Handler {
	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(middleware.Logger)
	router.Use(middleware.Recoverer)
	// "/trucks/" and "/trucks" are the same route.
	router.Use(middleware.StripSlashes)
	// No endpoint takes more than a 500-ping batch; anything bigger is a
	// mistake or an attack, and json.Decoder would otherwise read it all.
	// Proof of delivery carries a photo: that route alone gets more room.
	router.Use(limitBody(1<<20, proofBodyLimit))
	router.Use(cors.Handler(cors.Options{
		AllowedOrigins: s.cfg.AllowedOrigins,
		AllowedMethods: []string{"GET", "POST", "PATCH", "PUT", "OPTIONS"},
		AllowedHeaders: []string{"Content-Type", "Authorization"},
		// The browser apps go through Next's same-origin rewrite, so the
		// session cookie never needs to cross origins.
		AllowCredentials: false,
		MaxAge:           300,
	}))
	// Works out who's calling; requireRole below decides what they may do.
	router.Use(s.authenticate)
	router.Use(csrfGuard)
	// After a change, tell the open consoles what changed (live.go).
	router.Use(s.announceChanges)

	// The socket lives for as long as the client stays, so it sits outside
	// the request timeout: that would cancel r.Context() and drop it at 15 s.
	// The agent listens too, so auto mode reacts within seconds of a change.
	// A browser on another host signs in with a one-minute pass (wsticket.go).
	router.With(s.ticketAuth, requireRole(roleDispatcher, roleAgent)).Get("/ws", s.positionsSocket)
	router.With(requireRole(roleDispatcher)).Get("/ws/ticket", s.wsTicket)

	// The agent beside the API, when it runs on the same host (AGENT_INTERNAL_URL).
	// Outside the timeout: an answer can take a minute and a half.
	if p := s.agentProxy(); p != nil {
		router.Handle("/agent", p)
		router.Handle("/agent/*", p)
	}

	router.Group(func(r chi.Router) {
		r.Use(middleware.Timeout(15 * time.Second))

		// Public: no sign-in.
		r.Get("/health", s.health)
		r.Get("/config", s.getConfig)
		r.Get("/track/{token}", s.trackDelivery)
		r.Get("/track/{token}/stops/{seq}/{kind}", s.trackProofImage)
		r.Get("/book/geocode", s.bookGeocode)
		r.Post("/book/quote", s.bookQuote)
		r.Post("/book", s.book)
		r.Get("/book/{ref}", s.bookingStatus)
		r.Post("/paystack/webhook", s.paystackWebhook)
		r.Post("/auth/login", s.login)
		r.Post("/auth/driver-login", s.driverLogin)
		r.Post("/auth/logout", s.logout)
		r.Get("/auth/me", s.me)

		// Reads. Drivers see only their own truck, deliveries and themselves
		// (the handlers filter); the agent and dispatchers see everything.
		r.Group(func(r chi.Router) {
			r.Use(requireRole(roleDispatcher, roleDriver, roleAgent))
			r.Get("/trucks", s.listTrucks)
			r.Get("/drivers", s.listDriver)
			r.Get("/deliveries", s.listDeliveries)
			r.Get("/deliveries/{id}", s.ownDeliveryOnly(s.getDelivery))
		})

		// What the driver's phone does. A driver only for their own truck;
		// a dispatcher for any (to step in when a phone dies).
		r.Group(func(r chi.Router) {
			r.Use(requireRole(roleDispatcher, roleDriver))
			r.Post("/locations", s.ingestLocations)
			r.Post("/deliveries/{id}/start", s.ownDeliveryOnly(s.startDelivery))
			r.Post("/deliveries/{id}/complete", s.ownDeliveryOnly(s.completeDelivery))
			r.Post("/deliveries/{id}/fail", s.ownDeliveryOnly(s.failDelivery))
			r.Post("/deliveries/{id}/stops/{stopId}/arrive", s.ownDeliveryOnly(s.arriveAtStop))
			r.Post("/deliveries/{id}/stops/{stopId}/complete", s.ownDeliveryOnly(s.completeStop))
			r.Post("/deliveries/{id}/stops/{stopId}/proof", s.ownDeliveryOnly(s.saveProof))
			r.Get("/deliveries/{id}/stops/{stopId}/proof/{kind}", s.ownDeliveryOnly(s.proofImage))
			r.Post("/auth/me/shift", s.setMyShift)
		})

		// Dispatchers only.
		r.Group(func(r chi.Router) {
			r.Use(requireRole(roleDispatcher))
			r.Get("/geocode", s.geocodeAddress)
			r.Post("/trucks", s.createTruck)
			r.Patch("/trucks/{id}", s.updateTruck)
			r.Post("/drivers", s.createDriver)
			r.Post("/drivers/{id}/pin", s.setDriverPIN)
			r.Post("/drivers/{id}/pin/generate", s.generateDriverPIN)
			r.Post("/drivers/{id}/shift/end", s.endDriverShift)
			r.Post("/drivers/{id}/shift/start", s.startDriverShift)
			r.Post("/deliveries", s.createDelivery)
			r.Post("/deliveries/{id}/assign", s.assignTruck)
			r.Post("/deliveries/{id}/cancel", s.cancelDelivery)
			r.Put("/config", s.putConfig)
			r.Post("/config/check", s.checkArea)
			r.Post("/deliveries/{id}/reopen", s.reopenDelivery)
			r.Post("/deliveries/{id}/due", s.setDue)
			r.Get("/deliveries/history", s.deliveryHistory)
			r.Post("/proposals/{id}/approve", s.approveProposal)
			r.Post("/proposals/{id}/reject", s.rejectProposal)
		})

		// The queue: dispatchers read it, only the agent writes to it.
		r.With(requireRole(roleDispatcher, roleAgent)).Get("/proposals", s.listProposals)
		r.With(requireRole(roleAgent)).Post("/proposals", s.createProposal)

		// Auto mode (supervisor.go): the agent acts, dispatchers see what it did.
		r.With(requireRole(roleAgent)).Post("/supervisor/pair", s.supervisorPair)
		r.With(requireRole(roleAgent)).Post("/supervisor/assign", s.supervisorAssign)
		r.With(requireRole(roleDispatcher, roleAgent)).Get("/supervisor/actions", s.supervisorActions)
	})

	return router
}

func limitBody(n, proof int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil {
				max := n
				if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/proof") {
					max = proof
				}
				r.Body = http.MaxBytesReader(w, r.Body, max)
			}
			next.ServeHTTP(w, r)
		})
	}
}
