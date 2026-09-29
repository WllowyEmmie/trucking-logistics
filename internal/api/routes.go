package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

func (s *Server) Routes() http.Handler {
	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(middleware.Logger)
	router.Use(middleware.Recoverer)
	router.Use(middleware.Timeout(15 * time.Second))

	router.Get("/health", s.health)
	router.Get("/track/{token}", s.trackDelivery)

	router.Route("/trucks", func(trucks chi.Router) {
		trucks.Get("/", s.listTrucks)
		trucks.Post("/", s.createTruck)
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
	})

	return router
}
