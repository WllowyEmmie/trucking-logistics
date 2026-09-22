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

	router.Route("/trucks", func(trucks chi.Router) {
		trucks.Get("/", s.listTrucks)
		trucks.Post("/", s.createTruck)
	})
	router.Route("/drivers", func(drivers chi.Router) {
		drivers.Get("/", s.listDriver)
		drivers.Post("/", s.createDriver)
	})

	return router
}
