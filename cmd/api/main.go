package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"

	"github.com/WllowyEmmie/trucking-logistics/internal/api"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://fleet:fleet_dev_pw@127.0.0.1:5434/fleet?sslmode=disable"
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		logger.Error("create pool", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		logger.Error("ping db", "err", err)
		os.Exit(1)
	}
	logger.Info("connected to database")

	srv := api.NewServer(pool, logger)

	logger.Info("listening", "addr", ":8080")
	if err := http.ListenAndServe(":8080", srv.Routes()); err != nil {
		logger.Error("server stopped", "err", err)
		os.Exit(1)
	}
}
