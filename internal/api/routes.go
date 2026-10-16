package api

import (
	"net/http"
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
	router.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"http://localhost:3000", "http://localhost:3471"},
		AllowedMethods:   []string{"GET", "POST", "PATCH", "OPTIONS"},
		AllowedHeaders:   []string{"Content-Type"},
		AllowCredentials: false,
		MaxAge:           300,
	}))

	// The socket lives for as long as the client stays, so it sits outside
	// the request timeout: that would cancel r.Context() and drop it at 15 s.
	router.Get("/ws", s.positionsSocket)

	router.Group(func(router chi.Router) {
		router.Use(middleware.Timeout(15 * time.Second))

		router.Get("/health", s.health)
		router.Get("/config", s.getConfig)
		router.Get("/geocode", s.geocodeAddress)
		router.Get("/track/{token}", s.trackDelivery)

		router.Post("/locations", s.ingestLocations)

		router.Route("/trucks", func(trucks chi.Router) {
			trucks.Get("/", s.listTrucks)
			trucks.Post("/", s.createTruck)
			trucks.Patch("/{id}", s.updateTruck)
		})
		router.Route("/drivers", func(drivers chi.Router) {
			drivers.Get("/", s.listDriver)
			drivers.Post("/", s.createDriver)
		})
		router.Route("/deliveries", func(deliveries chi.Router) {
			deliveries.Post("/", s.createDelivery)
			deliveries.Post("/{id}/assign", s.assignTruck)
			deliveries.Post("/{id}/complete", s.completeDelivery)
			deliveries.Post("/{id}/cancel", s.cancelDelivery)
			deliveries.Post("/{id}/start", s.startDelivery)
			deliveries.Post("/{id}/fail", s.failDelivery)
			deliveries.Get("/", s.listDeliveries)
			deliveries.Get("/{id}", s.getDelivery)
			deliveries.Post("/{id}/stops/{stopId}/arrive", s.arriveAtStop)
			deliveries.Post("/{id}/stops/{stopId}/complete", s.completeStop)
		})
		router.Route("/proposals", func(proposals chi.Router) {
			proposals.Get("/", s.listProposals)
			proposals.Post("/", s.createProposal)
			proposals.Post("/{id}/approve", s.approveProposal)
			proposals.Post("/{id}/reject", s.rejectProposal)
		})
	})

	return router
}
