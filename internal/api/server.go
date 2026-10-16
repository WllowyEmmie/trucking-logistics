package api

import (
	"log/slog"

	"github.com/WllowyEmmie/trucking-logistics/internal/config"
	"github.com/WllowyEmmie/trucking-logistics/internal/db"
	"github.com/WllowyEmmie/trucking-logistics/internal/geocode"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Server struct {
	pool     *pgxpool.Pool
	queries  *db.Queries
	logger   *slog.Logger
	cfg      *config.Config
	hub      *hub
	geocoder geocode.Geocoder
}

func NewServer(pool *pgxpool.Pool, logger *slog.Logger, cfg *config.Config) *Server {
	return &Server{
		pool:     pool,
		queries:  db.New(pool),
		logger:   logger,
		cfg:      cfg,
		hub:      newHub(logger),
		geocoder: geocode.NewNominatim(cfg.GeocoderURL, cfg.GeocoderUserAgent, cfg.GeocoderEmail),
	}
}
