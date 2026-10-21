package api

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/WllowyEmmie/trucking-logistics/internal/config"
	"github.com/WllowyEmmie/trucking-logistics/internal/db"
	"github.com/WllowyEmmie/trucking-logistics/internal/geocode"
	"github.com/WllowyEmmie/trucking-logistics/internal/payments"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Server struct {
	pool    *pgxpool.Pool
	queries *db.Queries
	logger  *slog.Logger
	cfg     *config.Config
	hub     *hub
	// Failed sign-ins per account: see loginsBlocked.
	logins    *throttle
	loginsDay *throttle
	geocoder  geocode.Geocoder
	// One-minute passes for the live-updates socket (wsticket.go).
	tickets *ticketStore
	// The booking page (booking.go): Paystack (nil when not set up), and
	// limits per visitor.
	paystack       *payments.Paystack
	bookLimit      *throttle
	bookQuoteLimit *throttle
	bookGeoLimit   *throttle
	bookGeoGlobal  *throttle

	// The settings in effect (see config.go), swapped whole on save.
	settings   atomic.Pointer[config.Settings]
	settingsAt atomic.Pointer[time.Time]
	settingsBy atomic.Pointer[string]
}

func NewServer(pool *pgxpool.Pool, logger *slog.Logger, cfg *config.Config) *Server {
	s := &Server{
		pool:      pool,
		queries:   db.New(pool),
		logger:    logger,
		cfg:       cfg,
		hub:       newHub(logger),
		logins:    newThrottle(5, 15*time.Minute),
		loginsDay: newThrottle(20, 24*time.Hour),
		geocoder:  geocode.NewNominatim(cfg.GeocoderURL, cfg.GeocoderUserAgent, cfg.GeocoderEmail),
		tickets:   newTicketStore(),

		paystack:       payments.New(cfg.PaystackURL, cfg.PaystackSecretKey),
		bookLimit:      newThrottle(5, 10*time.Minute),
		bookQuoteLimit: newThrottle(60, 10*time.Minute),
		bookGeoLimit:   newThrottle(30, 10*time.Minute),
		bookGeoGlobal:  newThrottle(50, time.Minute),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s.loadSettings(ctx)
	return s
}
