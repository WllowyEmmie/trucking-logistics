package api

import (
	"log/slog"

	"github.com/WllowyEmmie/trucking-logistics/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Server struct {
	pool    *pgxpool.Pool
	queries *db.Queries
	logger  *slog.Logger
}
func NewServer(pool *pgxpool.Pool,  logger *slog.Logger, )*Server{
	return &Server{
		pool: pool,
		queries: db.New(pool),
		logger: logger,
	}
}
